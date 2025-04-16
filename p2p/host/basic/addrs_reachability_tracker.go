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
		addrTracker:          newProbeManager(cl.Now, maxRecentProbeResultWindow),
		newAddrsProbeDelay:   1 * time.Second,
		maxConcurrency:       defaultMaxConcurrency,
		newAddrs:             make(chan []ma.Multiaddr, 1),
		clock:                cl,
	}
}

func (r *addrsReachabilityTracker) UpdateAddrs(addrs []ma.Multiaddr) {
	r.newAddrs <- slices.Clone(addrs)
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

const defaultResetInterval = 5 * time.Minute

func (r *addrsReachabilityTracker) background() error {
	go func() {
		defer r.wg.Done()

		timer := r.clock.Timer(time.Duration(math.MaxInt64))
		defer timer.Stop()

		var task reachabilityTask
		var backoffInterval time.Duration
		var reachable, unreachable []ma.Multiaddr // used to avoid allocations
		var prevReachable, prevUnreachable []ma.Multiaddr
		for {
			var resetInterval time.Duration
			select {
			case <-timer.C:
				if task.RespCh == nil {
					task = r.refreshReachability()
				}
				resetInterval = defaultResetInterval
			case backoff := <-task.RespCh:
				task = reachabilityTask{}
				if backoff {
					backoffInterval = newBackoffInterval(backoffInterval)
				}
				reachable, unreachable = r.appendConfirmedAddrs(reachable[:0], unreachable[:0])
				resetInterval = 0
			case addrs := <-r.newAddrs:
				if task.RespCh != nil {
					task.Cancel()
					backoff := <-task.RespCh
					task = reachabilityTask{}
					if backoff {
						backoffInterval = newBackoffInterval(backoffInterval)
					}
					// We must update reachable addrs here.
					// If there are no new addrs in this event we may not probe again for a while
					// and suppress any reachability updates.
					reachable, unreachable = r.appendConfirmedAddrs(reachable[:0], unreachable[:0])
				}
				resetInterval = r.newAddrsProbeDelay
				r.updateTrackedAddrs(addrs)
			case <-r.ctx.Done():
				if task.RespCh != nil {
					task.Cancel()
					<-task.RespCh
					task = reachabilityTask{}
				}
				return
			}

			if areAddrsDifferent(prevReachable, r.reachableAddrs) || areAddrsDifferent(prevUnreachable, r.unreachableAddrs) {
				reachable, unreachable = r.appendConfirmedAddrs(reachable[:0], unreachable[:0])
			}
			prevReachable, prevUnreachable = r.reachableAddrs, r.unreachableAddrs
			if backoffInterval > resetInterval {
				resetInterval = backoffInterval
			}
			timer.Reset(resetInterval)
		}
	}()
	return nil
}

func (r *addrsReachabilityTracker) ConfirmedAddrs() (reachableAddrs, unreachableAddrs []ma.Multiaddr) {
	r.mx.Lock()
	defer r.mx.Unlock()
	return slices.Clone(r.reachableAddrs), slices.Clone(r.unreachableAddrs)
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

const (
	backoffStartInterval = 5 * time.Second
	maxBackoffInterval   = 2 * defaultResetInterval
)

func newBackoffInterval(current time.Duration) time.Duration {
	if current == 0 {
		return backoffStartInterval
	}
	current *= 2
	if current > maxBackoffInterval {
		return maxBackoffInterval
	}
	return current
}

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
// errTooManyConsecutiveFailures in case of many consecutive failures
type errCountingClient struct {
	autonatv2Client
	MaxConsecutiveErrors    int
	mx                      sync.Mutex
	consecutiveErrors       int
	loggedPrivateAddrsError bool
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
		// This is hacky, but we do want to log this error
		if !c.loggedPrivateAddrsError && errors.Is(err, autonatv2.ErrPrivateAddrs) {
			log.Errorf("private IP addr in autonatv2 request: %s", err)
			c.loggedPrivateAddrsError = true // log it only once. This should never happen
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
	for i := 0; i < concurrency; i++ {
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
	// maxProbeResultTTL is the maximum time to keep probe results for an address
	maxProbeResultTTL = 3 * time.Hour
	// maxProbeInterval is the maximum interval between probes for an address
	maxProbeInterval = 1 * time.Hour
	// addrRefusedProbeInterval is the interval to probe addresses that have been refused
	// these are generally addresses with newer transports for which we don't have many peers
	// capable of dialing the transport
	addrRefusedProbeInterval = 10 * time.Minute
	// maxConsecutiveRefusals is the maximum number of consecutive refusals for an address after which
	// we wait for `addrRefusedProbeInterval` before probing again
	maxConsecutiveRefusals = 5
	// confidence is the absolute difference between the number of successes and failures for an address
	// targetConfidence is the confidence threshold for an address after which we wait for `maxProbeInterval`
	// before probing again
	targetConfidence = 3
	// minConfidence is the confidence threshold for an address to be considered reachable or unreachable
	// confidence is the absolute difference between the number of successes and failures for an address
	minConfidence = 2
	// maxRecentProbeResultWindow is the maximum number of recent probe results to consider for a single address
	//
	// +2 allows for 1 invalid probe result. Consider a string of successes, after which we have a single failure
	// and then a success(...S S S S F S). The confidence in the targetConfidence window  will be equal to
	// targetConfidence, the last F and S cancel each other, and we won't probe again for maxProbeInterval.
	maxRecentProbeResultWindow = targetConfidence + 2
)

// probeManager tracks reachability for a set of addresses. This struct decides the priority order of
// addresses for testing reachability.
//
// Use the `runProbes` function to execute the probes with an autonatv2 client.
//
// Probes returned by `GetProbe` should be marked as in progress using `MarkProbeInProgress`
// before being executed.
type probeManager struct {
	now                          func() time.Time
	recentProbeResultWindow      int
	ProbeInterval                time.Duration
	MaxProbesPerAddrsPerInterval int

	mx                    sync.Mutex
	inProgressProbes      map[string]int // addr -> count
	inProgressProbesTotal int
	statuses              map[string]*addrStatus
	addrs                 []ma.Multiaddr
}

func newProbeManager(now func() time.Time, recentProbeResultWindow int) *probeManager {
	return &probeManager{
		statuses:                make(map[string]*addrStatus),
		inProgressProbes:        make(map[string]int),
		now:                     now,
		recentProbeResultWindow: recentProbeResultWindow,
	}
}

// AppendConfirmedAddrs appends the current confirmed reachable and unreachable addresses.
func (m *probeManager) AppendConfirmedAddrs(reachable, unreachable []ma.Multiaddr) (reachableAddrs, unreachableAddrs []ma.Multiaddr) {
	m.mx.Lock()
	defer m.mx.Unlock()

	for _, a := range m.addrs {
		switch m.statuses[string(a.Bytes())].outcomes.Reachability() {
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
	for _, addr := range addrs {
		if _, ok := m.statuses[string(addr.Bytes())]; !ok {
			m.statuses[string(addr.Bytes())] = &addrStatus{Addr: addr}
		}
	}
	for k, s := range m.statuses {
		found := false
		for _, a := range addrs {
			if a.Equal(s.Addr) {
				found = true
				break
			}
		}
		if !found {
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

	reqs := make([]autonatv2.Request, 0, maxAddrsPerRequest)
	now := m.now()
	for _, a := range m.addrs {
		akey := string(a.Bytes())
		pc := m.probeCount(m.statuses[akey], now)
		if pc == 0 {
			continue
		}
		if len(reqs) == 0 && m.inProgressProbes[akey] >= pc {
			continue
		}
		reqs = append(reqs, autonatv2.Request{Addr: a, SendDialData: true})
		if len(reqs) >= maxAddrsPerRequest {
			break
		}
	}
	return reqs
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
	m.inProgressProbesTotal--
	if m.inProgressProbes[primaryAddrKey] <= 0 {
		delete(m.inProgressProbes, primaryAddrKey)
	}

	// nothing to do if the request errored.
	if err != nil {
		return
	}

	expireBefore := now.Add(-maxProbeInterval)
	// request failed
	if res.AllAddrsRefused {
		if s, ok := m.statuses[primaryAddrKey]; ok {
			s.lastRefusalTime = now
			s.consecutiveRefusals++
		}
		return
	}

	// mark only the primary status as refused
	if s, ok := m.statuses[primaryAddrKey]; ok {
		s.lastRefusalTime = now
		s.consecutiveRefusals++
	}

	// record the result for the probed address
	if s, ok := m.statuses[string(res.Addr.Bytes())]; ok {
		s.outcomes.AddDialOutcomeAndExpire(now, res.Reachability, m.recentProbeResultWindow, expireBefore)
		s.probeTimes = append(s.probeTimes, now)
	}
}

func (m *probeManager) probeCount(s *addrStatus, now time.Time) int {
	if s.consecutiveRefusals >= maxConsecutiveRefusals {
		if now.Sub(s.lastRefusalTime) < addrRefusedProbeInterval {
			return 0
		}
		// reset this
		s.lastRefusalTime = time.Time{}
		s.consecutiveRefusals = 0
	}

	// Don't probe if we have probed too many times recently
	if m.recentProbesCount(s, now) >= m.MaxProbesPerAddrsPerInterval {
		return 0
	}

	return s.outcomes.ProbeCount(now)
}

func (m *probeManager) recentProbesCount(s *addrStatus, now time.Time) int {
	cnt := 0
	for _, t := range slices.Backward(s.probeTimes) {
		if now.Sub(t) > m.ProbeInterval {
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
	probeTimes          []time.Time
	outcomes            *addrOutcomes // TODO: no pointer?
}

type addrOutcomes struct {
	outcomes []dialOutcome
}

func (o *addrOutcomes) Reachability() network.Reachability {
	successes, failures := o.outcomeCounts()
	return o.computeReachability(successes, failures)
}

func (*addrOutcomes) computeReachability(success, failures int) network.Reachability {
	if success-failures >= minConfidence {
		return network.ReachabilityPublic
	}
	if failures-success >= minConfidence {
		return network.ReachabilityPrivate
	}
	return network.ReachabilityUnknown
}

func (o *addrOutcomes) numProbesInInterval(now time.Time, probeInterval time.Duration) int {
	cnt := 0
	for _, v := range slices.Backward(o.outcomes) {
		if now.Sub(v.At) > probeInterval {
			break
		}
		cnt++
	}
	return cnt
}

func (o *addrOutcomes) ProbeCount(now time.Time) int {
	successes, failures := o.outcomeCounts()
	confidence := successes - failures
	if confidence < 0 {
		confidence = -confidence
	}
	cnt := targetConfidence - confidence
	if cnt > 0 {
		return cnt
	}
	// we have enough confirmations, see if we should still retest

	// There are no confirmations. This should never happen. In case there are no confirmations,
	// the confidence logic above should require a few probes.
	if len(o.outcomes) == 0 {
		return 0
	}
	lastOutcome := o.outcomes[len(o.outcomes)-1]
	// If the last probe result is old, we need to retest
	if now.Sub(lastOutcome.At) > maxProbeInterval {
		return 1
	}
	// if the last probe result was different from reachability, probe again.
	switch o.computeReachability(successes, failures) {
	case network.ReachabilityPublic:
		if !lastOutcome.Success {
			return 1
		}
	case network.ReachabilityPrivate:
		if lastOutcome.Success {
			return 1
		}
	default:
		// this should never happen. no reachability => confidence is low
		return 1
	}
	return 0
}

func (o *addrOutcomes) AddDialOutcomeAndExpire(at time.Time, rch network.Reachability, windowSize int, expireBefore time.Time) {
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
	o.removeBefore(expireBefore)
}

// removeBefore removes outcomes before t
func (o *addrOutcomes) removeBefore(t time.Time) {
	st := 0
	var ot dialOutcome
	for st, ot = range o.outcomes {
		if ot.At.After(t) {
			break
		}
	}
	o.outcomes = slices.Delete(o.outcomes, 0, st)
}

func (o *addrOutcomes) outcomeCounts() (successes, failures int) {
	for _, r := range o.outcomes {
		if r.Success {
			successes++
		} else {
			failures++
		}
	}
	return successes, failures
}
