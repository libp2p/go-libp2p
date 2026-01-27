package basichost

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

type autonatv2Client interface {
	GetReachability(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error)
}

const (

	// maxAddrsPerRequest is the maximum number of addresses to probe in a single request
	maxAddrsPerRequest = 10
	// maxTrackedAddrs is the maximum number of addresses to track
	// 10 addrs per transport for 5 transports
	maxTrackedAddrs = 50
	// defaultMaxConcurrency is the default number of concurrent workers for reachability checks
	defaultMaxConcurrency = 5
	// newAddrsProbeDelay is the delay before probing new addr's reachability.
	newAddrsProbeDelay = 1 * time.Second
)

// addrsReachabilityTracker tracks reachability for addresses.
// Use UpdateAddrs to provide addresses for tracking reachability.
// reachabilityUpdateCh is notified when reachability for any of the tracked address changes.
type addrsReachabilityTracker struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	client autonatv2Client
	// reachabilityUpdateCh is used to notify when reachability may have changed
	reachabilityUpdateCh chan struct{}
	maxConcurrency       int
	newAddrsProbeDelay   time.Duration
	probeManager         *probeManager
	newAddrs             chan []ma.Multiaddr
	clock                clock.Clock
	metricsTracker       MetricsTracker

	mx               sync.Mutex
	reachableAddrs   []ma.Multiaddr
	unreachableAddrs []ma.Multiaddr
	unknownAddrs     []ma.Multiaddr
}

// newAddrsReachabilityTracker returns a new addrsReachabilityTracker.
// reachabilityUpdateCh is notified when reachability for any of the tracked address changes.
func newAddrsReachabilityTracker(client autonatv2Client, reachabilityUpdateCh chan struct{}, cl clock.Clock, metricsTracker MetricsTracker) *addrsReachabilityTracker {
	ctx, cancel := context.WithCancel(context.Background())
	if cl == nil {
		cl = clock.New()
	}
	return &addrsReachabilityTracker{
		ctx:                  ctx,
		cancel:               cancel,
		client:               client,
		reachabilityUpdateCh: reachabilityUpdateCh,
		probeManager:         newProbeManager(cl.Now),
		newAddrsProbeDelay:   newAddrsProbeDelay,
		maxConcurrency:       defaultMaxConcurrency,
		newAddrs:             make(chan []ma.Multiaddr, 1),
		clock:                cl,
		metricsTracker:       metricsTracker,
	}
}

func (r *addrsReachabilityTracker) UpdateAddrs(addrs []ma.Multiaddr) {
	select {
	case r.newAddrs <- slices.Clone(addrs):
	case <-r.ctx.Done():
	}
}

func (r *addrsReachabilityTracker) ConfirmedAddrs() (reachableAddrs, unreachableAddrs, unknownAddrs []ma.Multiaddr) {
	r.mx.Lock()
	defer r.mx.Unlock()
	return slices.Clone(r.reachableAddrs), slices.Clone(r.unreachableAddrs), slices.Clone(r.unknownAddrs)
}

func (r *addrsReachabilityTracker) Start() error {
	r.wg.Add(1)
	go r.background()
	return nil
}

func (r *addrsReachabilityTracker) Close() error {
	r.cancel()
	r.wg.Wait()
	return nil
}

const (
	// defaultReachabilityRefreshInterval is the default interval to refresh reachability.
	// In steady state, we check for any required probes every refresh interval.
	// This doesn't mean we'll probe for any particular address, only that we'll check
	// if any address needs to be probed.
	defaultReachabilityRefreshInterval = 5 * time.Minute
	// maxBackoffInterval is the maximum back off in case we're unable to probe for reachability.
	// We may be unable to confirm addresses in case there are no valid peers with autonatv2
	// or the autonatv2 subsystem is consistently erroring.
	maxBackoffInterval = 5 * time.Minute
	// backoffStartInterval is the initial back off in case we're unable to probe for reachability.
	backoffStartInterval = 5 * time.Second
)

