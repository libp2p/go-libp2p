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
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
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
	addrTracker          *addrsProbeTracker
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
		addrTracker:          newAddrsTracker(cl.Now, maxRecentProbeResultWindow),
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
		for {
			select {
			case <-timer.C:
				if task.RespCh == nil {
					task = r.refreshReachability()
				}
				timer.Reset(defaultResetInterval)
			case backoff := <-task.RespCh:
				task = reachabilityTask{}
				if backoff {
					backoffInterval = newBackoffInterval(backoffInterval)
				} else {
					backoffInterval = 0
				}
				reachable, unreachable = r.appendConfirmedAddrsAndNotify(reachable[:0], unreachable[:0])
				timer.Reset(backoffInterval)
			case addrs := <-r.newAddrs:
				if task.RespCh != nil {
					task.Cancel()
					<-task.RespCh
					task = reachabilityTask{}
					// We must send the event here. If there are no new addrs in this event we may not probe
					// again for a while delaying any reachability updates.
					reachable, unreachable = r.appendConfirmedAddrsAndNotify(reachable[:0], unreachable[:0])
				}
				addrs = slices.DeleteFunc(addrs, func(a ma.Multiaddr) bool {
					return !manet.IsPublicAddr(a)
				})
				if len(addrs) > maxTrackedAddrs {
					log.Errorf("too many addresses (%d) for addrs reachability tracker; dropping %d", len(addrs), len(addrs)-maxTrackedAddrs)
					addrs = addrs[:maxTrackedAddrs]
				}
				r.addrTracker.UpdateAddrs(addrs)
				timer.Reset(r.newAddrsProbeDelay)
			case <-r.ctx.Done():
				if task.RespCh != nil {
					task.Cancel()
					<-task.RespCh
					task = reachabilityTask{}
				}
				return
			}
		}
	}()
	return nil
}

func (r *addrsReachabilityTracker) appendConfirmedAddrsAndNotify(reachable, unreachable []ma.Multiaddr) (reachableAddrs, unreachableAddrs []ma.Multiaddr) {
	reachable, unreachable = r.addrTracker.AppendConfirmedAddrs(reachable, unreachable)
	r.mx.Lock()
	r.reachableAddrs = append(r.reachableAddrs[:0], reachable...)
	r.unreachableAddrs = append(r.unreachableAddrs[:0], unreachable...)
	r.mx.Unlock()
	select {
	case r.reachabilityUpdateCh <- struct{}{}:
	default:
	}
	return reachable, unreachable
}

func (r *addrsReachabilityTracker) ConfirmedAddrs() (reachableAddrs, unreachableAddrs []ma.Multiaddr) {
	r.mx.Lock()
	defer r.mx.Unlock()
	return slices.Clone(r.reachableAddrs), slices.Clone(r.unreachableAddrs)
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
	MaxConsecutiveErrors int
	mx                   sync.Mutex
	consecutiveErrors    int
}

func (c *errCountingClient) GetReachability(ctx context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
	res, err := c.autonatv2Client.GetReachability(ctx, reqs)
	c.mx.Lock()
	defer c.mx.Unlock()
	if err == nil || errors.Is(err, autonatv2.ErrDialRefused) || errors.Is(err, autonatv2.ErrNoValidPeers) {
		c.consecutiveErrors = 0
	} else {
		c.consecutiveErrors++
		if c.consecutiveErrors > c.MaxConsecutiveErrors {
			err = fmt.Errorf("%w:%w", errTooManyConsecutiveFailures, err)
		}
	}
	return res, err
}

type probeResponse struct {
	Requests []autonatv2.Request
	Result   autonatv2.Result
	Err      error
}

const maxConsecutiveErrors = 20

