package basichost

import (
	"context"
	"errors"
	"fmt"
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
	maxRecentProbeResultWindow = 5

	// maxAddrsPerRequest is the maximum number of addresses to probe in a single request
	maxAddrsPerRequest = 10
	// maxTrackedAddrs is the maximum number of addresses to track
	maxTrackedAddrs = 200
	// defaultMaxConcurrency is the default number of concurrent workers for reachability checks
	defaultMaxConcurrency = 5
)

type addrsReachabilityTracker struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	cli autonatv2Client
	// reachabilityUpdateCh is used to send reachability updates.
	reachabilityUpdateCh chan reachabilityUpdate
	maxConcurrency       int
	bootDelay            time.Duration
	addrTracker          *addrsProbeTracker
	newAddrs             chan []ma.Multiaddr
	clock                clock.Clock
}

// reachabilityUpdate is the struct to notify on completion of probes.
//
// Reachable and Unreachable naming is used to avoid confusion with Public and Private IPs.
type reachabilityUpdate struct {
	Reachable   []ma.Multiaddr
	Unreachable []ma.Multiaddr
}

// newAddrsReachabilityTracker tracks reachability for addresses.
// Use UpdateAddrs to provide addresses for tracking reachability.
// reachabilityUpdateCh is notified when any reachability probes are made. The reader must dedup the events. It may be
// notified even when the reachability for any addrs has not changed.
func newAddrsReachabilityTracker(client autonatv2Client, reachabilityUpdateCh chan reachabilityUpdate, cl clock.Clock) *addrsReachabilityTracker {
	ctx, cancel := context.WithCancel(context.Background())
	if cl == nil {
		cl = clock.New()
	}
	return &addrsReachabilityTracker{
		ctx:                  ctx,
		cancel:               cancel,
		cli:                  client,
		reachabilityUpdateCh: reachabilityUpdateCh,
		addrTracker:          newAddrsTracker(cl.Now),
		bootDelay:            1 * time.Second,
		maxConcurrency:       defaultMaxConcurrency,
		newAddrs:             make(chan []ma.Multiaddr, 1),
		clock:                cl,
	}
}