func (r *addrsReachabilityTracker) background() {
	defer r.wg.Done()

	// probeTicker is used to trigger probes at regular intervals
	probeTicker := r.clock.Ticker(defaultReachabilityRefreshInterval)
	defer probeTicker.Stop()

	// probeTimer is used to trigger probes at specific times
	probeTimer := r.clock.Timer(time.Duration(math.MaxInt64))
	defer probeTimer.Stop()
	nextProbeTime := time.Time{}

	var task reachabilityTask
	var backoffInterval time.Duration
	var currReachable, currUnreachable, currUnknown, prevReachable, prevUnreachable, prevUnknown []ma.Multiaddr
	for {
		select {
		case <-probeTicker.C:
			// don't start a probe if we have a scheduled probe
			if task.BackoffCh == nil && nextProbeTime.IsZero() {
				task = r.refreshReachability()
			}
		case <-probeTimer.C:
			if task.BackoffCh == nil {
				task = r.refreshReachability()
			}
			nextProbeTime = time.Time{}
		case backoff := <-task.BackoffCh:
			task = reachabilityTask{}
			// On completion, start the next probe immediately, or wait for backoff.
			// In case there are no further probes, the reachability tracker will return an empty task,
			// which hangs forever. Eventually, we'll refresh again when the ticker fires.
			if backoff {
				backoffInterval = newBackoffInterval(backoffInterval)
			} else {
				backoffInterval = -1 * time.Second // negative to trigger next probe immediately
			}
			nextProbeTime = r.clock.Now().Add(backoffInterval)
		case addrs := <-r.newAddrs:
			if task.BackoffCh != nil { // cancel running task.
				task.Cancel()
				<-task.BackoffCh // ignore backoff from cancelled task
				task = reachabilityTask{}
			}
			r.updateTrackedAddrs(addrs)
			newAddrsNextTime := r.clock.Now().Add(r.newAddrsProbeDelay)
			if nextProbeTime.Before(newAddrsNextTime) {
				nextProbeTime = newAddrsNextTime
			}
		case <-r.ctx.Done():
			if task.BackoffCh != nil {
				task.Cancel()
				<-task.BackoffCh
				task = reachabilityTask{}
			}
			if r.metricsTracker != nil {
				r.metricsTracker.ReachabilityTrackerClosed()
			}
			return
		}

		currReachable, currUnreachable, currUnknown = r.appendConfirmedAddrs(currReachable[:0], currUnreachable[:0], currUnknown[:0])
		if areAddrsDifferent(prevReachable, currReachable) || areAddrsDifferent(prevUnreachable, currUnreachable) || areAddrsDifferent(prevUnknown, currUnknown) {
			if r.metricsTracker != nil {
				r.metricsTracker.ConfirmedAddrsChanged(currReachable, currUnreachable, currUnknown)
			}
			r.notify()
		}
		prevReachable = append(prevReachable[:0], currReachable...)
		prevUnreachable = append(prevUnreachable[:0], currUnreachable...)
		prevUnknown = append(prevUnknown[:0], currUnknown...)
		if !nextProbeTime.IsZero() {
			probeTimer.Reset(nextProbeTime.Sub(r.clock.Now()))
		}
	}
}

func newBackoffInterval(current time.Duration) time.Duration {
	if current <= 0 {
		return backoffStartInterval
	}
	current *= 2
	if current > maxBackoffInterval {
		return maxBackoffInterval
	}
	return current
}

func (r *addrsReachabilityTracker) appendConfirmedAddrs(reachable, unreachable, unknown []ma.Multiaddr) (reachableAddrs, unreachableAddrs, unknownAddrs []ma.Multiaddr) {
	reachable, unreachable, unknown = r.probeManager.AppendConfirmedAddrs(reachable, unreachable, unknown)
	r.mx.Lock()
	r.reachableAddrs = append(r.reachableAddrs[:0], reachable...)
	r.unreachableAddrs = append(r.unreachableAddrs[:0], unreachable...)
	r.unknownAddrs = append(r.unknownAddrs[:0], unknown...)
	r.mx.Unlock()

	return reachable, unreachable, unknown
}

