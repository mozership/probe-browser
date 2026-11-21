package browser

import (
	"cmp"
	"context"
	"fmt"
	jsonv2 "github.com/chromedp/cdproto/cdp/jsonv2"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// NetworkError is a request of the page that failed while the actions ran:
// one the server answered with a status of 400 or more, or one that got no
// answer, as when the server could not be reached.
type NetworkError struct {
	Method string `map:"method"`
	URL    string `map:"url"`
	// Status is 0 for a request that got no answer, and that of the answer
	// for one whose body failed after it.
	Status int64 `map:"status"`
	// Error is why a request failed when it got no answer or its body did
	// not come whole, as the browser says it, such as
	// net::ERR_CONNECTION_REFUSED.
	Error string `map:"error"`
}

// String returns the request and how it failed, as GET /api 500, or GET
// /api net::ERR_CONNECTION_REFUSED, or both, of a response whose body
// failed.
func (e NetworkError) String() string {
	var parts []string
	// A request the page timed alone has no method.
	if e.Method != "" {
		parts = append(parts, e.Method)
	}
	parts = append(parts, e.URL)
	if e.Status != 0 {
		parts = append(parts, fmt.Sprint(e.Status))
	}
	if e.Error != "" {
		parts = append(parts, e.Error)
	}
	return strings.Join(parts, " ")
}

// Requests are the requests of the page over HTTP or HTTPS that ended while
// the actions ran.
type Requests struct {
	// Count is how many finished or failed.
	Count int `map:"count"`
	// Slowest are those that took longest from when they were sent, their
	// redirects included, to their last byte, or to when they failed, the
	// slowest first.
	Slowest []SlowRequest `map:"slowest"`
}

// SlowRequest is a request that ended, and how long it took: to its last
// byte, or to when it failed, with why.
type SlowRequest struct {
	Method     string `map:"method"`
	URL        string `map:"url"`
	Status     int64  `map:"status"`
	Error      string `map:"error"`
	DurationMS int64  `map:"duration_ms"`
}

// maxSlowest is how many of the slowest requests are kept.
const maxSlowest = 5

// netRequest is what the events of a request have told so far.
type netRequest struct {
	method, url string
	sent, ended float64
	status      int64
	failed      bool
	canceled    bool
	errText     string
	notHTTP     bool
	gotResponse bool
	// wall is when it was sent, in milliseconds of the Unix epoch, by
	// which the page's timing of it is told from another's of its URL.
	wall float64
	// firstURL is the URL it was asked for, before any redirect, by which
	// the page names it in its timings.
	firstURL string
	// carried is a request an earlier step left pending, whose response
	// can have come between the steps, unseen.
	carried bool
	// timedMS is how long a request took, as the page timed it, when no
	// event told of when it was sent or when it ended, as it was between
	// the steps.
	timedMS int64
	timed   bool
	// gone is a carried request that ended unseen with the document it
	// was of, which is neither counted nor carried on.
	gone bool
	// counted is one settle counted.
	counted bool
}

// networkRecorder keeps the events of the requests of the page from when
// it starts, and puts the requests together from them when it stops.
type networkRecorder struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	// events are those of each method, in the order they came.
	events map[string][]jsonv2.Value
	// before are events that came before it began, of each method, which
	// are taken first, as those a recorder that went wrote.
	before map[string][]jsonv2.Value
	// last is when the last event came.
	last time.Time
	// inflight are the requests of HTTP or HTTPS sent while it records,
	// and whether each has ended, by their IDs, for stop to wait for those
	// that have not.
	inflight map[string]bool
	// pending are the requests an earlier step left.
	pending []pendingRequest
	// since is when the step before stopped recording, and started when
	// this one began, in milliseconds of the Unix epoch, 0 when not known;
	// first are the page's timings as this one began, from which what
	// ended between them is read.
	since, started float64
	first          *pageTimings
}

// networkIdle is how long stop waits for no event to come, before it puts
// the requests together: the events of what the actions did are sent
// before the end mark of the console, but each method's reach the
// recorder on a channel of its own.
const networkIdle = 50 * time.Millisecond

// networkDrain bounds how long stop waits for the events to go quiet, as
// for a page that polls more often than networkIdle, which they never do
// on.
const networkDrain = 500 * time.Millisecond

// networkSettle bounds how long stop waits for the requests sent while it
// recorded to end, as for a form that sends one as the step ends, whose
// answer would else come between the steps, where no step sees it.
const networkSettle = 2 * time.Second

