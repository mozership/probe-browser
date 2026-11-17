package browser

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// What ends between the steps of a kept browser, when no step records the
// events of the page, is read from the page's own timings of its requests,
// its Resource Timing. Each step leaves when it stopped recording in the
// state of the job, and the next reads the page's timings as it begins to
// record: those that ended in between are the page's alone. The times are
// all of the Unix epoch, in milliseconds, the page's taken to the clock of
// probe-browser by how far apart the two are as they are read.

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
// with when its timeline began and its time as they were read.
type pageTimings struct {
	Origin  float64          `json:"origin"`
	Now     float64          `json:"now"`
	Entries []resourceTiming `json:"entries"`
	// skew is how far the page's clock is ahead of probe-browser's.
	skew float64
	used []bool
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
  return {origin: performance.timeOrigin, now: performance.now(),
    entries: performance.getEntriesByType('navigation').concat(performance.getEntriesByType('resource'))
      .filter((e) => e.responseEnd > 0)
      .map((e) => ({url: e.name, status: e.responseStatus || 0, start: e.startTime, end: e.responseEnd}))};
})()`

// sentNear is how far apart when the browser says a request was sent and
// when the page timed it starting can be, for the two to be taken for one.
const sentNear = 100

// readTimings reads the page's timings of the tab of t, or nil when they
// cannot be read, as of a page that is going away.
func readTimings(ctx context.Context, t *chromedp.Target) *pageTimings {
	got, err := chromedp.Evaluate[pageTimings](pageTimingsScript)(ctx, t)
	if err != nil {
		return nil
	}
	got.skew = got.Origin + got.Now - epochMS(time.Now())
	got.used = make([]bool, len(got.Entries))
	return &got
}

// epochMS returns t in milliseconds of the Unix epoch.
func epochMS(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e6
}

// reconcile takes from the page's timings what no event of the step told
// of. first are those read as the step began, which last for what ended
// after since, when the step before stopped, and before started, when
// this one began: a request carried from the step before is timed by its
// own, and one sent and ended between the steps, as a form a timer sent,
// is added. last are those read as the step stopped, which time what
// ended in the step but was sent between, of which it saw the response
// alone, and a carried request whose response came between the steps.
// A carried request that has not ended, and was sent before the document
// of the page began, ended with the document before it, and is dropped.
func (s *requestSet) reconcile(since, started float64, first, last *pageTimings) {
	if first != nil && since > 0 {
		for i, e := range first.Entries {
			if end := first.wall(e.End); end <= since || end > started {
				continue
			}
			first.used[i] = true
			if q := s.carriedFor(first, e); q != nil {
				q.timeBy(e)
				continue
			}
			q := s.of(network.RequestID(fmt.Sprintf("timing-%d", i)))
			q.url, q.notHTTP = e.URL, !isHTTP(e.URL)
			q.sent, q.ended, q.gotResponse = 1, 1, true
			q.timed, q.timedMS, q.status = true, int64(math.Round(e.End-e.Start)), e.Status
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
		case q.carried && (q.ended == 0 || !q.gotResponse):
			if i := last.find(q, started); i >= 0 {
				last.used[i] = true
				q.timeBy(last.Entries[i])
			} else if q.ended == 0 && q.wall > 0 && q.wall < last.wall(0)-sentNear {
				q.gone = true
			}
		case q.sent == 0 && q.ended != 0 && q.gotResponse && q.url != "":
			if i := last.find(q, started); i >= 0 {
				last.used[i] = true
				q.timeBy(last.Entries[i])
			}
		}
	}
}

// carriedFor returns the request carried from the step before, not yet
// timed, that e of p is the timing of, or nil.
func (s *requestSet) carriedFor(p *pageTimings, e resourceTiming) *netRequest {
	for _, id := range s.order {
		q := s.byID[id]
		if !q.carried || q.timed || q.notHTTP || q.url != e.URL || (q.ended != 0 && q.gotResponse) {
			continue
		}
		if q.wall == 0 || math.Abs(p.wall(e.Start)-q.wall) <= sentNear {
			return q
		}
	}
	return nil
}

// find returns the index of the timing of q not used, or -1: of one the
// browser told when it was sent, the one that started then; of another,
// the last of its URL that started before started, when the step began.
func (p *pageTimings) find(q *netRequest, started float64) int {
	found := -1
	for i, e := range p.Entries {
		if p.used[i] || e.URL != q.url {
			continue
		}
		if q.wall > 0 {
			if math.Abs(p.wall(e.Start)-q.wall) <= sentNear {
				return i
			}
			continue
		}
		if p.wall(e.Start) < started {
			found = i
		}
	}
	return found
}

// timeBy completes q with the page's timing of it: when no event told of
// its end, it took as long as the page timed it, and when no event told of
// its response, its status is the page's, 0 when the page has none, as of
// another origin that does not allow it.
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
