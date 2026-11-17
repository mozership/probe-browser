package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	jsonv2 "github.com/chromedp/cdproto/cdp/jsonv2"
	"github.com/chromedp/cdproto/network"
)

func TestSlowest_KeepsTheSlowestFirst(t *testing.T) {
	var list []SlowRequest
	for i, ms := range []int64{30, 10, 50, 20, 40, 60, 5, 50} {
		list = slowest(list, SlowRequest{URL: string(rune('a' + i)), DurationMS: ms})
	}
	var got []int64
	var urls []string
	for _, r := range list {
		got = append(got, r.DurationMS)
		urls = append(urls, r.URL)
	}
	if want := []int64{60, 50, 50, 40, 30}; !equalInts(got, want) {
		t.Errorf("slowest %v, want %v", got, want)
	}
	// Of two that took as long, the one that ended first comes first.
	if urls[1] != "c" || urls[2] != "h" {
		t.Errorf("the requests of 50 ms are %v", urls[1:3])
	}
}

func equalInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func newRequestSet() *requestSet {
	return &requestSet{byID: map[network.RequestID]*netRequest{}, errs: []NetworkError{}, requests: Requests{Slowest: []SlowRequest{}}}
}

// event is an event of a method, as the browser sends it.
func event(t *testing.T, s *requestSet, method, raw string) {
	t.Helper()
	if err := s.apply(method, jsonv2.Value(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestRequestSet(t *testing.T) {
	s := newRequestSet()
	sent := network.RequestWillBeSent.Method
	resp := network.ResponseReceived.Method
	failed := network.LoadingFailed.Method
	finished := network.LoadingFinished.Method
	// The events of a redirect, those of the other methods taken first, as
	// the channels of the methods give them apart: the request is the last
	// one sent under its ID, timed from the first.
	event(t, s, finished, `{"requestId":"r","timestamp":3}`)
	event(t, s, resp, `{"requestId":"r","response":{"status":200}}`)
	event(t, s, sent, `{"requestId":"r","timestamp":1,"request":{"method":"POST","url":"https://a/form"}}`)
	event(t, s, sent, `{"requestId":"r","timestamp":2,"request":{"method":"GET","url":"https://a/done"}}`)
	// A response whose body failed after it.
	event(t, s, sent, `{"requestId":"b","timestamp":1,"request":{"method":"GET","url":"https://a/big"}}`)
	event(t, s, resp, `{"requestId":"b","response":{"status":200}}`)
	event(t, s, failed, `{"requestId":"b","timestamp":1.5,"errorText":"net::ERR_CONTENT_LENGTH_MISMATCH"}`)
	// No answer, a status of 500, one given up on, one not of HTTP, and one
	// not ended.
	event(t, s, sent, `{"requestId":"x","timestamp":1,"request":{"method":"GET","url":"https://b/"}}`)
	event(t, s, failed, `{"requestId":"x","timestamp":31,"errorText":"net::ERR_CONNECTION_TIMED_OUT"}`)
	event(t, s, sent, `{"requestId":"e","timestamp":1,"request":{"method":"GET","url":"https://a/broken"}}`)
	event(t, s, resp, `{"requestId":"e","response":{"status":500}}`)
	event(t, s, finished, `{"requestId":"e","timestamp":1.1}`)
	event(t, s, sent, `{"requestId":"c","timestamp":1,"request":{"method":"GET","url":"https://a/gone"}}`)
	event(t, s, failed, `{"requestId":"c","timestamp":1.1,"canceled":true,"errorText":"net::ERR_ABORTED"}`)
	event(t, s, sent, `{"requestId":"d","timestamp":1,"request":{"method":"GET","url":"data:,x"}}`)
	event(t, s, finished, `{"requestId":"d","timestamp":1.1}`)
	event(t, s, sent, `{"requestId":"o","timestamp":1,"request":{"method":"GET","url":"https://a/open"}}`)
	s.settle()

	if s.requests.Count != 5 {
		t.Errorf("count %d, want the five over HTTP that ended", s.requests.Count)
	}
	// A request that failed is timed to when it did.
	if top := s.requests.Slowest[0]; top.URL != "https://b/" || top.Error != "net::ERR_CONNECTION_TIMED_OUT" || top.DurationMS != 30000 {
		t.Errorf("the slowest is %+v, want the request that timed out", top)
	}
	if next := s.requests.Slowest[1]; next.URL != "https://a/done" || next.Method != "GET" || next.Status != 200 || next.DurationMS != 2000 {
		t.Errorf("the next slowest is %+v, want the redirect, its last URL, timed from the first", next)
	}
	for _, r := range s.requests.Slowest {
		if r.URL == "https://a/gone" {
			t.Errorf("a request given up on is timed: %+v", r)
		}
	}
	var got []string
	for _, e := range s.errs {
		got = append(got, e.String())
	}
	want := []string{
		"GET https://a/big 200 net::ERR_CONTENT_LENGTH_MISMATCH",
		"GET https://b/ net::ERR_CONNECTION_TIMED_OUT",
		"GET https://a/broken 500",
	}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("network errors\n%q\nwant\n%q", got, want)
	}
}

func TestRequestSet_KeepsEveryError(t *testing.T) {
	s := newRequestSet()
	for i := range 30 {
		id := fmt.Sprintf("r%d", i)
		event(t, s, network.RequestWillBeSent.Method, `{"requestId":"`+id+`","timestamp":1,"request":{"method":"GET","url":"https://a/`+id+`"}}`)
		event(t, s, network.ResponseReceived.Method, `{"requestId":"`+id+`","response":{"status":503}}`)
		event(t, s, network.LoadingFinished.Method, `{"requestId":"`+id+`","timestamp":1.1}`)
	}
	s.settle()
	if len(s.errs) != 30 {
		t.Errorf("%d network errors, want the 30", len(s.errs))
	}
}

func TestWithNetworkErrors(t *testing.T) {
	errs := make([]NetworkError, 7)
	for i := range errs {
		errs[i] = NetworkError{Method: "GET", URL: "https://a/" + string(rune('a'+i)), Status: 500}
	}
	got := withNetworkErrors(errors.New("click: timeout"), errs).Error()
	if !strings.HasPrefix(got, "click: timeout (network errors: GET https://a/a 500,") || !strings.HasSuffix(got, "and 2 more)") {
		t.Errorf("got %q", got)
	}
	if got := withNetworkErrors(errors.New("x"), nil).Error(); got != "x" {
		t.Errorf("got %q with none", got)
	}
}

// TestStop_APagePollingDoesNotKeepItWaiting stops a recorder whose events
// never go quiet, as of a page that polls.
func TestStop_APagePollingDoesNotKeepItWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &networkRecorder{cancel: cancel, events: map[string][]jsonv2.Value{}, last: time.Now()}
	r.wg.Go(func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				r.mu.Lock()
				r.last = time.Now()
				r.mu.Unlock()
			}
		}
	})
	start := time.Now()
	r.stop()
	if d := time.Since(start); d > networkDrain+200*time.Millisecond {
		t.Errorf("stop took %s, want about %s at most", d, networkDrain)
	}
}
