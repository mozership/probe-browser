package browser

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// What ends while no step records the events of the page of a kept
// browser, between the steps, is read from the page's own timings of its
// requests, its Resource Timing. Each step leaves when it stopped in the
// state of the job, and the next reads the page's timings as it begins to
// record: those that ended in between are the page's alone. The times are
// all of the Unix epoch, in milliseconds, the page's taken to the clock of
// probe-browser by how far apart the two are as they are read. Only the
// document's own timings are read, not those of its frames.

// resourceTiming is a request as the page timed it, from its performance
// entries: of the document, or of a resource it loaded, named by the URL
// it was asked for, before any redirect, from when it started to its last
// byte, on the page's timeline.
type resourceTiming struct {
	URL    string  `json:"url"`
	Status int64   `json:"status"`
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
}

// pageTimings are the requests of the page that ended, as it timed them,
// with when its timeline began and its time as they were read.
type pageTimings struct {
	Origin  float64          `json:"origin"`
	Now     float64          `json:"now"`
	Entries []resourceTiming `json:"entries"`
	// skew is how far the page's clock is ahead of probe-browser's, and
	// read is when the page read them, on probe-browser's clock.
	skew, read float64
	used       []bool
}

// wall returns a time of the page's timeline on the clock of
// probe-browser.
func (p *pageTimings) wall(t float64) float64 {
	return p.Origin + t - p.skew
}

// timingBuffer lets the page keep the timings of more resources than the
// 250 it keeps at first, after which it keeps no more, so that those of a
// page long open are read too.
const timingBuffer = `performance.setResourceTimingBufferSize(10000)`

// pageTimingsScript returns the pageTimings of now, the document first,
// then the resources in the order they started.
const pageTimingsScript = `(() => { ` + timingBuffer + `;
  const entries = performance.getEntriesByType('navigation').concat(performance.getEntriesByType('resource'))
    .filter((e) => e.responseEnd > 0)
    .map((e) => ({url: e.name, status: e.responseStatus || 0, start: e.startTime, end: e.responseEnd}));
  return {origin: performance.timeOrigin, now: performance.now(), entries};
})()`

// sentNear is how far apart when the browser says a request was sent and
// when the page timed it starting can be, for the two to be taken for one.
const sentNear = 100

// readTimings reads the page's timings of the tab of t, or nil when they
// cannot be read, as of a page that is going away. The page read them
// about halfway through the call, which is taken for when it did.
func readTimings(ctx context.Context, t *chromedp.Target) *pageTimings {
	before := epochMS(time.Now())
	got, err := chromedp.Evaluate[pageTimings](pageTimingsScript)(ctx, t)
	if err != nil {
		return nil
	}
	got.read = (before + epochMS(time.Now())) / 2
	got.skew = got.Origin + got.Now - got.read
	got.used = make([]bool, len(got.Entries))
	return &got
}

// epochMS returns t in milliseconds of the Unix epoch.
func epochMS(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e6
}

