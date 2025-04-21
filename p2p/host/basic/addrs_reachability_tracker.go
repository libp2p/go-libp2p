package basichost

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
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

type addrsReachabilityTracker struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	cli autonatv2Client
	// reachabilityUpdateCh is used to notify when reachability may have changed
	reachabilityUpdateCh chan struct{}
	maxConcurrency       int
	newAddrsProbeDelay   time.Duration
	addrTracker          *probeManager
	newAddrs             chan []ma.Multiaddr
	clock                clock.Clock

	mx               sync.Mutex
	reachableAddrs   []ma.Multiaddr
	unreachableAddrs []ma.Multiaddr
}

// newAddrsReachabilityTracker tracks reachability for addresses.
// Use UpdateAddrs to provide addresses for tracking reachability.
// reachabilityUpdateCh is notified when any reachability probes are made. The reader must dedup the events. It may be
// notified even when the reachability for any addrs has not changed.
func newAddrsReachabilityTracker(client autonatv2Client, reachabilityUpdateCh chan struct{}, cl clock.Clock) *addrsReachabilityTracker {
	ctx, cancel := context.WithCancel(context.Background())
	if cl == nil {
		cl = clock.New()
	}
	return &addrsReachabilityTracker{
		ctx:                  ctx,
		cancel:               cancel,
		cli:                  client,
		reachabilityUpdateCh: reachabilityUpdateCh,
		addrTracker:          newProbeManager(cl.Now),
		newAddrsProbeDelay:   newAddrsProbeDelay,
		maxConcurrency:       defaultMaxConcurrency,
		newAddrs:             make(chan []ma.Multiaddr, 1),
		clock:                cl,
	}
}

func (r *addrsReachabilityTracker) UpdateAddrs(addrs []ma.Multiaddr) {
	r.newAddrs <- slices.Clone(addrs)
}

func (r *addrsReachabilityTracker) ConfirmedAddrs() (reachableAddrs, unreachableAddrs []ma.Multiaddr) {
	r.mx.Lock()
	defer r.mx.Unlock()
	return slices.Clone(r.reachableAddrs), slices.Clone(r.unreachableAddrs)
}

func (r *addrsReachabilityTracker) Start() error {
	r.wg.Add(1)
	err := r.background()
	if err != nil {
		return err
	}
	return nil
}

func (r *addrsReachabilityTracker) Close() error {
	r.cancel()
	r.wg.Wait()
	return nil
}

const (
	defaultResetInterval = 5 * time.Minute
	maxBackoffInterval   = 5 * time.Minute
	backoffStartInterval = 5 * time.Second
)

func (r *addrsReachabilityTracker) background() error {
	go func() {
		defer r.wg.Done()

		// probeTicker is used to trigger probes at regular intervals
		probeTicker := r.clock.Ticker(defaultResetInterval)
		defer probeTicker.Stop()

		// probeTimer is used to trigger probes at specific times
		probeTimer := r.clock.Timer(time.Duration(math.MaxInt64))
		defer probeTimer.Stop()
		nextProbeTime := time.Time{}

		var task reachabilityTask
		var backoffInterval time.Duration
		var currReachable, currUnreachable, prevReachable, prevUnreachable []ma.Multiaddr
		for {
			select {
			case <-probeTicker.C:
				// don't start a probe if we have a scheduled probe
				if task.RespCh == nil && nextProbeTime.IsZero() {
					task = r.refreshReachability()
				}
			case <-probeTimer.C:
				if task.RespCh == nil {
					task = r.refreshReachability()
				}
				nextProbeTime = time.Time{}
			case backoff := <-task.RespCh:
				task = reachabilityTask{}
				// On completion, start the next probe immediately, or wait for backoff
				// In case there are no further probes, the reachability tracker will return an empty task,
				// which hangs forever. Eventually, we'll refresh again when the ticker fires.
				if backoff {
					backoffInterval = newBackoffInterval(backoffInterval)
				} else {
					backoffInterval = -1 * time.Second // negative to trigger next probe immediately
				}
				nextProbeTime = r.clock.Now().Add(backoffInterval)
			case addrs := <-r.newAddrs:
				if task.RespCh != nil { // cancel running task.
					task.Cancel()
					<-task.RespCh // ignore backoff from cancelled task
					task = reachabilityTask{}
				}
				r.updateTrackedAddrs(addrs)
				newAddrsNextTime := r.clock.Now().Add(r.newAddrsProbeDelay)
				if nextProbeTime.Before(newAddrsNextTime) {
					nextProbeTime = newAddrsNextTime
				}
			case <-r.ctx.Done():
				if task.RespCh != nil {
					task.Cancel()
					<-task.RespCh
					task = reachabilityTask{}
				}
				return
			}

			currReachable, currUnreachable = r.appendConfirmedAddrs(currReachable[:0], currUnreachable[:0])
			if areAddrsDifferent(prevReachable, currReachable) || areAddrsDifferent(prevUnreachable, currUnreachable) {
				r.notify()
			}
			prevReachable = append(prevReachable[:0], currReachable...)
			prevUnreachable = append(prevUnreachable[:0], currUnreachable...)
			if !nextProbeTime.IsZero() {
				probeTimer.Reset(nextProbeTime.Sub(r.clock.Now()))
			}
		}
	}()
	return nil
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

func (r *addrsReachabilityTracker) appendConfirmedAddrs(reachable, unreachable []ma.Multiaddr) (reachableAddrs, unreachableAddrs []ma.Multiaddr) {
	reachable, unreachable = r.addrTracker.AppendConfirmedAddrs(reachable, unreachable)
	r.mx.Lock()
	r.reachableAddrs = append(r.reachableAddrs[:0], reachable...)
	r.unreachableAddrs = append(r.unreachableAddrs[:0], unreachable...)
	r.mx.Unlock()
	return reachable, unreachable
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
		log.Errorf("too many addresses (%d) for addrs reachability tracker; dropping %d", len(addrs), len(addrs)-maxTrackedAddrs)
		addrs = addrs[:maxTrackedAddrs]
	}
	r.addrTracker.UpdateAddrs(addrs)
}

