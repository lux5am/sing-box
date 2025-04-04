package dialer

import (
	"context"
	"net"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type DirectDialer interface {
	IsEmpty() bool
}

type DetourDialer struct {
	router          adapter.Router
	detour          string
	legacyDNSDialer bool
	dialer          N.Dialer
	initOnce        sync.Once
	initErr         error
}

func NewDetour(router adapter.Router, detour string, legacyDNSDialer bool) N.Dialer {
	return &DetourDialer{
		router: router,
		detour: detour,
		legacyDNSDialer: legacyDNSDialer,
	}
}

func InitializeDetour(dialer N.Dialer) error {
	detourDialer, isDetour := common.Cast[*DetourDialer](dialer)
	if !isDetour {
		return nil
	}
	return common.Error(detourDialer.Dialer())
}

func (d *DetourDialer) Dialer() (N.Dialer, error) {
	d.initOnce.Do(d.init)
	return d.dialer, d.initErr
}

func (d *DetourDialer) init() {
	dialer, loaded := d.router.ProviderManager().OutboundWithProvider(d.detour)
	if !loaded {
		d.initErr = E.New("outbound detour not found: ", d.detour)
		return
	}
	if !d.legacyDNSDialer {
		if directDialer, isDirect := dialer.(DirectDialer); isDirect {
			if directDialer.IsEmpty() {
				d.initErr = E.New("detour to an empty direct outbound makes no sense")
				return
			}
		}
	}
	d.dialer = dialer
}

func (d *DetourDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	dialer, err := d.Dialer()
	if err != nil {
		return nil, err
	}
	if dialer.(adapter.Outbound).Type() != C.TypeDirect && d.router.Tracker() != nil {
		conn, err := dialer.DialContext(ctx, network, destination)
		if err != nil {
			return nil, err
		}
		metadata := adapter.InboundContext{
			InboundType: C.TypeInner,
			Network: network,
			Outbound: d.detour,
			Destination: destination,
		}
		return d.router.Tracker().RoutedConnection(ctx, conn, metadata, nil, dialer.(adapter.Outbound)), nil
	}
	return dialer.DialContext(ctx, network, destination)
}

func (d *DetourDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	dialer, err := d.Dialer()
	if err != nil {
		return nil, err
	}
	if dialer.(adapter.Outbound).Type() != C.TypeDirect && d.router.Tracker() != nil {
		conn, err := dialer.ListenPacket(ctx, destination)
		if err != nil {
			return nil, err
		}
		metadata := adapter.InboundContext{
			InboundType: C.TypeInner,
			Network: N.NetworkUDP,
			Outbound: d.detour,
			Destination: destination,
		}
		return d.router.Tracker().RoutedPacketConnection(ctx, bufio.NewPacketConn(conn), metadata, nil, dialer.(adapter.Outbound)).(net.PacketConn), nil
	}
	return dialer.ListenPacket(ctx, destination)
}

func (d *DetourDialer) Upstream() any {
	detour, _ := d.Dialer()
	return detour
}
