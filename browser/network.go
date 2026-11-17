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
	// carried is a request an earlier step left pending, whose response
	// can have come between the steps, unseen.
	carried bool
	// timedMS is how long a carried request took, as the page timed it,
	// when it ended between the steps, and no event told of its end.
	timedMS int64
	timed   bool
}

// networkRecorder keeps the events of the requests of the page from when
// it starts, and puts the requests together from them when it stops.
type networkRecorder struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	// events are those of each method, in the order they came.
	events map[string][]jsonv2.Value
	// last is when the last event came.
	last time.Time
	// inflight are the requests of HTTP or HTTPS sent while it records,
	// and whether each has ended, by their IDs, for stop to wait for those
	// that have not.
	inflight map[string]bool
	// pending are the requests an earlier step left.
	pending []pendingRequest
	// since is where on the page the step before stopped recording, from
	// where the page's own timings of the requests that ended before this
	// step began recording are read; at is where this step began. Either
	// is nil when not known.
	since, at *pageMark
}

// pageMark is a moment of a page: its document, by the time its timeline
// began, and the time of its timeline then, in milliseconds.
type pageMark struct {
	Origin float64 `json:"origin"`
	Now    float64 `json:"now"`
}

// pageMarkScript returns the pageMark of now, and lets the page keep the
// timings of more resources than the 250 it keeps at first, so that those
// of a page long open are read too.
const pageMarkScript = `(() => { performance.setResourceTimingBufferSize(10000); return {origin: performance.timeOrigin, now: performance.now()}; })()`

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
}

// maxPending bounds the requests a step leaves to the next, the latest
// sent kept, for a page whose requests never end, as a stream.
const maxPending = 100

