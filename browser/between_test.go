package browser

import (
	"fmt"
	"testing"

	"github.com/chromedp/cdproto/network"
)

// timings are the page's timings of entries, of a document whose timeline
// began at origin, on the clock of probe-browser.
func timings(origin float64, entries ...resourceTiming) *pageTimings {
	return &pageTimings{Origin: origin, Entries: entries, used: make([]bool, len(entries))}
}

func carried(s *requestSet, id, method, url string, wall float64) *netRequest {
	q := s.of(network.RequestID(id))
	q.method, q.url, q.sent, q.wall, q.carried = method, url, 1, wall, true
	return q
}

// TestReconcile_BetweenTheSteps takes what ended after the step before
// stopped, at 1200, and before this one began, at 2000, from the page's
// timings as the step began: a carried request by when it was sent, of
// two of its URL, and one sent and ended between, which is added.
func TestReconcile_BetweenTheSteps(t *testing.T) {
	s := newRequestSet()
	carried(s, "signup", "POST", "https://a/signup", 1000)
	carried(s, "poll", "GET", "https://a/poll", 1100)
	first := timings(0,
		resourceTiming{URL: "https://a/signup", Status: 500, Start: 1005, End: 1500},
		resourceTiming{URL: "https://a/poll", Status: 200, Start: 900, End: 1150},  // before the step before stopped
		resourceTiming{URL: "https://a/poll", Status: 503, Start: 1101, End: 1700}, // the carried one
		resourceTiming{URL: "https://a/poll", Status: 200, Start: 1300, End: 1400}, // sent between
		resourceTiming{URL: "https://a/now", Status: 404, Start: 1900, End: 2100},  // of this step
	)
	s.reconcile(1200, 2000, first, nil)
	s.settle()
	if got := fmt.Sprint(s.errs); got != "[POST https://a/signup 500 GET https://a/poll 503]" {
		t.Errorf("errors %s", got)
	}
	if s.requests.Count != 3 {
		t.Errorf("count %d, want the sign-up and the two polls", s.requests.Count)
	}
	if got := fmt.Sprint(s.requests.Slowest); got != "[{GET https://a/poll 503  599} {POST https://a/signup 500  495} { https://a/poll 200  100}]" {
		t.Errorf("slowest %s", got)
	}
	if p := s.pending(); len(p) != 0 {
		t.Errorf("pending %+v", p)
	}
}

// TestReconcile_NoStepBefore reads nothing between the steps of a step
// that does not know when the one before stopped.
func TestReconcile_NoStepBefore(t *testing.T) {
	s := newRequestSet()
	s.reconcile(0, 2000, timings(0, resourceTiming{URL: "https://a/x", Status: 500, Start: 1, End: 2}), nil)
	s.settle()
	if s.requests.Count != 0 {
		t.Errorf("count %d, want none", s.requests.Count)
	}
}

// TestReconcile_AsTheStepStopped times by the page's timings as the step
// stopped what it saw end but not sent, and gives a carried request whose
// response came between the steps its status.
func TestReconcile_AsTheStepStopped(t *testing.T) {
	s := newRequestSet()
	event(t, s, network.ResponseReceived.Method, `{"requestId":"l","response":{"url":"https://a/late","status":200}}`)
	event(t, s, network.LoadingFinished.Method, `{"requestId":"l","timestamp":3}`)
	q := carried(s, "c", "POST", "https://a/form", 1000)
	q.sent = 10
	event(t, s, network.LoadingFinished.Method, `{"requestId":"c","timestamp":12.5}`)
	last := timings(0,
		resourceTiming{URL: "https://a/form", Status: 500, Start: 1002, End: 2300},
		resourceTiming{URL: "https://a/late", Status: 200, Start: 1900, End: 2300},
	)
	s.reconcile(1200, 2000, nil, last)
	s.settle()
	if got := fmt.Sprint(s.errs); got != "[POST https://a/form 500]" {
		t.Errorf("errors %s", got)
	}
	if got := fmt.Sprint(s.requests.Slowest); got != "[{POST https://a/form 500  2500} { https://a/late 200  400}]" {
		t.Errorf("slowest %s, want the form timed by its events, the late one by the page", got)
	}
}

// TestReconcile_GoneWithItsDocument drops a carried request that has not
// ended and was sent before the document of the page began, and keeps one
// sent after.
func TestReconcile_GoneWithItsDocument(t *testing.T) {
	s := newRequestSet()
	carried(s, "old", "GET", "https://a/stream", 1000)
	carried(s, "new", "GET", "https://a/events", 1600)
	s.reconcile(1200, 2000, nil, timings(1500))
	s.settle()
	if s.requests.Count != 0 {
		t.Errorf("count %d, want none", s.requests.Count)
	}
	if p := s.pending(); len(p) != 1 || p[0].URL != "https://a/events" {
		t.Errorf("pending %+v, want the one of the document alone", p)
	}
}

// TestPending_KeepsAResponseNotSent leaves to the next step a request this
// one saw the response of, and not sent, still loading.
func TestPending_KeepsAResponseNotSent(t *testing.T) {
	s := newRequestSet()
	event(t, s, network.ResponseReceived.Method, `{"requestId":"b","response":{"url":"https://a/big","status":200}}`)
	if p := s.pending(); len(p) != 1 || p[0].URL != "https://a/big" || !p[0].Response {
		t.Errorf("pending %+v, want the big one", p)
	}
}