// networkMethods are the events of a request, in the order a request has
// them: they come apart, one channel each, so they are put together once
// they are all in, rather than as they come.
var networkMethods = []string{
	network.RequestWillBeSent.Method,
	network.ResponseReceived.Method,
	network.LoadingFailed.Method,
	network.LoadingFinished.Method,
}

// pendingRequest is a request a step of a kept browser saw sent, and not
// end, which the next step goes on with, so that a request the actions of
// one step began, as a fetch that answers late, is recorded by the step it
// ends in. It is kept in the state of the job.
type pendingRequest struct {
	ID       string  `json:"id"`
	Method   string  `json:"method"`
	URL      string  `json:"url"`
	Sent     float64 `json:"sent"`
	Status   int64   `json:"status,omitempty"`
	Response bool    `json:"response,omitempty"`
	// Wall is when it was sent, in milliseconds of the Unix epoch, and
	// FirstURL the URL it was asked for, before any redirect.
	Wall     float64 `json:"wall,omitempty"`
	FirstURL string  `json:"first_url,omitempty"`
	// Ended is when it ended, of one whose other events had not come by
	// the time the step stopped, as each method's come apart.
	Ended float64 `json:"ended,omitempty"`
	// Failed, Canceled and Error are of one whose failure came before it
	// was seen sent.
	Failed   bool   `json:"failed,omitempty"`
	Canceled bool   `json:"canceled,omitempty"`
	Error    string `json:"error,omitempty"`
}

// maxPending bounds the requests a step leaves to the next, the latest
// sent kept, for a page whose requests never end, as a stream.
const maxPending = 1000

// recordNetwork starts keeping the events of the requests of the tab of
// ctx until stop, going on with the requests pending an earlier step of a
// kept browser saw, and from since, when the step before stopped
// recording, in milliseconds of the Unix epoch, 0 when not known.
func recordNetwork(ctx context.Context, pending []pendingRequest, since float64) *networkRecorder {
	c := chromedp.FromContext(ctx)
	if c == nil || c.Target == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	// What ends from now on is this step's, and seen by it.
	r := &networkRecorder{cancel: cancel, events: map[string][]jsonv2.Value{}, last: time.Now(), inflight: map[string]bool{}, pending: pending, since: since, started: epochMS(time.Now())}
	for _, m := range networkMethods {
		events, unsubscribe := c.Target.Subscribe(m)
		r.wg.Go(func() {
			defer unsubscribe()
			for {
				select {
				case <-ctx.Done():
					return
				case raw, ok := <-events:
					if !ok {
						return
					}
					r.mu.Lock()
					r.events[m] = append(r.events[m], raw)
					r.last = time.Now()
					r.track(m, raw)
					r.mu.Unlock()
				}
			}
		})
	}
	return r
}

// track keeps whether the request of an event of method is in flight:
// sent, of HTTP or HTTPS, and not ended. The events of one request come
// on channels of their own, so its end can be tracked before it is sent.
// The caller holds mu.
func (r *networkRecorder) track(method string, raw jsonv2.Value) {
	var ev struct {
		RequestID string `json:"requestId"`
		Request   *struct {
			URL string `json:"url"`
		} `json:"request"`
	}
	if jsonv2.Unmarshal(raw, &ev) != nil || ev.RequestID == "" {
		return
	}
	if r.inflight == nil {
		r.inflight = map[string]bool{}
	}
	switch method {
	case network.RequestWillBeSent.Method:
		if _, seen := r.inflight[ev.RequestID]; !seen && ev.Request != nil && isHTTP(ev.Request.URL) {
			r.inflight[ev.RequestID] = false
		}
	case network.LoadingFinished.Method, network.LoadingFailed.Method:
		r.inflight[ev.RequestID] = true
	}
}

// waiting returns whether a request sent while it records has not ended.
// The caller holds mu.
func (r *networkRecorder) waiting() bool {
	for _, ended := range r.inflight {
		if !ended {
			return true
		}
	}
	return false
}

// askedURL returns the URL q was asked for, before any redirect.
func (q *netRequest) askedURL() string {
	if q.firstURL != "" {
		return q.firstURL
	}
	return q.url
}

func isHTTP(url string) bool {
	return strings.HasPrefix(url, "http:") || strings.HasPrefix(url, "https:")
}

// requestSet puts requests together from their events.
type requestSet struct {
	byID     map[network.RequestID]*netRequest
	order    []network.RequestID
	errs     []NetworkError
	requests Requests
	// since is when the step before stopped, before which a request
	// carried from it was sent.
	since float64
}

func (s *requestSet) of(id network.RequestID) *netRequest {
	q, ok := s.byID[id]
	if !ok {
		q = &netRequest{}
		s.byID[id] = q
		s.order = append(s.order, id)
	}
	return q
}