func (r *addrsReachabilityTracker) notify() {
	select {
	case r.reachabilityUpdateCh <- struct{}{}:
	default:
	}
}

func (r *addrsReachabilityTracker) updateTrackedAddrs(addrs []ma.Multiaddr) {
	addrs = slices.DeleteFunc(addrs, func(a ma.Multiaddr) bool {
		return !manet.IsPublicAddr(a)
	})
	if len(addrs) > maxTrackedAddrs {
		log.Error("too many addresses for addrs reachability tracker; dropping some", "total", len(addrs), "max", maxTrackedAddrs, "dropping", len(addrs)-maxTrackedAddrs)
		addrs = addrs[:maxTrackedAddrs]
	}
	r.probeManager.UpdateAddrs(addrs)
}

type probe = []autonatv2.Request

const probeTimeout = 30 * time.Second

// reachabilityTask is a task to refresh reachability.
// Waiting on the zero value blocks forever.
type reachabilityTask struct {
	Cancel context.CancelFunc
	// BackoffCh returns whether the caller should backoff before
	// refreshing reachability
	BackoffCh chan bool
}

func (r *addrsReachabilityTracker) refreshReachability() reachabilityTask {
	if len(r.probeManager.GetProbe()) == 0 {
		return reachabilityTask{}
	}
	resCh := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Minute)
	r.wg.Add(1)
	// We run probes provided by addrsTracker. It stops probing when any
	// of the following happens:
	// - there are no more probes to run
	// - context is completed
	// - there are too many consecutive failures from the client
	// - the client has no valid peers to probe
	go func() {
		defer r.wg.Done()
		defer cancel()
		client := &errCountingClient{autonatv2Client: r.client, MaxConsecutiveErrors: maxConsecutiveErrors}
		var backoff atomic.Bool
		var wg sync.WaitGroup
		wg.Add(r.maxConcurrency)
		for range r.maxConcurrency {
			go func() {
				defer wg.Done()
				for {
					if ctx.Err() != nil {
						return
					}
					reqs := r.probeManager.GetProbe()
					if len(reqs) == 0 {
						return
					}
					r.probeManager.MarkProbeInProgress(reqs)
					rctx, cancel := context.WithTimeout(ctx, probeTimeout)
					res, err := client.GetReachability(rctx, reqs)
					cancel()
					r.probeManager.CompleteProbe(reqs, res, err)
					if isErrorPersistent(err) {
						backoff.Store(true)
						return
					}
				}
			}()
		}
		wg.Wait()
		resCh <- backoff.Load()
	}()
	return reachabilityTask{Cancel: cancel, BackoffCh: resCh}
}

var errTooManyConsecutiveFailures = errors.New("too many consecutive failures")

// errCountingClient counts errors from autonatv2Client and wraps the errors in response with a
// errTooManyConsecutiveFailures in case of persistent failures from autonatv2 module.
type errCountingClient struct {
	autonatv2Client
	MaxConsecutiveErrors int
	mx                   sync.Mutex
	consecutiveErrors    int
}

func (c *errCountingClient) GetReachability(ctx context.Context, reqs probe) (autonatv2.Result, error) {
	res, err := c.autonatv2Client.GetReachability(ctx, reqs)
	c.mx.Lock()
	defer c.mx.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) { // ignore canceled errors, they're not errors from autonatv2
		c.consecutiveErrors++
		if c.consecutiveErrors > c.MaxConsecutiveErrors {
			err = fmt.Errorf("%w:%w", errTooManyConsecutiveFailures, err)
		}
		if errors.Is(err, autonatv2.ErrPrivateAddrs) {
			log.Error("private IP addr in autonatv2 request", "err", err)
		}
	} else {
		c.consecutiveErrors = 0
	}
	return res, err
}

