package provider

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/taskmonitor"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/rw"
	"github.com/sagernet/sing/service/filemanager"
)

var _ adapter.OutboundProviderManager = (*Manager)(nil)

type Manager struct {
	logFactory            log.Factory
	logger                log.ContextLogger
	access                sync.Mutex
	started               bool
	stage                 adapter.StartStage
	outbound              adapter.OutboundManager
	outboundProviders     []adapter.OutboundProvider
	outboundProviderByTag map[string]adapter.OutboundProvider
}

func NewManager(logFactory log.Factory, outbound adapter.OutboundManager) *Manager {
	return &Manager{
		logFactory:            logFactory,
		logger:                logFactory.NewLogger("provider"),
		outbound:              outbound,
		outboundProviderByTag: make(map[string]adapter.OutboundProvider),
	}
}

func (m *Manager) Start(stage adapter.StartStage) error {
	monitor := taskmonitor.New(m.logger, C.StartTimeout)
	switch stage {
	case adapter.StartStateInitialize:
		for i, p := range m.outboundProviders {
			var tag string
			if p.Tag() == "" {
				tag = F.ToString(i)
			} else {
				tag = p.Tag()
			}
			monitor.Start("initialize outbound provider/", p.Type(), "[", tag, "]")
			err := p.Start()
			monitor.Finish()
			if err != nil {
				return E.Cause(err, "initialize outbound provider/", p.Type(), "[", tag, "]")
			}
		}
	case adapter.StartStateStart:
		return m.startOutboundProviders()
	case adapter.StartStatePostStart:
		for _, p := range m.outboundProviders {
			err := p.PostStart()
			if err != nil {
				return E.Cause(err, "post-start outbound provider/", p.Tag())
			}
		}
		m.started = true
	}
	return nil
}

func (m *Manager) startOutboundProviders() error {
	monitor := taskmonitor.New(m.logger, C.StartTimeout)
	outboundTag := make(map[string]int)
	for _, out := range m.outbound.Outbounds() {
		tag := out.Tag()
		outboundTag[tag] = 0
	}
	for i, p := range m.outboundProviders {
		var pTag string
		if p.Tag() == "" {
			pTag = F.ToString(i)
		} else {
			pTag = p.Tag()
		}
		for j, out := range p.Outbounds() {
			var tag string
			if out.Tag() == "" {
				out.SetTag(fmt.Sprint("[", pTag, "]", F.ToString(j)))
			}
			tag = out.Tag()
			if _, exists := outboundTag[tag]; exists {
				count := outboundTag[tag] + 1
				tag = fmt.Sprint(tag, "[", count, "]")
				out.SetTag(tag)
				outboundTag[tag] = count
			}
			outboundTag[tag] = 0
			if starter, isStarter := out.(interface {
				Start() error
			}); isStarter {
				monitor.Start("initialize outbound provider[", pTag, "]", " outbound/", out.Type(), "[", tag, "]")
				err := starter.Start()
				monitor.Finish()
				if err != nil {
					return E.Cause(err, "initialize outbound provider[", pTag, "]", " outbound/", out.Type(), "[", tag, "]")
				}
			}
		}
		p.UpdateOutboundByTag()
	}
	return nil
}

func (m *Manager) Close() error {
	monitor := taskmonitor.New(m.logger, C.StopTimeout)
	var errors error
	for i, p := range m.outboundProviders {
		for j, out := range p.Outbounds() {
			monitor.Start("closing provider/", p.Type(), "[", i, "]", " outbound/", out.Type(), "[", j, "]")
			errors = E.Append(errors, common.Close(out), func(err error) error {
				return E.Cause(err, "close provider/", p.Type(), "[", i, "]", " outbound/", out.Type(), "[", j, "]")
			})
			monitor.Finish()
		}
		monitor.Start("closing provider/", p.Type(), "[", i, "]")
		errors = E.Append(errors, common.Close(p), func(err error) error {
			return E.Cause(err, "close provider/", p.Type(), "[", i, "]")
		})
		monitor.Finish()
	}
	return nil
}

