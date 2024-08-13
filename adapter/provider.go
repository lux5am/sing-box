package adapter

import (
	"context"
	"time"

	"github.com/sagernet/sing-box/option"
)

type OutboundProvider interface {
	Tag() string
	Path() string
	Type() string
	Outbounds() []Outbound
	Outbound(tag string) (Outbound, bool)
	UpdateTime() time.Time

	Start() error
	Close() error
	PostStart() error
	Healthcheck(ctx context.Context, link string, force bool) map[string]uint16
	SubInfo() map[string]int64
	UpdateProvider(ctx context.Context, router Router) error
	UpdateOutboundByTag()
}

type OutboundProviderManager interface {
	Lifecycle
	OutboundsWithProvider() []Outbound
	OutboundWithProvider(tag string) (Outbound, bool)
	OutboundProviders() []OutboundProvider
	OutboundProvider(tag string) (OutboundProvider, bool)
	Remove(tag string) error
	Create(ctx context.Context, router Router, tag string, options option.OutboundProvider) error
}