const maxConsecutiveErrors = 20

// isErrorPersistent returns whether the error will repeat on future probes for a while
func isErrorPersistent(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, autonatv2.ErrPrivateAddrs) || errors.Is(err, autonatv2.ErrNoPeers) ||
		errors.Is(err, errTooManyConsecutiveFailures)
}

const (
	// recentProbeInterval is the interval to probe addresses that have been refused
	// these are generally addresses with newer transports for which we don't have many peers
	// capable of dialing the transport
	recentProbeInterval = 10 * time.Minute
	// maxConsecutiveRefusals is the maximum number of consecutive refusals for an address after which
	// we wait for `recentProbeInterval` before probing again
	maxConsecutiveRefusals = 5
	// maxRecentDialsPerAddr is the maximum number of dials on an address before we stop probing for the address.
	// This is used to prevent infinite probing of an address whose status is indeterminate for any reason.
	maxRecentDialsPerAddr = 10
	// confidence is the absolute difference between the number of successes and failures for an address
	// targetConfidence is the confidence threshold for an address after which we wait for `maxProbeInterval`
	// before probing again.
	targetConfidence = 3
	// minConfidence is the confidence threshold for an address to be considered reachable or unreachable
	// confidence is the absolute difference between the number of successes and failures for an address
	minConfidence = 2
	// maxRecentDialsWindow is the maximum number of recent probe results to consider for a single address
	//
	// +2 allows for 1 invalid probe result. Consider a string of successes, after which we have a single failure
	// and then a success(...S S S S F S). The confidence in the targetConfidence window  will be equal to
	// targetConfidence, the last F and S cancel each other, and we won't probe again for maxProbeInterval.
	maxRecentDialsWindow = targetConfidence + 2
	// highConfidenceAddrProbeInterval is the maximum interval between probes for an address
	highConfidenceAddrProbeInterval = 1 * time.Hour
	// highConfidenceSecondaryAddrProbeInterval is the maximum interval between probes for an address
	highConfidenceSecondaryAddrProbeInterval = 3 * time.Hour
	// maxProbeResultTTL is the maximum time to keep probe results for a primary address
	maxProbeResultTTL = maxRecentDialsWindow * highConfidenceAddrProbeInterval
)

// probeManager tracks reachability for a set of addresses by periodically probing reachability with autonatv2.
// A Probe is a list of addresses which can be tested for reachability with autonatv2.
// This struct decides the priority order of addresses for testing reachability, and throttles in case there have
// been too many probes for an address in the `ProbeInterval`.
//
// Use the `runProbes` function to execute the probes with an autonatv2 client.
type probeManager struct {
	now func() time.Time

	mx                    sync.Mutex
	inProgressProbes      map[string]int // addr -> count
	inProgressProbesTotal int
	statuses              map[string]*addrStatus
	primaryAddrs          []ma.Multiaddr
	secondaryAddrs        []ma.Multiaddr
}

// newProbeManager creates a new probe manager.
func newProbeManager(now func() time.Time) *probeManager {
	return &probeManager{
		statuses:         make(map[string]*addrStatus),
		inProgressProbes: make(map[string]int),
		now:              now,
	}
}

// AppendConfirmedAddrs appends the current confirmed reachable and unreachable addresses.
func (m *probeManager) AppendConfirmedAddrs(reachable, unreachable, unknown []ma.Multiaddr) (reachableAddrs, unreachableAddrs, unknownAddrs []ma.Multiaddr) {
	m.mx.Lock()
	defer m.mx.Unlock()

	for _, a := range m.primaryAddrs {
		s := m.statuses[string(a.Bytes())]
		s.RemoveBefore(m.now().Add(-maxProbeResultTTL)) // cleanup stale results
		switch s.Reachability() {
		case network.ReachabilityPublic:
			reachable = append(reachable, a)
		case network.ReachabilityPrivate:
			unreachable = append(unreachable, a)
		case network.ReachabilityUnknown:
			unknown = append(unknown, a)
		}
	}

	for _, a := range m.secondaryAddrs {
		s := m.statuses[string(a.Bytes())]
		s.RemoveBefore(m.now().Add(-maxProbeResultTTL)) // cleanup stale results
		switch s.Reachability() {
		case network.ReachabilityPublic:
			reachable = append(reachable, a)
		case network.ReachabilityPrivate:
			unreachable = append(unreachable, a)
		case network.ReachabilityUnknown:
			unknown = append(unknown, a)
		}
	}
	return reachable, unreachable, unknown
}

