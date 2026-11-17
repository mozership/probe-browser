package browser

import (
	"errors"
	"strings"
	"testing"

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

func TestSettle(t *testing.T) {
	r := &networkRecorder{open: map[network.RequestID]*netRequest{}, requests: Requests{Slowest: []SlowRequest{}}}
	for id, q := range map[network.RequestID]*netRequest{
		"ok":       {method: "GET", url: "https://a/ok", sent: 1, ended: 1.2, status: 200, gotResponse: true},
		"broken":   {method: "POST", url: "https://a/broken", sent: 1, ended: 1.1, status: 503, gotResponse: true},
		"refused":  {method: "GET", url: "https://b/", sent: 1, ended: 1.1, failed: true, errText: "net::ERR_CONNECTION_REFUSED"},
		"canceled": {method: "GET", url: "https://a/gone", sent: 1, ended: 1.1, failed: true, canceled: true, errText: "net::ERR_ABORTED"},
		"data":     {method: "GET", url: "data:,x", sent: 1, ended: 1.1, status: 200, gotResponse: true, notHTTP: true},
		"open":     {method: "GET", url: "https://a/open", sent: 1, status: 200, gotResponse: true},
	} {
		r.open[id] = q
		r.settle(id, q)
	}
	errs, reqs := r.stopped()
	if reqs.Count != 4 {
		t.Errorf("count %d, want the four over HTTP that ended", reqs.Count)
	}
	var got []string
	for _, e := range errs {
		got = append(got, e.String())
	}
	joined := strings.Join(got, "; ")
	if len(errs) != 2 || !strings.Contains(joined, "POST https://a/broken 503") || !strings.Contains(joined, "GET https://b/ net::ERR_CONNECTION_REFUSED") {
		t.Errorf("network errors %q, want the broken and the refused, not the canceled", joined)
	}
	if _, ok := r.open["open"]; !ok {
		t.Error("a request that has not ended was taken as done")
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
