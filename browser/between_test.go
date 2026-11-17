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
	s.reconcile(1200, 2000, 3000, first, nil)
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
	s.reconcile(0, 2000, 3000, timings(0, resourceTiming{URL: "https://a/x", Status: 500, Start: 1, End: 2}), nil)
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
	s.reconcile(1200, 2000, 3000, nil, last)
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
	s.reconcile(1200, 2000, 3000, nil, timings(1500))
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

// TestReconcile_WhileNotRecording adds of the page's timings as the step
// stopped one sent between the steps that ended in the step, which it saw
// neither sent nor answered, and one that ended after it stopped
// recording, at 3000, and not those it saw.
func TestReconcile_WhileNotRecording(t *testing.T) {
	s := newRequestSet()
	event(t, s, network.RequestWillBeSent.Method, `{"requestId":"n","timestamp":5,"wallTime":2.5,"request":{"url":"https://a/now","method":"GET"}}`)
	event(t, s, network.ResponseReceived.Method, `{"requestId":"n","response":{"url":"https://a/now","status":200}}`)
	event(t, s, network.LoadingFinished.Method, `{"requestId":"n","timestamp":5.1}`)
	event(t, s, network.LoadingFinished.Method, `{"requestId":"x","timestamp":5.2}`)
	last := timings(0,
		resourceTiming{URL: "https://a/now", Status: 200, Start: 2501, End: 2600},
		resourceTiming{URL: "https://a/across", Status: 500, Start: 1500, End: 2400},
		resourceTiming{URL: "https://a/tail", Status: 404, Start: 2900, End: 3050},
		resourceTiming{URL: "https://a/old", Status: 500, Start: 100, End: 900},
	)
	s.reconcile(1200, 2000, 3000, nil, last)
	s.settle()
	if got := fmt.Sprint(s.errs); got != "[https://a/across 500 https://a/tail 404]" {
		t.Errorf("errors %s", got)
	}
	if s.requests.Count != 3 {
		t.Errorf("count %d, want now, across and the tail", s.requests.Count)
	}
}

// TestReconcile_ByTheURLAskedAndTheNearest matches a carried request that
// was redirected by the URL it was asked for, and of two of one URL, the
// timing that started nearest when it was sent.
func TestReconcile_ByTheURLAskedAndTheNearest(t *testing.T) {
	s := newRequestSet()
	q := carried(s, "r", "GET", "https://a/final", 1000)
	q.firstURL = "https://a/start"
	carried(s, "p", "GET", "https://a/poll", 1050)
	first := timings(0,
		resourceTiming{URL: "https://a/start", Status: 200, Start: 1001, End: 1500},
		resourceTiming{URL: "https://a/poll", Status: 500, Start: 1052, End: 1600},
	)
	carried(s, "p0", "GET", "https://a/poll", 1000)
	s.reconcile(1200, 2000, 3000, first, nil)
	s.settle()
	if got := fmt.Sprint(s.errs); got != "[GET https://a/poll 500]" {
		t.Errorf("errors %s", got)
	}
	if p := s.pending(); len(p) != 1 || p[0].ID != "p0" {
		t.Errorf("pending %+v, want the poll sent first, still on", p)
	}
	if s.requests.Count != 2 {
		t.Errorf("count %d, want the redirected one and a poll", s.requests.Count)
	}
}

// TestSettle_AnsweredNotSent counts a request the step saw answered and
// end, and not sent, though no timing tells how long it took.
func TestSettle_AnsweredNotSent(t *testing.T) {
	s := newRequestSet()
	event(t, s, network.ResponseReceived.Method, `{"requestId":"u","response":{"url":"https://a/u","status":500}}`)
	event(t, s, network.LoadingFinished.Method, `{"requestId":"u","timestamp":3}`)
	s.settle()
	if s.requests.Count != 1 || fmt.Sprint(s.errs) != "[https://a/u 500]" || len(s.requests.Slowest) != 0 {
		t.Errorf("count %d, errors %v, slowest %v", s.requests.Count, s.errs, s.requests.Slowest)
	}
}

// TestStoppedFromState reads when the step before stopped, which the
// runner gives as an int64 when it has no fraction.
func TestStoppedFromState(t *testing.T) {
	for _, v := range []any{float64(1700000000123), int64(1700000000123)} {
		if got := stoppedFromState(map[string]any{"network_stopped": v}); got != 1700000000123 {
			t.Errorf("%T: %v", v, got)
		}
	}
}