func (m *Manager) Remove(tag string) error {
	m.access.Lock()
	provider, found := m.outboundProviderByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	delete(m.outboundProviderByTag, tag)
	index := common.Index(m.outboundProviders, func(it adapter.OutboundProvider) bool {
		return it == provider
	})
	if index == -1 {
		panic("invalid outboundProvider index")
	}
	m.outboundProviders = append(m.outboundProviders[:index], m.outboundProviders[index+1:]...)
	started := m.started
	m.access.Unlock()
	if started {
		return provider.Close()
	}
	return nil
}

func (m *Manager) Create(ctx context.Context, router adapter.Router, tag string, options option.OutboundProvider) error {
	if options.Path == "" {
		return E.New("provider path missing")
	}
	path, _ := C.FindPath(options.Path)
	if foundPath, loaded := C.FindPath(path); loaded {
		path = foundPath
	}
	if !rw.FileExists(path) {
		path = filemanager.BasePath(ctx, path)
	}
	if stat, err := os.Stat(path); err == nil {
		if stat.IsDir() {
			return E.New("provider path is a directory: ", path)
		}
		if stat.Size() == 0 {
			os.Remove(path)
		}
	}
	var provider adapter.OutboundProvider
	var err error
	switch options.Type {
	case C.ProviderTypeLocal:
		provider, err = NewLocalProvider(ctx, m, router, options, path)
	case C.ProviderTypeRemote:
		provider, err = NewRemoteProvider(ctx, m, router, options, path)
	default:
		err = E.New("invalid provider type: ", options.Type)
	}
	if err != nil {
		return err
	}
	m.access.Lock()
	defer m.access.Unlock()
	if m.started {
		for _, stage := range adapter.ListStartStages {
			err = adapter.LegacyStart(provider, stage)
			if err != nil {
				return E.Cause(err, stage, " outboundProvider/", provider.Type(), "[", provider.Tag(), "]")
			}
		}
	}
	if existsProvider, loaded := m.outboundProviderByTag[tag]; loaded {
		if m.started {
			err = existsProvider.Close()
			if err != nil {
				return E.Cause(err, "close outboundProvider/", existsProvider.Type(), "[", existsProvider.Tag(), "]")
			}
		}
		existsIndex := common.Index(m.outboundProviders, func(it adapter.OutboundProvider) bool {
			return it == existsProvider
		})
		if existsIndex == -1 {
			panic("invalid outboundProvider index")
		}
		m.outboundProviders = append(m.outboundProviders[:existsIndex], m.outboundProviders[existsIndex+1:]...)
	}
	m.outboundProviders = append(m.outboundProviders, provider)
	m.outboundProviderByTag[tag] = provider
	return nil
}

func (m *Manager) OutboundWithProvider(tag string) (adapter.Outbound, bool) {
	outbound, loaded := m.outbound.Outbound(tag)
	if loaded {
		return outbound, loaded
	}
	for _, provider := range m.outboundProviders {
		outbound, loaded = provider.Outbound(tag)
		if loaded {
			return outbound, loaded
		}
	}
	return nil, false
}

func (m *Manager) OutboundsWithProvider() []adapter.Outbound {
	outbounds := []adapter.Outbound{}
	outbounds = append(outbounds, m.outbound.Outbounds()...)
	for _, provider := range m.outboundProviders {
		myOutbounds := provider.Outbounds()
		outbounds = append(outbounds, myOutbounds...)
	}
	return outbounds
}

func (m *Manager) OutboundProviders() []adapter.OutboundProvider {
	m.access.Lock()
	defer m.access.Unlock()
	return m.outboundProviders
}

func (m *Manager) OutboundProvider(tag string) (adapter.OutboundProvider, bool) {
	m.access.Lock()
	defer m.access.Unlock()
	provider, found := m.outboundProviderByTag[tag]
	return provider, found
}
