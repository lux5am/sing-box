package dhcp

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-dns"
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/control"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/task"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"

	"github.com/insomniacslk/dhcp/dhcpv4"
	mDNS "github.com/miekg/dns"
)

func init() {
	dns.RegisterUpstream([]string{"dhcp"}, func(options dns.UpstreamOptions) (dns.Upstream, error) {
		return NewUpstream(options)
	})
}

type Upstream struct {
	options           dns.UpstreamOptions
	router            adapter.Router
	networkManager    adapter.NetworkManager
	interfaceName     string
	autoInterface     bool
	interfaceCallback *list.Element[tun.DefaultInterfaceUpdateCallback]
	upstreams         []dns.Upstream
	updateAccess      sync.Mutex
	updatedAt         time.Time
}

func NewUpstream(options dns.UpstreamOptions) (*Upstream, error) {
	linkURL, err := url.Parse(options.Address)
	if err != nil {
		return nil, err
	}
	if linkURL.Host == "" {
		return nil, E.New("missing interface name for DHCP")
	}
	upstream := &Upstream{
		options:        options,
		networkManager: service.FromContext[adapter.NetworkManager](options.Context),
		interfaceName:  linkURL.Host,
		autoInterface:  linkURL.Host == "auto",
	}
	return upstream, nil
}

func (t *Upstream) Start() error {
	err := t.fetchServers()
	if err != nil {
		return err
	}
	if t.autoInterface {
		t.interfaceCallback = t.networkManager.InterfaceMonitor().RegisterCallback(t.interfaceUpdated)
	}
	return nil
}

func (t *Upstream) Reset() {
	for _, upstream := range t.upstreams {
		upstream.Reset()
	}
}

func (t *Upstream) Close() error {
	for _, upstream := range t.upstreams {
		upstream.Close()
	}
	if t.interfaceCallback != nil {
		t.networkManager.InterfaceMonitor().UnregisterCallback(t.interfaceCallback)
	}
	return nil
}

func (t *Upstream) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	err := t.fetchServers()
	if err != nil {
		return nil, err
	}

	if len(t.upstreams) == 0 {
		return nil, E.New("dhcp: empty DNS servers from response")
	}

	var response *mDNS.Msg
	for _, upstream := range t.upstreams {
		response, err = upstream.Exchange(ctx, message)
		if err == nil {
			return response, nil
		}
	}
	return nil, err
}

func (t *Upstream) fetchInterface() (*control.Interface, error) {
	if t.autoInterface {
		if t.networkManager.InterfaceMonitor() == nil {
			return nil, E.New("missing monitor for auto DHCP, set route.auto_detect_interface")
		}
		defaultInterface := t.networkManager.InterfaceMonitor().DefaultInterface()
		if defaultInterface == nil {
			return nil, E.New("missing default interface")
		}
		return defaultInterface, nil
	} else {
		return t.networkManager.InterfaceFinder().ByName(t.interfaceName)
	}
}

func (t *Upstream) fetchServers() error {
	if time.Since(t.updatedAt) < C.DHCPTTL {
		return nil
	}
	t.updateAccess.Lock()
	defer t.updateAccess.Unlock()
	if time.Since(t.updatedAt) < C.DHCPTTL {
		return nil
	}
	return t.updateServers()
}

func (t *Upstream) updateServers() error {
	iface, err := t.fetchInterface()
	if err != nil {
		return E.Cause(err, "dhcp: prepare interface")
	}

	t.options.Logger.Info("dhcp: query DNS servers on ", iface.Name)
	fetchCtx, cancel := context.WithTimeout(t.options.Context, C.DHCPTimeout)
	err = t.fetchServers0(fetchCtx, iface)
	cancel()
	if err != nil {
		return err
	} else if len(t.upstreams) == 0 {
		return E.New("dhcp: empty DNS servers response")
	} else {
		t.updatedAt = time.Now()
		return nil
	}
}

func (t *Upstream) interfaceUpdated(defaultInterface *control.Interface, flags int) {
	err := t.updateServers()
	if err != nil {
		t.options.Logger.Error("update servers: ", err)
	}
}