// reachabilityTask is a task to refresh reachability.
// Waiting on the zero value blocks forever.
type reachabilityTask struct {
	Cancel context.CancelFunc
	RespCh chan bool
}

func (r *addrsReachabilityTracker) refreshReachability() reachabilityTask {
	if len(r.addrTracker.GetProbe()) == 0 {
		return reachabilityTask{}
	}
	resCh := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Minute)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		backoff := runProbes(ctx, r.maxConcurrency, r.addrTracker, r.cli)
		resCh <- backoff
	}()
	return reachabilityTask{Cancel: cancel, RespCh: resCh}
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

func (c *errCountingClient) GetReachability(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
	res, err := c.autonatv2Client.GetReachability(ctx, reqs)
	c.mx.Lock()
	defer c.mx.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) { // ignore canceled errors, they're not errors from autonatv2
		c.consecutiveErrors++
		if c.consecutiveErrors > c.MaxConsecutiveErrors {
			err = fmt.Errorf("%w:%w", errTooManyConsecutiveFailures, err)
		}
		if errors.Is(err, autonatv2.ErrPrivateAddrs) {
			log.Errorf("private IP addr in autonatv2 request: %s", err)
		}
	} else {
		c.consecutiveErrors = 0
	}
	return res, err
}

type probeResponse struct {
	Req []autonatv2.Request
	Res autonatv2.Result
	Err error
}

const maxConsecutiveErrors = 20

// runProbes runs probes provided by addrsTracker with the given client. It returns true if the caller should
// backoff before retrying probes. It stops probing when any of the following happens:
// - there are no more probes to run
// - context is completed
// - there are too many consecutive failures from the client
// - the client has no valid peers to probe
func runProbes(ctx context.Context, concurrency int, addrsTracker *probeManager, client autonatv2Client) bool {
	client = &errCountingClient{autonatv2Client: client, MaxConsecutiveErrors: maxConsecutiveErrors}

	resultsCh := make(chan probeResponse, 2*concurrency) // enough buffer to allow all worker goroutines to exit quickly
	jobsCh := make(chan []autonatv2.Request, 1)          // close jobs to terminate the workers
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()
			for reqs := range jobsCh {
				ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				res, err := client.GetReachability(ctx, reqs)
				cancel()
				resultsCh <- probeResponse{Req: reqs, Res: res, Err: err}
			}
		}()
	}

	nextProbe := addrsTracker.GetProbe()
	backoff := false