// runProbes runs probes provided by addrsTracker with the given client. It returns true if the caller should
// backoff before retrying probes. It stops probing when any of the following happens:
// - there are no more probes to run
// - context is completed
// - there are too many consecutive failures from the client
// - the client has no valid peers to probe
func runProbes(ctx context.Context, concurrency int, addrsTracker *addrsProbeTracker, client autonatv2Client) bool {
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
				resultsCh <- probeResponse{Requests: reqs, Result: res, Err: err}
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
			addrsTracker.CompleteProbe(resp.Requests, resp.Result, resp.Err)
			if errors.Is(resp.Err, autonatv2.ErrNoValidPeers) || errors.Is(resp.Err, errTooManyConsecutiveFailures) {
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
		addrsTracker.CompleteProbe(resp.Requests, resp.Result, resp.Err)
		if errors.Is(resp.Err, autonatv2.ErrNoValidPeers) || errors.Is(resp.Err, errTooManyConsecutiveFailures) {
			backoff = true
		}
	}
	wg.Wait()
	return backoff
}

// addrsProbeTracker tracks reachability for a set of addresses. This struct decides the priority order of
// addresses for testing reachability.
//
// To execute the probes with a client use the `runProbes` function.
//
// Probes returned by `GetProbe` should be marked as in progress using `MarkProbeInProgress`
// before being executed.
type addrsProbeTracker struct {
	now                     func() time.Time
	recentProbeResultWindow int

	mx                    sync.Mutex
	inProgressProbes      map[string]int // addr -> count
	inProgressProbesTotal int
	statuses              map[string]*addrStatus
	addrs                 []ma.Multiaddr
}

func newAddrsTracker(now func() time.Time, recentProbeResultWindow int) *addrsProbeTracker {
	return &addrsProbeTracker{
		statuses:                make(map[string]*addrStatus),
		inProgressProbes:        make(map[string]int),
		now:                     now,
		recentProbeResultWindow: recentProbeResultWindow,
	}
}

// AppendConfirmedAddrs appends the current confirmed reachable and unreachable addresses.
func (t *addrsProbeTracker) AppendConfirmedAddrs(reachable, unreachable []ma.Multiaddr) (reachableAddrs, unreachableAddrs []ma.Multiaddr) {
	t.mx.Lock()
	defer t.mx.Unlock()

	t.gc()
	for _, as := range t.statuses {
		switch as.Reachability() {
		case network.ReachabilityPublic:
			reachable = append(reachable, as.Addr)
		case network.ReachabilityPrivate:
			unreachable = append(unreachable, as.Addr)
		}
	}
	return reachable, unreachable
}

func (t *addrsProbeTracker) UpdateAddrs(addrs []ma.Multiaddr) {
	t.mx.Lock()
	defer t.mx.Unlock()
	for _, addr := range addrs {
		if _, ok := t.statuses[string(addr.Bytes())]; !ok {
			t.statuses[string(addr.Bytes())] = &addrStatus{Addr: addr}
		}
	}
	for k, s := range t.statuses {
		found := false
		for _, a := range addrs {
			if a.Equal(s.Addr) {
				found = true
				break
			}
		}
		if !found {
			delete(t.statuses, k)
		}
	}
	t.addrs = addrs
}