// UpdateAddrs updates the tracked addrs
func (m *probeManager) UpdateAddrs(addrs []ma.Multiaddr) {
	m.mx.Lock()
	defer m.mx.Unlock()

	slices.SortFunc(addrs, func(a, b ma.Multiaddr) int { return a.Compare(b) })
	statuses := make(map[string]*addrStatus, len(addrs))
	for _, addr := range addrs {
		k := string(addr.Bytes())
		if _, ok := m.statuses[k]; !ok {
			statuses[k] = &addrStatus{Addr: addr}
		} else {
			statuses[k] = m.statuses[k]
			// our addresses have changed, we may have removed the primary address
			statuses[k].primary = nil
		}
	}
	assignPrimaryAddrs(statuses)
	m.primaryAddrs = m.primaryAddrs[:0]
	m.secondaryAddrs = m.secondaryAddrs[:0]
	for _, a := range addrs {
		if statuses[string(a.Bytes())].primary == nil {
			m.primaryAddrs = append(m.primaryAddrs, a)
		} else {
			m.secondaryAddrs = append(m.secondaryAddrs, a)
		}
	}
	m.statuses = statuses
}

// GetProbe returns the next probe. Returns zero value in case there are no more probes.
// Probes that are run against an autonatv2 client should be marked in progress with
// `MarkProbeInProgress` before running.
func (m *probeManager) GetProbe() probe {
	m.mx.Lock()
	defer m.mx.Unlock()

	/*
		 - First, select the first address for the probe. The assumption is that this is the
			address which will be dialled.
		 - Then, we fill the rest of the addresses in the probe while trying to ensure diversity.
	*/
	now := m.now()
	// first check if the probe's first address is a primary address
	idx, ok := m.getFirstProbeAddrIdx(m.primaryAddrs, now)
	var reqs probe
	if ok {
		reqs = make(probe, 0, maxAddrsPerRequest)
		reqs = append(reqs, autonatv2.Request{Addr: m.primaryAddrs[idx], SendDialData: true})
		reqs = m.appendRequestsToProbe(reqs, m.primaryAddrs, idx, true, now)
		reqs = m.appendRequestsToProbe(reqs, m.secondaryAddrs, 0, false, now)
	} else {
		// no primary addresses available, try secondary.
		idx, ok := m.getFirstProbeAddrIdx(m.secondaryAddrs, now)
		if !ok {
			return nil
		}
		reqs = make(probe, 0, maxAddrsPerRequest)
		reqs = append(reqs, autonatv2.Request{Addr: m.secondaryAddrs[idx], SendDialData: true})
		reqs = m.appendRequestsToProbe(reqs, m.primaryAddrs, 0, false, now)
		reqs = m.appendRequestsToProbe(reqs, m.secondaryAddrs, idx, true, now)
	}

	if len(reqs) >= maxAddrsPerRequest {
		reqs = reqs[:maxAddrsPerRequest]
	}
	return reqs
}

// getFirstProbeAddrIdx returns the idx of the probe's first address
func (m *probeManager) getFirstProbeAddrIdx(addrs []ma.Multiaddr, now time.Time) (int, bool) {
	for i, a := range addrs {
		s := m.statuses[string(a.Bytes())]
		pc := s.RequiredProbeCount(now)
		if pc == 0 || m.inProgressProbes[string(addrs[i].Bytes())] >= pc {
			continue
		}
		return i, true
	}
	return -1, false
}