outer:
	for jc := jobsCh; addrsTracker.InProgressProbes() > 0 || len(nextProbe) > 0; {
		select {
		case jc <- nextProbe:
			addrsTracker.MarkProbeInProgress(nextProbe)
		case resp := <-resultsCh:
			addrsTracker.CompleteProbe(resp.Req, resp.Res, resp.Err)
			if isErrorPersistent(resp.Err) {
				backoff = true
				break outer
			}
		case <-ctx.Done():
			break outer
		}
		jc = jobsCh
		nextProbe = addrsTracker.GetProbe()
		if len(nextProbe) == 0 {
			jc = nil
		}
	}
	close(jobsCh)
	for addrsTracker.InProgressProbes() > 0 {
		resp := <-resultsCh
		addrsTracker.CompleteProbe(resp.Req, resp.Res, resp.Err)
		if isErrorPersistent(resp.Err) {
			backoff = true
		}
	}
	wg.Wait()
	return backoff
}

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
	// before probing again
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
	// maxProbeInterval is the maximum interval between probes for an address
	maxProbeInterval = 1 * time.Hour
	// maxProbeResultTTL is the maximum time to keep probe results for an address
	maxProbeResultTTL = maxRecentDialsWindow * maxProbeInterval
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
	addrs                 []ma.Multiaddr
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
func (m *probeManager) AppendConfirmedAddrs(reachable, unreachable []ma.Multiaddr) (reachableAddrs, unreachableAddrs []ma.Multiaddr) {
	m.mx.Lock()
	defer m.mx.Unlock()

	for _, a := range m.addrs {
		s := m.statuses[string(a.Bytes())]
		s.outcomes.RemoveBefore(m.now().Add(-maxProbeResultTTL)) // cleanup stale results
		switch s.outcomes.Reachability() {
		case network.ReachabilityPublic:
			reachable = append(reachable, a)
		case network.ReachabilityPrivate:
			unreachable = append(unreachable, a)
		}
	}
	return reachable, unreachable
}

// UpdateAddrs updates the tracked addrs
func (m *probeManager) UpdateAddrs(addrs []ma.Multiaddr) {
	m.mx.Lock()
	defer m.mx.Unlock()

	slices.SortFunc(addrs, func(a, b ma.Multiaddr) int { return a.Compare(b) })

	for _, addr := range addrs {
		k := string(addr.Bytes())
		if _, ok := m.statuses[k]; !ok {
			m.statuses[k] = &addrStatus{Addr: addr, outcomes: addrOutcomes{}}
		}
	}
	for k, s := range m.statuses {
		_, ok := slices.BinarySearchFunc(addrs, s.Addr, func(a, b ma.Multiaddr) int { return a.Compare(b) })
		if !ok {
			delete(m.statuses, k)
		}
	}
	m.addrs = addrs
}

// GetProbe returns the next probe. Returns empty slice in case there are no more probes.
// Probes that are run against an autonatv2 client should be marked in progress with
// `MarkProbeInProgress` before running.
func (m *probeManager) GetProbe() []autonatv2.Request {
	m.mx.Lock()
	defer m.mx.Unlock()

	now := m.now()
	for i, a := range m.addrs {
		ab := a.Bytes()
		pc := m.requiredProbeCount(m.statuses[string(ab)], now)
		if pc == 0 {
			continue
		}
		if m.inProgressProbes[string(ab)] >= pc {
			continue
		}
		reqs := make([]autonatv2.Request, 0, maxAddrsPerRequest)
		reqs = append(reqs, autonatv2.Request{Addr: a, SendDialData: true})
		// We have the first(primary) address. Append other addresses, ignoring inprogress probes
		// on secondary addresses. The expectation is that the primary address will
		// be dialed.
		for j := 1; j < len(m.addrs); j++ {
			k := (i + j) % len(m.addrs)
			ab := m.addrs[k].Bytes()
			pc := m.requiredProbeCount(m.statuses[string(ab)], now)
			if pc == 0 {
				continue
			}
			reqs = append(reqs, autonatv2.Request{Addr: m.addrs[k], SendDialData: true})
			if len(reqs) >= maxAddrsPerRequest {
				break
			}
		}
		return reqs
	}
	return nil
}