func (t *addrsProbeTracker) GetProbe() []autonatv2.Request {
	t.mx.Lock()
	defer t.mx.Unlock()

	reqs := make([]autonatv2.Request, 0, maxAddrsPerRequest)
	now := t.now()
	for _, a := range t.addrs {
		akey := string(a.Bytes())
		pc := t.statuses[akey].ProbeCount(now)
		if pc == 0 {
			continue
		}
		if len(reqs) == 0 && t.inProgressProbes[akey] >= pc {
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
func (t *addrsProbeTracker) MarkProbeInProgress(reqs []autonatv2.Request) {
	if len(reqs) == 0 {
		return
	}
	t.mx.Lock()
	defer t.mx.Unlock()
	t.inProgressProbes[string(reqs[0].Addr.Bytes())]++
	t.inProgressProbesTotal++
}

// InProgressProbes returns the number of probes that are currently in progress.
func (t *addrsProbeTracker) InProgressProbes() int {
	t.mx.Lock()
	defer t.mx.Unlock()
	return t.inProgressProbesTotal
}

// CompleteProbe should be called when a probe completes.
func (t *addrsProbeTracker) CompleteProbe(reqs []autonatv2.Request, res autonatv2.Result, err error) {
	now := t.now()

	if len(reqs) == 0 {
		// should never happen
		return
	}

	t.mx.Lock()
	defer t.mx.Unlock()

	// decrement in-progress count for the first address
	primaryAddrKey := string(reqs[0].Addr.Bytes())
	t.inProgressProbes[primaryAddrKey]--
	t.inProgressProbesTotal--
	if t.inProgressProbes[primaryAddrKey] <= 0 {
		delete(t.inProgressProbes, primaryAddrKey)
	}

	// request failed
	if err != nil {
		// request refused
		if errors.Is(err, autonatv2.ErrDialRefused) {
			for _, req := range reqs {
				if status, ok := t.statuses[string(req.Addr.Bytes())]; ok {
					status.AddRefusal(now)
				}
			}
		}
		return
	}

	// mark addresses that were skipped as refused
	for _, req := range reqs {
		if req.Addr.Equal(res.Addr) {
			break
		}
		if status, ok := t.statuses[string(req.Addr.Bytes())]; ok {
			status.AddRefusal(now)
		}
	}

	// record the result for the probed address
	if status, ok := t.statuses[string(res.Addr.Bytes())]; ok {
		switch res.Status {
		case pb.DialStatus_OK:
			status.AddResult(now, true)
		case pb.DialStatus_E_DIAL_ERROR:
			status.AddResult(now, false)
		default:
			log.Debug("unexpected dial status", res.Addr, res.Status)
		}
		status.Trim(t.recentProbeResultWindow)
	}
}

func (t *addrsProbeTracker) gc() {
	expireBefore := t.now().Add(-maxProbeResultTTL)
	for _, s := range t.statuses {
		s.ExpireBefore(expireBefore)
	}
}

type probeResult struct {
	Time    time.Time
	Success bool
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

type addrStatus struct {
	Addr                ma.Multiaddr
	results             []probeResult
	consecutiveRefusals struct {
		Count int
		Last  time.Time
	}
}

func (s *addrStatus) Reachability() network.Reachability {
	successes, failures := s.resultCounts()
	return s.reachability(successes, failures)
}

func (*addrStatus) reachability(success, failures int) network.Reachability {
	if success-failures >= minConfidence {
		return network.ReachabilityPublic
	}
	if failures-success >= minConfidence {
		return network.ReachabilityPrivate
	}
	return network.ReachabilityUnknown
}

func (s *addrStatus) ProbeCount(now time.Time) int {
	// if we have had too many consecutive refusals, probe after a small wait.
	if s.consecutiveRefusals.Count >= maxConsecutiveRefusals {
		if s.consecutiveRefusals.Last.Add(addrRefusedProbeInterval).Before(now) {
			return 1
		}
		return 0
	}

	successes, failures := s.resultCounts()
	cnt := 0
	if successes >= failures {
		cnt = targetConfidence - (successes - failures)
	}
	if failures >= successes {
		cnt = targetConfidence - (failures - successes)
	}
	if cnt <= 0 {
		if len(s.results) == 0 {
			return 0
		}
		if s.results[len(s.results)-1].Time.Add(maxProbeInterval).Before(now) {
			return 1
		}
		// Last probe result was different from reachability. Probe again.
		switch s.reachability(successes, failures) {
		case network.ReachabilityPublic:
			if !s.results[len(s.results)-1].Success {
				return 1
			}
		case network.ReachabilityPrivate:
			if s.results[len(s.results)-1].Success {
				return 1
			}
		}
		return 0
	}
	return cnt
}

func (s *addrStatus) resultCounts() (successes, failures int) {
	for _, r := range s.results {
		if r.Success {
			successes++
		} else {
			failures++
		}
	}
	return successes, failures
}

func (s *addrStatus) ExpireBefore(before time.Time) {
	s.results = slices.DeleteFunc(s.results, func(pr probeResult) bool {
		return pr.Time.Before(before)
	})
}

func (s *addrStatus) AddResult(at time.Time, success bool) {
	s.results = append(s.results, probeResult{
		Success: success,
		Time:    at,
	})
	s.consecutiveRefusals.Count = 0
	s.consecutiveRefusals.Last = time.Time{}
}

func (s *addrStatus) Trim(n int) {
	if len(s.results) >= n {
		s.results = s.results[len(s.results)-n:]
	}
}

func (s *addrStatus) AddRefusal(at time.Time) {
	s.consecutiveRefusals.Count++
	s.consecutiveRefusals.Last = at
}