// appendRequestsToProbe appends requests to `reqs` after the first address has been determined
func (m *probeManager) appendRequestsToProbe(reqs probe, addrs []ma.Multiaddr, st int, skipStart bool, now time.Time) probe {
	n := len(addrs)
	for j := range n {
		k := (j + st) % n // We start from index: st
		if skipStart && k == st {
			continue
		}
		s := m.statuses[string(addrs[k].Bytes())]
		pc := s.RequiredProbeCount(now)
		if pc == 0 {
			continue
		}
		reqs = append(reqs, autonatv2.Request{Addr: addrs[k], SendDialData: true})
		if len(reqs) >= maxAddrsPerRequest {
			break
		}
	}
	return reqs
}

// MarkProbeInProgress should be called when a probe is started.
// All in progress probes *MUST* be completed with `CompleteProbe`
func (m *probeManager) MarkProbeInProgress(reqs probe) {
	if len(reqs) == 0 {
		return
	}
	m.mx.Lock()
	defer m.mx.Unlock()
	m.inProgressProbes[string(reqs[0].Addr.Bytes())]++
	m.inProgressProbesTotal++
}

// InProgressProbes returns the number of probes that are currently in progress.
func (m *probeManager) InProgressProbes() int {
	m.mx.Lock()
	defer m.mx.Unlock()
	return m.inProgressProbesTotal
}

// CompleteProbe should be called when a probe completes.
func (m *probeManager) CompleteProbe(reqs probe, res autonatv2.Result, err error) {
	now := m.now()

	if len(reqs) == 0 {
		// should never happen
		return
	}

	m.mx.Lock()
	defer m.mx.Unlock()

	// decrement in-progress count for the first address
	firstAddrKey := string(reqs[0].Addr.Bytes())
	m.inProgressProbes[firstAddrKey]--
	if m.inProgressProbes[firstAddrKey] <= 0 {
		delete(m.inProgressProbes, firstAddrKey)
	}
	m.inProgressProbesTotal--

	// nothing to do if the request errored.
	if err != nil {
		return
	}

	// Consider only first address as refused. This increases the number of
	// refused probes, but refused probes are cheap for a server as no dials are made.
	if res.AllAddrsRefused {
		if s, ok := m.statuses[firstAddrKey]; ok {
			s.AddRefusal(now)
		}
		return
	}
	dialAddrKey := string(res.Addr.Bytes())
	if dialAddrKey != firstAddrKey {
		if s, ok := m.statuses[firstAddrKey]; ok {
			s.AddRefusal(now)
		}
	}

	// record the result for the dialed address
	if s, ok := m.statuses[dialAddrKey]; ok {
		s.AddOutcome(now, res.Reachability, maxRecentDialsWindow)
	}
}

type dialOutcome struct {
	Success bool
	At      time.Time
}

type addrStatus struct {
	Addr                ma.Multiaddr
	primary             *addrStatus
	lastRefusalTime     time.Time
	consecutiveRefusals int
	dialTimes           []time.Time
	outcomes            []dialOutcome
}

func (s *addrStatus) Reachability() network.Reachability {
	rch, _, _ := s.reachabilityAndCounts()
	return rch
}

func (s *addrStatus) RequiredProbeCount(now time.Time) int {
	// Secondary addresses inherit reachability from their confirmed-public primary.
	// If the primary is ReachabilityPublic, the port is confirmed open at the
	// network level, so the secondary is also reachable (they share the socket).
	//
	// If the primary is ReachabilityPrivate, we still probe the secondary because
	// Private is a weaker signal - it could indicate:
	//   - Port genuinely blocked (secondary will also fail)
	//   - Protocol-specific issues with the primary (secondary might work)
	// The cost of extra probes when truly firewalled is low (quick failures).
	if s.primary != nil && s.primary.Reachability() == network.ReachabilityPublic {
		return 0
	}

	if s.consecutiveRefusals >= maxConsecutiveRefusals {
		if now.Sub(s.lastRefusalTime) < recentProbeInterval {
			return 0
		}
		// reset every `recentProbeInterval`
		s.lastRefusalTime = time.Time{}
		s.consecutiveRefusals = 0
	}

	// Don't probe if we have probed too many times recently
	rd := s.recentDialCount(now)
	if rd >= maxRecentDialsPerAddr {
		return 0
	}

	return s.requiredProbeCountForConfirmation(now)
}