// recordNetwork starts keeping the events of the requests of the tab of
// ctx until stop, going on with the requests pending an earlier step of a
// kept browser saw, and from since, where on the page it stopped.
func recordNetwork(ctx context.Context, pending []pendingRequest, since *pageMark) *networkRecorder {
	c := chromedp.FromContext(ctx)
	if c == nil || c.Target == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &networkRecorder{cancel: cancel, events: map[string][]jsonv2.Value{}, last: time.Now(), inflight: map[string]bool{}, pending: pending, since: since}
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

func isHTTP(url string) bool {
	return strings.HasPrefix(url, "http:") || strings.HasPrefix(url, "https:")
}

// requestSet puts requests together from their events.
type requestSet struct {
	byID     map[network.RequestID]*netRequest
	order    []network.RequestID
	errs     []NetworkError
	requests Requests
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
		if q.sent == 0 || q.ended == 0 || (!q.failed && !q.gotResponse && !q.carried) || q.notHTTP {
			continue
		}
		s.requests.Count++
		switch {
		case q.failed && !q.canceled:
			s.errs = append(s.errs, NetworkError{Method: q.method, URL: q.url, Status: q.status, Error: q.errText})
		case !q.failed && q.status >= 400:
			s.errs = append(s.errs, NetworkError{Method: q.method, URL: q.url, Status: q.status})
		}
		// A request that failed is timed to when it did, as one that took
		// long to time out; one given up on is not timed.
		if !q.canceled {
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

// resourceTiming is a request as the page timed it, from its performance
// entries: of the document, or of a resource it loaded, from when it
// started to its last byte, on the page's timeline.
type resourceTiming struct {
	URL    string  `json:"url"`
	Status int64   `json:"status"`
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
}

// pageTimings are the requests of the page that ended, as it timed them,
// and the page's mark of when they were read.
type pageTimings struct {
	pageMark
	Entries []resourceTiming `json:"entries"`
}

// pageTimingsScript returns the pageTimings of now, the document first,
// then the resources in the order they started.
const pageTimingsScript = `({origin: performance.timeOrigin, now: performance.now(),
  entries: performance.getEntriesByType('navigation').concat(performance.getEntriesByType('resource'))
    .filter((e) => e.responseEnd > 0)
    .map((e) => ({url: e.name, status: e.responseStatus || 0, start: e.startTime, end: e.responseEnd}))})`

// stop ends the recording, once no event has come for networkIdle and the
// requests sent while it recorded have ended, and returns the requests:
// those that ended by then, as the events of what the actions did came
// before the end mark of the console, which is read first, on the same
// connection, and those of the page that ended while it waited. It waits
// networkDrain at most for the events to go quiet, and networkSettle at
// most for the requests to end.
//
// What ended between the steps of a kept browser, which no event tells
// of, is taken from timings, the requests the page timed: a request
// carried from a step before, by its URL, and one sent after the step
// before stopped recording that ended before this one began, as a form
// sent as the step before ended. One the page has no status of, as of
// another origin, is of status 0. It returns too where on the page it
// stopped, for the next step, when timings tell it.
//
// A nil recorder, as for a runner that reads no network, has none.
func (r *networkRecorder) stop(timings func() *pageTimings) ([]NetworkError, Requests, []pendingRequest, *pageMark) {
	set := &requestSet{byID: map[network.RequestID]*netRequest{}, errs: []NetworkError{}, requests: Requests{Slowest: []SlowRequest{}}}
	if r == nil {
		return set.errs, set.requests, nil, nil
	}
	for _, p := range r.pending {
		q := set.of(network.RequestID(p.ID))
		q.method, q.url, q.sent, q.status, q.gotResponse = p.Method, p.URL, p.Sent, p.Status, p.Response
		q.carried = true
		q.notHTTP = !isHTTP(q.url)
	}
	start := time.Now()
	drain, settle := start.Add(networkDrain), start.Add(networkSettle)
	for {
		r.mu.Lock()
		quiet, waiting := time.Since(r.last), r.waiting()
		r.mu.Unlock()
		now := time.Now()
		if !waiting && (quiet >= networkIdle || !now.Before(drain)) || !now.Before(settle) {
			break
		}
		wait := networkIdle - quiet
		if wait <= 0 {
			wait = networkIdle
		}
		time.Sleep(max(time.Millisecond, min(wait, time.Until(settle))))
	}
	r.cancel()
	r.wg.Wait()
	// The events of each method are taken in turn, all of one before the
	// next, in the order of networkMethods.
	for _, m := range networkMethods {
		for _, raw := range r.events[m] {
			_ = set.apply(m, raw)
		}
	}
	var end *pageMark
	if timings != nil {
		if t := timings(); t != nil {
			end = &t.pageMark
			used := set.timeUntimed(t.Entries)
			if r.since != nil && r.at != nil && r.at.Origin == t.Origin {
				set.addBetween(t.Entries, used, *r.since, *r.at)
			}
		}
	}
	set.settle()
	return set.errs, set.requests, set.pending(), end
}

// timeUntimed times by the first of timings of its URL each request whose
// events leave it untimed: one carried from a step before that no event
// was seen to end, as it ended between the steps, and one ended that no
// event was seen sent, as it was sent between them. It takes the status
// the page had of it when no event told it, and how long the page took
// for it. Each timing times one request at most; it returns those used.
func (s *requestSet) timeUntimed(timings []resourceTiming) []bool {
	used := make([]bool, len(timings))
	for _, id := range s.order {
		q := s.byID[id]
		carried := q.carried && q.ended == 0
		unsent := q.sent == 0 && q.ended != 0 && q.gotResponse && q.url != ""
		if (!carried && !unsent) || q.notHTTP {
			continue
		}
		for i, t := range timings {
			if used[i] || t.URL != q.url {
				continue
			}
			used[i] = true
			if q.sent == 0 {
				q.sent = 1
			}
			q.ended, q.timed, q.timedMS = q.sent, true, int64(math.Round(t.End-t.Start))
			q.gotResponse = true
			if q.status == 0 {
				q.status = t.Status
			}
			break
		}
	}
	return used
}

// addBetween adds the requests of timings not used that the page sent
// after since, where the step before stopped recording, and that ended
// before at, where this one began: every request of the document at is of
// is after since when since is of another document.
func (s *requestSet) addBetween(timings []resourceTiming, used []bool, since, at pageMark) {
	for i, t := range timings {
		if used[i] || t.End > at.Now || (since.Origin == at.Origin && t.Start < since.Now) {
			continue
		}
		q := s.of(network.RequestID(fmt.Sprintf("timing-%d", i)))
		q.url, q.notHTTP = t.URL, !isHTTP(t.URL)
		q.sent, q.ended, q.gotResponse = 1, 1, true
		q.timed, q.timedMS, q.status = true, int64(math.Round(t.End-t.Start)), t.Status
	}
}

// pending returns the requests sent and not ended, of HTTP or HTTPS, the
// latest sent maxPending of them.
func (s *requestSet) pending() []pendingRequest {
	var out []pendingRequest
	for _, id := range s.order {
		q := s.byID[id]
		if q.sent == 0 || q.ended != 0 || q.notHTTP {
			continue
		}
		out = append(out, pendingRequest{ID: string(id), Method: q.method, URL: q.url, Sent: q.sent, Status: q.status, Response: q.gotResponse})
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