// MarkProbeInProgress should be called when a probe is started.
// All in progress probes *MUST* be completed with `CompleteProbe`
func (m *probeManager) MarkProbeInProgress(reqs []autonatv2.Request) {
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
func (m *probeManager) CompleteProbe(reqs []autonatv2.Request, res autonatv2.Result, err error) {
	now := m.now()

	if len(reqs) == 0 {
		// should never happen
		return
	}

	m.mx.Lock()
	defer m.mx.Unlock()

	// decrement in-progress count for the first address
	primaryAddrKey := string(reqs[0].Addr.Bytes())
	m.inProgressProbes[primaryAddrKey]--
	if m.inProgressProbes[primaryAddrKey] <= 0 {
		delete(m.inProgressProbes, primaryAddrKey)
	}
	m.inProgressProbesTotal--

	// nothing to do if the request errored.
	if err != nil {
		return
	}

	// Consider only primary address as refused. This increases the number of
	// probes are refused, but refused probes are cheap as no dial is
	// made by the server.
	if res.AllAddrsRefused {
		if s, ok := m.statuses[primaryAddrKey]; ok {
			m.addRefusal(s, now)
		}
		return
	}
	dialAddrKey := string(res.Addr.Bytes())
	if dialAddrKey != primaryAddrKey {
		if s, ok := m.statuses[primaryAddrKey]; ok {
			m.addRefusal(s, now)
		}
	}

	// record the result for the dialled address
	expireBefore := now.Add(-maxProbeInterval)
	if s, ok := m.statuses[dialAddrKey]; ok {
		m.addDial(s, now, res.Reachability, expireBefore)
	}
}

func (*probeManager) addRefusal(s *addrStatus, now time.Time) {
	s.lastRefusalTime = now
	s.consecutiveRefusals++
}

func (*probeManager) addDial(s *addrStatus, now time.Time, rch network.Reachability, expireBefore time.Time) {
	s.lastRefusalTime = time.Time{}
	s.consecutiveRefusals = 0
	s.dialTimes = append(s.dialTimes, now)
	s.outcomes.AddOutcome(now, rch, maxRecentDialsWindow)
	s.outcomes.RemoveBefore(expireBefore)
}

func (m *probeManager) requiredProbeCount(s *addrStatus, now time.Time) int {
	if s.consecutiveRefusals >= maxConsecutiveRefusals {
		if now.Sub(s.lastRefusalTime) < recentProbeInterval {
			return 0
		}
		// reset every `recentProbeInterval`
		s.lastRefusalTime = time.Time{}
		s.consecutiveRefusals = 0
	}

	// Don't probe if we have probed too many times recently
	rd := m.recentDialCount(s, now)
	if rd >= maxRecentDialsPerAddr {
		return 0
	}

	return s.outcomes.RequiredProbeCount(now)
}

func (*probeManager) recentDialCount(s *addrStatus, now time.Time) int {
	cnt := 0
	for _, t := range slices.Backward(s.dialTimes) {
		if now.Sub(t) > recentProbeInterval {
			break
		}
		cnt++
	}
	return cnt
}

type dialOutcome struct {
	Success bool
	At      time.Time
}

type addrStatus struct {
	Addr                ma.Multiaddr
	lastRefusalTime     time.Time
	consecutiveRefusals int
	dialTimes           []time.Time
	outcomes            addrOutcomes
}

type addrOutcomes struct {
	outcomes []dialOutcome
}

func (o *addrOutcomes) Reachability() network.Reachability {
	rch, _, _ := o.reachabilityAndCounts()
	return rch
}

func (o *addrOutcomes) RequiredProbeCount(now time.Time) int {
	reachability, successes, failures := o.reachabilityAndCounts()
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
	if len(o.outcomes) == 0 {
		return 0
	}
	lastOutcome := o.outcomes[len(o.outcomes)-1]
	// If the last probe result is old, we need to retest
	if now.Sub(lastOutcome.At) > maxProbeInterval {
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

func (o *addrOutcomes) AddOutcome(at time.Time, rch network.Reachability, windowSize int) {
	success := false
	switch rch {
	case network.ReachabilityPublic:
		success = true
	case network.ReachabilityPrivate:
		success = false
	default:
		return // don't store the outcome if reachability is unknown
	}
	o.outcomes = append(o.outcomes, dialOutcome{At: at, Success: success})
	if len(o.outcomes) > windowSize {
		o.outcomes = slices.Delete(o.outcomes, 0, len(o.outcomes)-windowSize)
	}
}

// RemoveBefore removes outcomes before t
func (o *addrOutcomes) RemoveBefore(t time.Time) {
	var end = 0
	for ; end < len(o.outcomes); end++ {
		if !o.outcomes[end].At.Before(t) {
			break
		}
	}
	o.outcomes = slices.Delete(o.outcomes, 0, end)
}

func (o *addrOutcomes) reachabilityAndCounts() (rch network.Reachability, successes int, failures int) {
	for _, r := range o.outcomes {
		if r.Success {
			successes++
		} else {
			failures++
		}
	}
	if successes-failures >= minConfidence {
		return network.ReachabilityPublic, successes, failures
	}
	if failures-successes >= minConfidence {
		return network.ReachabilityPrivate, successes, failures
	}
	return network.ReachabilityUnknown, successes, failures
}