func (t *Upstream) fetchServers0(ctx context.Context, iface *control.Interface) error {
	var listener net.ListenConfig
	listener.Control = control.Append(listener.Control, control.BindToInterface(t.networkManager.InterfaceFinder(), iface.Name, iface.Index))
	listener.Control = control.Append(listener.Control, control.ReuseAddr())
	listenAddr := "0.0.0.0:68"
	if runtime.GOOS == "linux" || runtime.GOOS == "android" {
		listenAddr = "255.255.255.255:68"
	}
	packetConn, err := listener.ListenPacket(t.options.Context, "udp4", listenAddr)
	if err != nil {
		return err
	}
	defer packetConn.Close()

	discovery, err := dhcpv4.NewDiscovery(iface.HardwareAddr, dhcpv4.WithBroadcast(true), dhcpv4.WithRequestedOptions(dhcpv4.OptionDomainNameServer))
	if err != nil {
		return err
	}

	_, err = packetConn.WriteTo(discovery.ToBytes(), &net.UDPAddr{IP: net.IPv4bcast, Port: 67})
	if err != nil {
		return err
	}

	var group task.Group
	group.Append0(func(ctx context.Context) error {
		return t.fetchServersResponse(iface, packetConn, discovery.TransactionID)
	})
	group.Cleanup(func() {
		packetConn.Close()
	})
	return group.Run(ctx)
}

func (t *Upstream) fetchServersResponse(iface *control.Interface, packetConn net.PacketConn, transactionID dhcpv4.TransactionID) error {
	buffer := buf.NewSize(dhcpv4.MaxMessageSize)
	defer buffer.Release()

	for {
		_, _, err := buffer.ReadPacketFrom(packetConn)
		if err != nil {
			return err
		}

		dhcpPacket, err := dhcpv4.FromBytes(buffer.Bytes())
		if err != nil {
			t.options.Logger.Trace("dhcp: parse DHCP response: ", err)
			return err
		}

		if dhcpPacket.MessageType() != dhcpv4.MessageTypeOffer {
			t.options.Logger.Trace("dhcp: expected OFFER response, but got ", dhcpPacket.MessageType())
			continue
		}

		if dhcpPacket.TransactionID != transactionID {
			t.options.Logger.Trace("dhcp: expected transaction ID ", transactionID, ", but got ", dhcpPacket.TransactionID)
			continue
		}

		dns := dhcpPacket.DNS()
		if len(dns) == 0 {
			return nil
		}

		var addrs []netip.Addr
		for _, ip := range dns {
			addr, _ := netip.AddrFromSlice(ip)
			addrs = append(addrs, addr.Unmap())
		}
		return t.recreateServers(iface, addrs)
	}
}

func (t *Upstream) recreateServers(iface *control.Interface, serverAddrs []netip.Addr) error {
	if len(serverAddrs) > 0 {
		t.options.Logger.Info("dhcp: updated DNS servers from ", iface.Name, ": [", strings.Join(common.Map(serverAddrs, func(it netip.Addr) string {
			return it.String()
		}), ","), "]")
	}
	serverDialer := common.Must1(dialer.NewDefault(t.options.Context, option.DialerOptions{
		BindInterface:      iface.Name,
		UDPFragmentDefault: true,
	}))
	var upstreams []dns.Upstream
	for _, serverAddr := range serverAddrs {
		serverUpstream, err := dns.NewUDPUpstream(dns.UpstreamOptions{
			Context: t.options.Context,
			Logger:  t.options.Logger,
			Address: serverAddr.String(),
			Dialer:  serverDialer,
		})
		if err != nil {
			return E.Cause(err, "create UDP transport from DHCP result: ", serverAddr)
		}
		upstreams = append(upstreams, serverUpstream)
	}
	for _, upstream := range t.upstreams {
		upstream.Close()
	}
	t.upstreams = upstreams
	return nil
}

func (t *Upstream) Lookup(ctx context.Context, domain string, strategy dns.DomainStrategy) ([]netip.Addr, error) {
	return nil, os.ErrInvalid
}