func (s *addrStatus) requiredProbeCountForConfirmation(now time.Time) int {
	reachability, successes, failures := s.reachabilityAndCounts()
	confidence := successes - failures
	if confidence < 0 {
		confidence = -confidence
	}
	cnt := targetConfidence - confidence
	if cnt > 0 {
		return cnt
	}
	// we have enough confirmations; check if we should refresh

	// Should never happen. The confidence logic above should require a few probes.
	if len(s.outcomes) == 0 {
		return 0
	}
	lastOutcome := s.outcomes[len(s.outcomes)-1]
	// If the last probe result is old, we need to retest
	if d := now.Sub(lastOutcome.At); (s.primary == nil && d > highConfidenceAddrProbeInterval) ||
		(d > highConfidenceSecondaryAddrProbeInterval) {
		return 1
	}
	// if the last probe result was different from reachability, probe again.
	switch reachability {
	case network.ReachabilityPublic:
		if !lastOutcome.Success {
			return 1
		}
	case network.ReachabilityPrivate:
		if lastOutcome.Success {
			return 1
		}
	default:
		// this should never happen
		return 1
	}
	return 0
}

func (s *addrStatus) AddRefusal(now time.Time) {
	s.lastRefusalTime = now
	s.consecutiveRefusals++
}

func (s *addrStatus) AddOutcome(at time.Time, rch network.Reachability, windowSize int) {
	s.lastRefusalTime = time.Time{}
	s.consecutiveRefusals = 0

	s.dialTimes = append(s.dialTimes, at)
	for i, t := range s.dialTimes {
		if at.Sub(t) < recentProbeInterval {
			s.dialTimes = slices.Delete(s.dialTimes, 0, i)
			break
		}
	}

	s.RemoveBefore(at.Add(-maxProbeResultTTL)) // remove old outcomes
	success := false
	switch rch {
	case network.ReachabilityPublic:
		success = true
	case network.ReachabilityPrivate:
		success = false
	default:
		return // don't store the outcome if reachability is unknown
	}
	s.outcomes = append(s.outcomes, dialOutcome{At: at, Success: success})
	if len(s.outcomes) > windowSize {
		s.outcomes = slices.Delete(s.outcomes, 0, len(s.outcomes)-windowSize)
	}
}

// RemoveBefore removes outcomes before t
func (s *addrStatus) RemoveBefore(t time.Time) {
	end := 0
	for ; end < len(s.outcomes); end++ {
		if !s.outcomes[end].At.Before(t) {
			break
		}
	}
	s.outcomes = slices.Delete(s.outcomes, 0, end)
}

func (s *addrStatus) recentDialCount(now time.Time) int {
	cnt := 0
	for _, t := range slices.Backward(s.dialTimes) {
		if now.Sub(t) > recentProbeInterval {
			break
		}
		cnt++
	}
	return cnt
}

