package resourcemanager

import (
	"fmt"

	"github.com/your-project/peer"
	"github.com/your-project/protocol"
	"github.com/your-project/log"
)

type BasicResourceManager struct {
	limiter     Limiter
	scopes      map[string]*resourceScope
	trace       *trace
	metrics     *metrics
	allowlist   map[peer.ID]struct{}
	peerScopes  map[peer.ID]*resourceScope
	protoScopes map[protocol.ID]*resourceScope
	svcScopes   map[string]*resourceScope
	system      *resourceScope
	transient   *resourceScope
	monitor     *ResourceMonitor
}

func NewResourceManager(limiter Limiter, opts ...Option) (*BasicResourceManager, error) {
	if limiter == nil {
		return nil, fmt.Errorf("missing resource limiter")
	}

	// Validate all limits before proceeding
	if err := ValidateLimiter(limiter); err != nil {
		return nil, fmt.Errorf("invalid resource limits: %w", err)
	}

	mgr := &BasicResourceManager{
		limiter:     limiter,
		scopes:      make(map[string]*resourceScope),
		trace:       newTrace(),
		metrics:     newMetrics(),
		allowlist:   make(map[peer.ID]struct{}),
		peerScopes:  make(map[peer.ID]*resourceScope),
		protoScopes: make(map[protocol.ID]*resourceScope),
		svcScopes:   make(map[string]*resourceScope),
	}

	var err error
	mgr.system, err = mgr.newSystemScope()
	if err != nil {
		return nil, fmt.Errorf("failed to create system scope: %w", err)
	}

	mgr.transient, err = mgr.newTransientScope()
	if err != nil {
		mgr.system.Done() // Clean up system scope on error
		return nil, fmt.Errorf("failed to create transient scope: %w", err)
	}

	mgr.monitor = NewResourceMonitor(mgr.system, mgr.metrics)

	for _, opt := range opts {
		if err := opt(mgr); err != nil {
			mgr.Close() // Clean up everything on error
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	return mgr, nil
}

func (mgr *BasicResourceManager) Close() error {
	if mgr.monitor != nil {
		if err := mgr.monitor.Close(); err != nil {
			log.Warnf("error closing resource monitor: %s", err)
		}
	}

	// Clean up scopes in reverse order of creation
	if mgr.transient != nil {
		mgr.transient.Done()
	}
	if mgr.system != nil {
		mgr.system.Done()
	}

	// Clean up all remaining scopes
	for _, scope := range mgr.scopes {
		scope.Done()
	}
	for _, scope := range mgr.peerScopes {
		scope.Done()
	}
	for _, scope := range mgr.protoScopes {
		scope.Done()
	}
	for _, scope := range mgr.svcScopes {
		scope.Done()
	}

	return nil
}

func (mgr *BasicResourceManager) ReportResourceError(errorType string) {
	if mgr.monitor != nil {
		mgr.monitor.ReportError(errorType)
	}
}

.. 