// apply applies an event of method to its request. Of the events of one
// method, a later one comes later, as a redirect is sent again under the
// same ID, with its URL and method, after the first.
func (s *requestSet) apply(method string, raw jsonv2.Value) error {
	switch method {
	case network.RequestWillBeSent.Method:
		var ev network.EventRequestWillBeSent
		if err := jsonv2.Unmarshal(raw, &ev); err != nil {
			return err
		}
		q := s.of(ev.RequestID)
		// A redirect is timed from the first request.
		if q.sent == 0 {
			q.sent = ev.Timestamp.Float64()
			q.wall = ev.WallTime.Float64() * 1000
			if ev.Request != nil {
				q.firstURL = ev.Request.URL
			}
		}
		if ev.Request != nil {
			q.method, q.url = ev.Request.Method, ev.Request.URL
		}
		q.notHTTP = !isHTTP(q.url)
	case network.ResponseReceived.Method:
		var ev network.EventResponseReceived
		if err := jsonv2.Unmarshal(raw, &ev); err != nil {
			return err
		}
		if ev.Response != nil {
			q := s.of(ev.RequestID)
			q.status, q.gotResponse = ev.Response.Status, true
			// Of a request sent before the recording, the response
			// alone tells its URL.
			if q.url == "" {
				q.url, q.notHTTP = ev.Response.URL, !isHTTP(ev.Response.URL)
			}
		}
	case network.LoadingFailed.Method:
		var ev network.EventLoadingFailed
		if err := jsonv2.Unmarshal(raw, &ev); err != nil {
			return err
		}
		q := s.of(ev.RequestID)
		q.failed, q.canceled, q.errText = true, ev.Canceled, ev.ErrorText
		q.ended = ev.Timestamp.Float64()
	case network.LoadingFinished.Method:
		var ev network.EventLoadingFinished
		if err := jsonv2.Unmarshal(raw, &ev); err != nil {
			return err
		}
		s.of(ev.RequestID).ended = ev.Timestamp.Float64()
	}
	return nil
}

// settle records the requests that ended, in the order they were first
// seen: a failed one as a network error, unless the page or the browser
// gave up on it, as when the page went away, with the status of a response
// that came before its body failed; and a finished one by how long it
// took, and as a network error when its status is 400 or more. A request
// that has not ended, or of no HTTP, is left out.
func (s *requestSet) settle() {
	for _, id := range s.order {
		q := s.byID[id]
		// A request carried from a step before that finished is counted
		// though its response came unseen, between the steps: of a status
		// not known, 0, which is not taken for an error.
		// One the step saw answered and end, and not sent, is counted
		// though it cannot be timed.
		unsent := q.sent == 0 && !q.timed
		if q.gone || (unsent && (!q.gotResponse || q.url == "")) || q.ended == 0 || (!q.failed && !q.gotResponse && !q.carried) || q.notHTTP {
			continue
		}
		s.requests.Count++
		q.counted = true
		switch {
		case q.failed && !q.canceled:
			s.errs = append(s.errs, NetworkError{Method: q.method, URL: q.url, Status: q.status, Error: q.errText})
		case !q.failed && q.status >= 400:
			s.errs = append(s.errs, NetworkError{Method: q.method, URL: q.url, Status: q.status})
		}
		// A request that failed is timed to when it did, as one that took
		// long to time out; one given up on is not timed.
		if !q.canceled && !unsent {
			ms := int64(math.Round((q.ended - q.sent) * 1000))
			if q.timed {
				ms = q.timedMS
			}
			s.requests.Slowest = slowest(s.requests.Slowest, SlowRequest{
				Method: q.method, URL: q.url, Status: q.status, Error: q.errText,
				DurationMS: ms,
			})
		}
	}
}

// slowest adds req to list, the slowest first, keeping maxSlowest; of two
// that took as long, the one that ended first comes first.
func slowest(list []SlowRequest, req SlowRequest) []SlowRequest {
	i := len(list)
	for j, l := range list {
		if req.DurationMS > l.DurationMS {
			i = j
			break
		}
	}
	if i >= maxSlowest {
		return list
	}
	list = slices.Insert(list, i, req)
	return list[:min(len(list), maxSlowest)]
}

// carry goes on with the requests pending a step before left.
func (s *requestSet) carry(pending []pendingRequest) {
	for _, p := range pending {
		q := s.of(network.RequestID(p.ID))
		q.method, q.url, q.sent, q.status, q.gotResponse, q.wall, q.firstURL, q.ended = p.Method, p.URL, p.Sent, p.Status, p.Response, p.Wall, p.FirstURL, p.Ended
		q.failed, q.canceled, q.errText = p.Failed, p.Canceled, p.Error
		q.carried = true
		q.notHTTP = !isHTTP(q.url)
	}
}