func (s *addrStatus) reachabilityAndCounts() (rch network.Reachability, successes int, failures int) {
	for _, r := range s.outcomes {
		if r.Success {
			successes++
		} else {
			failures++
		}
	}
	if s.primary != nil {
		prch, _, _ := s.primary.reachabilityAndCounts()
		if prch == network.ReachabilityPublic {
			// Secondary transports inherit Public reachability from their primary.
			//
			// This is important because not all AutoNAT v2 server implementations
			// support all secondary transports. As the Amino DHT gained a more
			// diverse set of node implementations (2025 Q4), we observed false
			// negatives: secondary addresses being marked unreachable when probing
			// peers simply didn't support the protocol, not because the port was
			// actually blocked.
			//
			// This handles shared-listener configurations where multiple
			// protocols share the same network socket:
			//
			//   TCP-based (libp2p.ShareTCPListener):
			//     Primary:   /ip4/.../tcp/port
			//     Secondary: /ip4/.../tcp/port/tls/sni/*.libp2p.direct/ws
			//     TCP and Secure WebSocket share the same TCP listener.
			//
			//   UDP/QUIC-based (quicreuse.ConnManager):
			//     Primary:   /ip4/.../udp/port/quic-v1
			//     Secondary: /ip4/.../udp/port/quic-v1/webtransport
			//     Secondary: /ip4/.../udp/port/webrtc-direct
			//     QUIC, WebTransport, and WebRTC share the same UDP socket.
			//
			// AutoNAT v2 probe failures for secondary protocols typically
			// indicate protocol incompatibility at the probing peer, not
			// port unreachability:
			//
			//   - Secure WebSocket: Probing peer may not support WebSockets,
			//     or TLS handshake fails because the certificate isn't
			//     provisioned yet (AutoTLS still obtaining cert).
			//   - WebTransport: Probing peer supports QUIC but not HTTP/3.
			//   - WebRTC: Probing peer supports QUIC but not DTLS-SRTP.
			//
			// Since the primary confirms the port is network-reachable, we
			// inherit that status. Protocol-level failures don't indicate
			// the address is unreachable to peers that DO support the protocol.
			return network.ReachabilityPublic, successes, failures
		}
		// If primary is Private or Unknown, we don't inherit - the secondary
		// builds its own status through probing. This is more conservative:
		// Private could indicate protocol-specific issues rather than port
		// unreachability, so we give the secondary a chance to prove itself.
	}
	if successes-failures >= minConfidence {
		return network.ReachabilityPublic, successes, failures
	}
	if failures-successes >= minConfidence {
		return network.ReachabilityPrivate, successes, failures
	}
	return network.ReachabilityUnknown, successes, failures
}

var errNotTW = errors.New("not a thinwaist address")

func thinWaistPart(a ma.Multiaddr) (ma.Multiaddr, error) {
	if len(a) < 2 {
		return nil, errNotTW
	}
	if c0, c1 := a[0].Code(), a[1].Code(); (c0 != ma.P_IP4 && c0 != ma.P_IP6) || (c1 != ma.P_TCP && c1 != ma.P_UDP) {
		return nil, errNotTW
	}
	return a[:2], nil
}

func assignPrimaryAddrs(statuses map[string]*addrStatus) {
	twMap := make(map[string][]ma.Multiaddr, len(statuses))
	for _, s := range statuses {
		twp, err := thinWaistPart(s.Addr)
		if err != nil {
			continue
		}
		twMap[string(twp.Bytes())] = append(twMap[string(twp.Bytes())], s.Addr)
	}

	score := func(a ma.Multiaddr) int {
		score := 0
		for _, p := range a {
			switch p.Code() {
			case ma.P_QUIC_V1, ma.P_TCP:
				score += 1
			case ma.P_WEBTRANSPORT:
				score += 1 << 1
			case ma.P_WEBRTC:
				score += 1 << 2
			case ma.P_WS, ma.P_WSS:
				score += 1 << 3
			}
		}
		if score == 0 {
			return 1 << 20
		}
		return score
	}
	for _, addrs := range twMap {
		if len(addrs) <= 1 {
			continue
		}
		slices.SortFunc(addrs, func(a, b ma.Multiaddr) int {
			return score(a) - score(b)
		})
		primary := addrs[0]
		ps := statuses[string(primary.Bytes())]
		for _, a := range addrs[1:] {
			s := statuses[string(a.Bytes())]
			s.primary = ps
		}
	}
}