func (r *addrsReachabilityTracker) UpdateAddrs(addrs []ma.Multiaddr) {
	r.newAddrs <- addrs
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

const defaultResetInterval = 10 * time.Minute

func (r *addrsReachabilityTracker) background() error {
	go func() {
		defer r.wg.Done()

		timer := r.clock.Timer(r.bootDelay)
		defer timer.Stop()

		var task reachabilityTask
		var backoffInterval time.Duration
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
				r.reachabilityUpdateCh <- r.addrTracker.GetReachabilityUpdate()
				timer.Reset(backoffInterval)
			case addrs := <-r.newAddrs:
				if len(addrs) > maxTrackedAddrs {
					log.Errorf("too many addresses (%d) for addrs reachability tracker; dropping %d", len(addrs), len(addrs)-maxTrackedAddrs)
					addrs = addrs[:maxTrackedAddrs]
				}
				if task.RespCh != nil {
					task.Cancel()
					<-task.RespCh
					// do send reachability update; if the addrs haven't changed, this update will get dropped.
					// TODO: Add a test for this!
					r.reachabilityUpdateCh <- r.addrTracker.GetReachabilityUpdate()
				}
				r.addrTracker.UpdateAddrs(addrs)
				timer.Reset(r.bootDelay)
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
	if err == nil || errors.Is(err, autonatv2.ErrDialRefused) {
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
	jc := jobsCh
	backoff := false
	blockNew := false
	for addrsTracker.InProgressProbes() > 0 || (!blockNew && len(nextProbe) > 0) {
		select {
		case jc <- nextProbe:
			addrsTracker.MarkProbeInProgress(nextProbe)
		case resp := <-resultsCh:
			addrsTracker.CompleteProbe(resp.Requests, resp.Result, resp.Err)
			if errors.Is(resp.Err, autonatv2.ErrNoValidPeers) || errors.Is(resp.Err, errTooManyConsecutiveFailures) {
				backoff = true
				blockNew = true
			}
		case <-ctx.Done():
			blockNew = true
		}
		if blockNew {
			jc = nil
		} else {
			nextProbe = addrsTracker.GetProbe()
			jc = jobsCh
			if len(nextProbe) == 0 {
				jc = nil
			}
		}
	}
	close(jobsCh)
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
	mx       sync.Mutex
	statuses map[string]*addrStatus
	addrs    []ma.Multiaddr
	now      func() time.Time

	inProgressProbes      map[string]int // addr -> count
	inProgressProbesTotal int
}

func newAddrsTracker(now func() time.Time) *addrsProbeTracker {
	return &addrsProbeTracker{
		statuses:         make(map[string]*addrStatus),
		inProgressProbes: make(map[string]int),
		now:              now,
	}
}

// GetReachabilityUpdate returns the current reachability for the addresses.
func (t *addrsProbeTracker) GetReachabilityUpdate() reachabilityUpdate {
	t.mx.Lock()
	defer t.mx.Unlock()

	t.gc()
	reachableAddrs := make([]ma.Multiaddr, 0, len(t.addrs))
	unreachableAddrs := make([]ma.Multiaddr, 0, len(t.addrs))
	for _, as := range t.statuses {
		switch as.Reachability() {
		case network.ReachabilityPublic:
			reachableAddrs = append(reachableAddrs, as.Addr)
		case network.ReachabilityPrivate:
			unreachableAddrs = append(unreachableAddrs, as.Addr)
		}
	}
	return reachabilityUpdate{
		Reachable:   reachableAddrs,
		Unreachable: unreachableAddrs,
	}
}

func (t *addrsProbeTracker) UpdateAddrs(addrs []ma.Multiaddr) {
	newAddrs := slices.DeleteFunc(slices.Clone(addrs), func(a ma.Multiaddr) bool {
		return !manet.IsPublicAddr(a)
	})
	t.mx.Lock()
	defer t.mx.Unlock()
	for _, addr := range newAddrs {
		if _, ok := t.statuses[string(addr.Bytes())]; !ok {
			t.statuses[string(addr.Bytes())] = &addrStatus{Addr: addr}
		}
	}
	for k, s := range t.statuses {
		found := false
		for _, a := range newAddrs {
			if a.Equal(s.Addr) {
				found = true
				break
			}
		}
		if !found {
			delete(t.statuses, k)
		}
	}
	t.addrs = newAddrs
}

func (t *addrsProbeTracker) GetProbe() []autonatv2.Request {
	t.mx.Lock()
	defer t.mx.Unlock()

	reqs := make([]autonatv2.Request, 0, maxAddrsPerRequest)
	now := t.now()
	for _, a := range t.addrs {
		ab := a.Bytes()
		pc := t.statuses[string(ab)].ProbeCount(now)
		if pc == 0 {
			continue
		}
		if len(reqs) == 0 && t.inProgressProbes[string(ab)] >= pc {
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
		if t.inProgressProbes[primaryAddrKey] < 0 || t.inProgressProbesTotal < 0 {
			log.Errorf("BUG: inProgressProbes[%s](%d) < 0 || inProgressProbesTotal(%d) < 0", reqs[0].Addr, t.inProgressProbes[primaryAddrKey], t.inProgressProbes)
		}
		delete(t.inProgressProbes, primaryAddrKey)
	}

	// request failed
	if err != nil {
		// request refused
		if errors.Is(err, autonatv2.ErrDialRefused) {
			for _, req := range reqs {
				if status, ok := t.statuses[string(req.Addr.Bytes())]; ok {
					status.AddResult(now, false, true)
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
			status.AddResult(now, false, true)
		}
	}

	// record the result for the probed address
	if status, ok := t.statuses[string(res.Addr.Bytes())]; ok {
		switch res.Status {
		case pb.DialStatus_OK:
			status.AddResult(now, true, false)
		case pb.DialStatus_E_DIAL_ERROR:
			status.AddResult(now, false, false)
		default:
			log.Debug("unexpected dial status", res.Addr, res.Status)
		}
	}
}

func (t *addrsProbeTracker) gc() {
	// clean up old results
	expiry := t.now().Add(-maxProbeResultTTL)
	for _, status := range t.statuses {
		status.ExpireBefore(expiry)
	}
}

type probeResult struct {
	Time    time.Time
	Refused bool
	Success bool
}

const (
	maxProbeResultTTL = 3 * time.Hour
)

type addrStatus struct {
	Addr    ma.Multiaddr
	Results []probeResult
}

type probeSummary struct {
	Successes           int
	Failures            int
	ConsecutiveRefusals int
	LastProbeTime       time.Time
}

func (s *addrStatus) Reachability() network.Reachability {
	su := s.Summary()
	if su.Successes-su.Failures >= minConfidence {
		return network.ReachabilityPublic
	}
	if su.Failures-su.Successes >= minConfidence {
		return network.ReachabilityPrivate
	}
	return network.ReachabilityUnknown
}

func (s *addrStatus) Summary() probeSummary {
	success := 0
	failures := 0
	for i := len(s.Results) - 1; i >= 0; i-- {
		rr := s.Results[i]
		if rr.Refused {
			continue
		}
		// Only query the last 4 addresses
		if rr.Success {
			success++
		} else {
			failures++
		}
		if success+failures >= maxRecentProbeResultWindow {
			break
		}
	}

	consecutiveRefusals := 0
	for i := len(s.Results) - 1; i >= 0; i-- {
		if !s.Results[i].Refused {
			break
		}
		consecutiveRefusals++
	}

	lastProbeTime := time.Time{}
	if len(s.Results) > 0 {
		lastProbeTime = s.Results[len(s.Results)-1].Time
	}

	return probeSummary{
		Successes:           success,
		Failures:            failures,
		ConsecutiveRefusals: consecutiveRefusals,
		LastProbeTime:       lastProbeTime,
	}
}

func (s *addrStatus) ProbeCount(now time.Time) int {
	su := s.Summary()
	cnt := 0
	if su.Successes >= su.Failures {
		cnt = targetConfidence - (su.Successes - su.Failures)
	}
	if su.Failures >= su.Successes {
		cnt = targetConfidence - (su.Failures - su.Successes)
	}
	if cnt <= 0 {
		if su.LastProbeTime.Add(maxProbeInterval).Before(now) {
			return 1
		}
		return 0
	}
	if su.ConsecutiveRefusals >= maxConsecutiveRefusals {
		if su.LastProbeTime.Add(addrRefusedProbeInterval).Before(now) {
			return 1
		}
		return 0
	}
	return cnt
}

func (s *addrStatus) ExpireBefore(t time.Time) {
	s.Results = slices.DeleteFunc(s.Results, func(pr probeResult) bool {
		return pr.Time.Before(t)
	})

	// if we have a request that wasn't refused, discard all refusals before it
	for i, res := range s.Results {
		if !res.Refused {
			ss := slices.DeleteFunc(s.Results[i+1:], func(pr probeResult) bool {
				return pr.Refused
			})
			s.Results = s.Results[:i+1+len(ss)]
			break
		}
	}
}

func (s *addrStatus) AddResult(at time.Time, success, refused bool) {
	s.Results = append(s.Results, probeResult{
		Success: success,
		Refused: refused,
		Time:    at,
	})
}
