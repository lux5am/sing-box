package dialer

import (
	"context"
	"net"
	"net/netip"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-dns"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type ResolveDialer struct {
	addresses     []netip.Addr
	dialer        N.Dialer
	parallel      bool
	router        adapter.Router
	strategy      dns.DomainStrategy
	fallbackDelay time.Duration
}

func NewResolveDialer(router adapter.Router, dialer N.Dialer, addresses []option.ListenAddress, parallel bool, strategy dns.DomainStrategy, fallbackDelay time.Duration) *ResolveDialer {
	var addrs []netip.Addr
	addrsMap := make(map[string]struct{})
	if len(addresses) > 0 {
		for _, address := range addresses {
			addr := address.Build()
			if _, exists := addrsMap[addr.String()]; exists {
				continue
			}
			addrs = append(addrs, addr)
			addrsMap[addr.String()] = struct{}{}
		}
	}
	return &ResolveDialer{
		addrs,
		dialer,
		parallel,
		router,
		strategy,
		fallbackDelay,
	}
}

func (d *ResolveDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if !destination.IsFqdn() {
		return d.dialer.DialContext(ctx, network, destination)
	}
	ctx, metadata := adapter.ExtendContext(ctx)
	ctx = log.ContextWithOverrideLevel(ctx, log.LevelDebug)
	metadata.Destination = destination
	metadata.Domain = ""
	var addresses []netip.Addr
	var err error
	if len(d.addresses) > 0 {
		addresses = d.addresses
	} else if d.strategy == dns.DomainStrategyAsIS {
		addresses, err = d.router.LookupDefault(ctx, destination.Fqdn)
	} else {
		addresses, err = d.router.Lookup(ctx, destination.Fqdn, d.strategy)
	}
	if err != nil {
		return nil, err
	}
	if d.parallel {
		return N.DialParallel(ctx, d.dialer, network, destination, addresses, d.strategy == dns.DomainStrategyPreferIPv6, d.fallbackDelay)
	} else {
		return N.DialSerial(ctx, d.dialer, network, destination, addresses)
	}
}

func (d *ResolveDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if !destination.IsFqdn() {
		return d.dialer.ListenPacket(ctx, destination)
	}
	ctx, metadata := adapter.ExtendContext(ctx)
	ctx = log.ContextWithOverrideLevel(ctx, log.LevelDebug)
	metadata.Destination = destination
	metadata.Domain = ""
	var addresses []netip.Addr
	var err error
	if d.strategy == dns.DomainStrategyAsIS {
		addresses, err = d.router.LookupDefault(ctx, destination.Fqdn)
	} else {
		addresses, err = d.router.Lookup(ctx, destination.Fqdn, d.strategy)
	}
	if err != nil {
		return nil, err
	}
	conn, destinationAddress, err := N.ListenSerial(ctx, d.dialer, destination, addresses)
	if err != nil {
		return nil, err
	}
	return bufio.NewNATPacketConn(bufio.NewPacketConn(conn), M.SocksaddrFrom(destinationAddress, destination.Port), destination), nil
}

func (d *ResolveDialer) Upstream() any {
	return d.dialer
}