// stop ends the recording and returns the requests: those that ended by
// then, as the events of what the actions did came before the end mark of
// the console, which is read first, on the same connection, and those of
// the page that ended while it waited. It waits for the requests sent
// while it recorded to end, networkSettle at most, as for a form the last
// action sent, and then for no event to come for networkIdle,
// networkDrain at most, as of a page that polls, and not past deadline,
// when the step is to end, unless it is zero.
//
// What ended between the steps of a kept browser, which no event tells
// of, is taken from the page's own timings: first, as this step began,
// and those timings reads now; see reconcile. It returns too when it
// stopped, in milliseconds of the Unix epoch, for the next step.
//
// A nil recorder, as for a runner that reads no network, has none.
func (r *networkRecorder) stop(timings func() *pageTimings, deadline time.Time) ([]NetworkError, Requests, []pendingRequest, float64) {
	set := &requestSet{byID: map[network.RequestID]*netRequest{}, errs: []NetworkError{}, requests: Requests{Slowest: []SlowRequest{}}}
	if r == nil {
		return set.errs, set.requests, nil, 0
	}
	set.carry(r.pending)
	// A request sent while it waits for the quiet, as one a timer of the
	// last action sends, is waited for too, within networkSettle.
	settle := time.Now().Add(networkSettle)
	if !deadline.IsZero() && deadline.Before(settle) {
		settle = deadline
	}
	var drain time.Time
	for {
		r.mu.Lock()
		quiet, waiting := time.Since(r.last), r.waiting()
		r.mu.Unlock()
		now := time.Now()
		if waiting && now.Before(settle) {
			drain = time.Time{}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if drain.IsZero() {
			drain = now.Add(networkDrain)
			if !deadline.IsZero() && deadline.Before(drain) {
				drain = deadline
			}
		}
		if quiet >= networkIdle || !now.Before(drain) {
			break
		}
		time.Sleep(max(time.Millisecond, min(networkIdle-quiet, time.Until(drain))))
	}
	r.cancel()
	r.wg.Wait()
	stopped := epochMS(time.Now())
	// The events of each method are taken in turn, all of one before the
	// next, in the order of networkMethods.
	for _, m := range networkMethods {
		for _, raw := range append(r.before[m], r.events[m]...) {
			_ = set.apply(m, raw)
		}
	}
	if timings != nil {
		last := timings()
		set.reconcile(r.since, r.started, stopped, r.first, last)
		// What ended until the page read last is this step's.
		if last != nil {
			stopped = last.read
		}
	}
	set.settle()
	return set.errs, set.requests, set.pending(), stopped
}

// pending returns the requests sent and not ended, of HTTP or HTTPS, the
// latest sent maxPending of them.
func (s *requestSet) pending() []pendingRequest {
	var out []pendingRequest
	for _, id := range s.order {
		q := s.byID[id]
		if q.gone || q.counted || q.notHTTP {
			continue
		}
		if q.ended != 0 {
			// One that ended but whose other events had not come, as each
			// method's come apart, is left once, for the next step to
			// count when they do.
			if q.carried {
				continue
			}
		} else if q.sent == 0 && (!q.gotResponse || q.url == "") {
			// One this step saw the response of, and not sent, is left
			// too, for the next to time it once it ends.
			continue
		}
		out = append(out, pendingRequest{ID: string(id), Method: q.method, URL: q.url, Sent: q.sent, Status: q.status, Response: q.gotResponse, Wall: q.wall, FirstURL: q.firstURL, Ended: q.ended, Failed: q.failed, Canceled: q.canceled, Error: q.errText})
	}
	slices.SortStableFunc(out, func(a, b pendingRequest) int { return cmp.Compare(a.Sent, b.Sent) })
	return out[max(0, len(out)-maxPending):]
}

// withNetworkErrors adds the network errors to the error of failed actions,
// as one of them is often why an action failed.
func withNetworkErrors(err error, errs []NetworkError) error {
	if len(errs) == 0 {
		return err
	}
	const shown = 5
	names := make([]string, 0, shown)
	for _, e := range errs[:min(len(errs), shown)] {
		names = append(names, e.String())
	}
	more := ""
	if n := len(errs) - len(names); n > 0 {
		more = fmt.Sprintf(" and %d more", n)
	}
	return fmt.Errorf("%w (network errors: %s%s)", err, strings.Join(names, ", "), more)
}
