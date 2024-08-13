package outbound

import (
	"github.com/sagernet/sing-box/option"
	C "github.com/sagernet/sing-box/constant"
)

type Adapter struct {
	outboundType string
	outboundTag  string
	network      []string
	port         uint16
	dependencies []string
}

func NewAdapter(outboundType string, outboundTag string, network []string, dependencies []string) Adapter {
	return Adapter{
		outboundType: outboundType,
		outboundTag:  outboundTag,
		network:      network,
		dependencies: dependencies,
	}
}

func NewAdapterWithDialerOptions(outboundType string, outboundTag string, network []string, dialOptions option.DialerOptions) Adapter {
	var dependencies []string
	if dialOptions.Detour != "" {
		dependencies = []string{dialOptions.Detour}
	}
	return NewAdapter(outboundType, outboundTag, network, dependencies)
}

func (a *Adapter) Type() string {
	return a.outboundType
}

func (a *Adapter) Tag() string {
	return a.outboundTag
}

func (a *Adapter) Port() int {
	switch a.outboundType {
	case C.TypeDirect, C.TypeBlock, C.TypeDNS, C.TypeTor, C.TypeSelector, C.TypeURLTest:
		return 65536
	default:
		return int(a.port)
	}
}

func (a *Adapter) SetPort(port uint16) {
	a.port = port
}

func (a *Adapter) SetTag(tag string) {
	a.outboundTag = tag
}

func (a *Adapter) Network() []string {
	return a.network
}

func (a *Adapter) Dependencies() []string {
	return a.dependencies
}
