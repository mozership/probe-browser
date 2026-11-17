package browser

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// NetworkError is a request of the page that failed while the actions ran:
// one the server answered with a status of 400 or more, or one that got no
// answer, as when the server could not be reached.
type NetworkError struct {
	Method string `map:"method"`
	URL    string `map:"url"`
	// Status is 0 for a request that got no answer.
	Status int64 `map:"status"`
	// Error is why a request that got no answer failed, as the browser
	// says it, such as net::ERR_CONNECTION_REFUSED.
	Error string `map:"error"`
}

// String returns the request and how it failed, as GET /api 500.
func (e NetworkError) String() string {
	if e.Status == 0 {
		return fmt.Sprintf("%s %s %s", e.Method, e.URL, e.Error)
	}
	return fmt.Sprintf("%s %s %d", e.Method, e.URL, e.Status)
}

// Requests are the requests of the page over HTTP or HTTPS that ended while
// the actions ran.
type Requests struct {
	// Count is how many finished or failed.
	Count int `map:"count"`
	// Slowest are those that took longest from when they were sent, their
	// redirects included, to their last byte, the slowest first.
	Slowest []SlowRequest `map:"slowest"`
}

// SlowRequest is a request that finished, and how long it took.
type SlowRequest struct {
	Method     string `map:"method"`
	URL        string `map:"url"`
	Status     int64  `map:"status"`
	DurationMS int64  `map:"duration_ms"`
}

// maxSlowest is how many of the slowest requests are kept.
const maxSlowest = 5

// maxNetworkErrors is how many network errors are kept, and how many a
// failure's message names.
const maxNetworkErrors = 20

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
}

// networkRecorder keeps the requests of the page from when it starts.
type networkRecorder struct {
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	open     map[network.RequestID]*netRequest
	errs     []NetworkError
	requests Requests
}

// recordNetwork starts reading the events of the requests of the tab of ctx
// until stop.
func recordNetwork(ctx context.Context) *networkRecorder {
	ctx, cancel := context.WithCancel(ctx)
	r := &networkRecorder{cancel: cancel, open: map[network.RequestID]*netRequest{}, requests: Requests{Slowest: []SlowRequest{}}}
	listen(r, chromedp.Events(ctx, network.RequestWillBeSent), func(ev network.EventRequestWillBeSent) network.RequestID { return ev.RequestID },
		func(ev network.EventRequestWillBeSent, q *netRequest) {
			// A redirect is sent again under the same ID, and is timed from
			// the first request.
			if q.sent == 0 {
				q.sent = ev.Timestamp.Float64()
			}
			q.method, q.url = ev.Request.Method, ev.Request.URL
			q.notHTTP = !strings.HasPrefix(q.url, "http:") && !strings.HasPrefix(q.url, "https:")
		})
	listen(r, chromedp.Events(ctx, network.ResponseReceived), func(ev network.EventResponseReceived) network.RequestID { return ev.RequestID },
		func(ev network.EventResponseReceived, q *netRequest) {
			if ev.Response != nil {
				q.status, q.gotResponse = ev.Response.Status, true
			}
		})
	listen(r, chromedp.Events(ctx, network.LoadingFailed), func(ev network.EventLoadingFailed) network.RequestID { return ev.RequestID },
		func(ev network.EventLoadingFailed, q *netRequest) {
			q.failed, q.canceled, q.errText = true, ev.Canceled, ev.ErrorText
			q.ended = ev.Timestamp.Float64()
		})
	listen(r, chromedp.Events(ctx, network.LoadingFinished), func(ev network.EventLoadingFinished) network.RequestID { return ev.RequestID },
		func(ev network.EventLoadingFinished, q *netRequest) {
			q.ended = ev.Timestamp.Float64()
		})
	return r
}

// listen applies the events of seq to the requests of their IDs.
func listen[E any](r *networkRecorder, seq func(func(E, error) bool), idOf func(E) network.RequestID, apply func(E, *netRequest)) {
	r.wg.Go(func() {
		for ev, err := range seq {
			if err != nil {
				return
			}
			r.mu.Lock()
			id := idOf(ev)
			q, ok := r.open[id]
			if !ok {
				q = &netRequest{}
				r.open[id] = q
			}
			apply(ev, q)
			r.settle(id, q)
			r.mu.Unlock()
		}
	})
}

// settle records a request whose events are all in: a failed one as a
// network error, unless the page or the browser gave up on it, as when the
// page went away, and a finished one by how long it took, and as a network
// error when its status is 400 or more. r.mu is held.
func (r *networkRecorder) settle(id network.RequestID, q *netRequest) {
	done := q.sent > 0 && q.ended > 0 && (q.failed || q.gotResponse)
	if !done {
		return
	}
	delete(r.open, id)
	if q.notHTTP {
		return
	}
	r.requests.Count++
	switch {
	case q.failed && !q.canceled:
		r.addError(NetworkError{Method: q.method, URL: q.url, Error: q.errText})
	case !q.failed && q.status >= 400:
		r.addError(NetworkError{Method: q.method, URL: q.url, Status: q.status})
	}
	if !q.failed {
		r.requests.Slowest = slowest(r.requests.Slowest, SlowRequest{
			Method: q.method, URL: q.url, Status: q.status,
			DurationMS: int64(math.Round((q.ended - q.sent) * 1000)),
		})
	}
}

func (r *networkRecorder) addError(e NetworkError) {
	if len(r.errs) < maxNetworkErrors {
		r.errs = append(r.errs, e)
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

// stop ends the recording and returns what it kept; a nil recorder, as for
// a runner that reads no network, has nothing. The events of what the
// actions did come before stop is called, as the end mark of the console is
// read first, on the same connection; a request that has not ended by then
// is left out.
func (r *networkRecorder) stop() ([]NetworkError, Requests) {
	if r == nil {
		return []NetworkError{}, Requests{Slowest: []SlowRequest{}}
	}
	r.cancel()
	r.wg.Wait()
	return r.stopped()
}

// stopped returns what the recorder kept.
func (r *networkRecorder) stopped() ([]NetworkError, Requests) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]NetworkError{}, r.errs...), Requests{Count: r.requests.Count, Slowest: append([]SlowRequest{}, r.requests.Slowest...)}
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
