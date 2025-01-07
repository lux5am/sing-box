package outbound

import (
	"context"
	"io"
	"net"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var _ adapter.Outbound = (*Pass)(nil)

type Pass struct {
	myOutboundAdapter
}

func NewPass(logger log.ContextLogger, tag string) *Pass {
	return &Pass{
		myOutboundAdapter{
			protocol: C.TypePass,
			network:  []string{N.NetworkTCP, N.NetworkUDP},
			logger:   logger,
			tag:      tag,
		},
	}
}

func (h *Pass) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return nil, io.EOF
}

func (h *Pass) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, io.EOF
}

func (h *Pass) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	return nil
}

func (h *Pass) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext) error {
	return nil
}