// reconcile takes from the page's timings what no event of the step told
// of, while the step did not record: before started, when it began, after
// since, when the step before stopped, which first, read as the step
// began, tell; and after cancel, when it stopped recording, which last,
// read as it stopped, tell.
//
// Of first, a request carried from the step before is timed by its own,
// and one sent and ended between the steps, as a form a timer sent, is
// added. Of last, a request whose end no event told of is timed, as is
// one of which the step saw the response alone, sent between the steps;
// one sent between the steps that ended in the step, which it saw neither
// sent nor answered, is added, as is one that ended after cancel. A
// carried request that has not ended, and was sent before the document of
// the page began, ended with the document before it, and is dropped.
func (s *requestSet) reconcile(since, started, cancel float64, first, last *pageTimings) {
	if first != nil && since > 0 {
		for i, e := range first.Entries {
			if end := first.wall(e.End); end <= since || end > started {
				continue
			}
			first.used[i] = true
			if q := s.sentAt(first, e); q != nil {
				q.timeBy(e)
				continue
			}
			s.add(fmt.Sprintf("first-%d", i), e)
		}
	}
	if last == nil {
		return
	}
	for _, id := range s.order {
		q := s.byID[id]
		if q.notHTTP || q.timed {
			continue
		}
		switch {
		case q.wall > 0:
			i := last.closest(q)
			if i >= 0 {
				last.used[i] = true
			}
			if q.ended != 0 && q.gotResponse {
				continue
			}
			if i >= 0 {
				q.timeBy(last.Entries[i])
			} else if q.carried && q.ended == 0 && q.wall < last.wall(0)-sentNear {
				q.gone = true
			}
		case q.sent == 0 && q.ended != 0 && q.gotResponse && q.url != "":
			if i := last.sentBefore(q, started); i >= 0 {
				last.used[i] = true
				q.timeBy(last.Entries[i])
			}
		}
	}
	for i, e := range last.Entries {
		if last.used[i] {
			continue
		}
		start, end := last.wall(e.Start), last.wall(e.End)
		if (since > 0 && start > since && start <= started && end > started) || end > cancel {
			last.used[i] = true
			s.add(fmt.Sprintf("last-%d", i), e)
		}
	}
}

// add adds the request of e, of which the page's timing alone tells.
func (s *requestSet) add(id string, e resourceTiming) {
	q := s.of(network.RequestID(id))
	q.url, q.notHTTP = e.URL, !isHTTP(e.URL)
	q.sent, q.ended, q.gotResponse = 1, 1, true
	q.timed, q.timedMS, q.status = true, int64(math.Round(e.End-e.Start)), e.Status
}

// sentAt returns the request carried from the step before, not yet timed
// nor seen answered and ended, that e of p is the timing of: of its URL,
// and sent nearest when e started, within sentNear; or nil.
func (s *requestSet) sentAt(p *pageTimings, e resourceTiming) *netRequest {
	var best *netRequest
	bestD := math.Inf(1)
	for _, id := range s.order {
		q := s.byID[id]
		if !q.carried || q.timed || q.notHTTP || q.wall == 0 || q.askedURL() != e.URL || (q.ended != 0 && q.gotResponse) {
			continue
		}
		if d := math.Abs(p.wall(e.Start) - q.wall); d <= sentNear && d < bestD {
			best, bestD = q, d
		}
	}
	return best
}

// closest returns the index of the timing not used of the URL q was asked
// for that started nearest when q was sent, within sentNear, or -1.
func (p *pageTimings) closest(q *netRequest) int {
	found, bestD := -1, math.Inf(1)
	for i, e := range p.Entries {
		if p.used[i] || e.URL != q.askedURL() {
			continue
		}
		if d := math.Abs(p.wall(e.Start) - q.wall); d <= sentNear && d < bestD {
			found, bestD = i, d
		}
	}
	return found
}

// sentBefore returns the index of the timing not used of q's URL that
// started last before started, when the step began, and ended after it,
// as q, which the step saw answered and not sent, or -1.
func (p *pageTimings) sentBefore(q *netRequest, started float64) int {
	found := -1
	for i, e := range p.Entries {
		if !p.used[i] && e.URL == q.url && p.wall(e.Start) <= started && p.wall(e.End) > started {
			found = i
		}
	}
	return found
}

// timeBy completes q with the page's timing of it: when no event told of
// when it was sent or ended, it took as long as the page timed it, and
// when no event told of its response, its status is the page's, 0 when the
// page has none, as of another origin that does not allow it.
func (q *netRequest) timeBy(e resourceTiming) {
	if q.ended == 0 || q.sent == 0 {
		if q.sent == 0 {
			q.sent = 1
		}
		q.ended, q.timed, q.timedMS = q.sent, true, int64(math.Round(e.End-e.Start))
	}
	if !q.gotResponse {
		q.status, q.gotResponse = e.Status, true
	}
}
