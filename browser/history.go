package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// history returns an action that goes back, for a step of -1, or forward,
// for 1, in the history of the tab, and waits for the page to load. The
// browser keeps no page in its back-forward cache (see buildChromeDPOptions),
// so the page loads anew, as chromedp waits for. With no page there, it
// fails at once: the blank page a browser starts on is none, as going back
// to it leaves no page to drive.
func history(step int64) chromedp.Action[chromedp.Void] {
	name, move := "back", chromedp.NavigateBack()
	if step > 0 {
		name, move = "forward", chromedp.NavigateForward()
	}
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		res, err := currentHistory(ctx, t)
		if err != nil {
			return chromedp.Void{}, err
		}
		to := res.CurrentIndex + step
		if to < 0 || to >= int64(len(res.Entries)) || (to == 0 && res.Entries[0].URL == "about:blank") {
			return chromedp.Void{}, fmt.Errorf("%s: the tab has no page to go %s to", name, name)
		}
		if _, err := move(ctx, t); err != nil {
			return chromedp.Void{}, fmt.Errorf("%s to %s: %w", name, res.Entries[to].URL, err)
		}
		return chromedp.Void{}, nil
	}
}

// historySync bounds how long currentHistory waits for the history of the
// tab to come to the page's URL.
const historySync = time.Second

var locationHref = chromedp.Evaluate[string]("location.href")

// currentHistory returns the history of the tab once its current entry is
// of the URL the page shows, or after historySync: the browser learns of an
// entry of the same document, as one a page pushes with history.pushState,
// a moment after the page has it, and the entry before would be taken for
// the current one.
func currentHistory(ctx context.Context, t *chromedp.Target) (*page.GetNavigationHistoryResult, error) {
	deadline := time.Now().Add(historySync)
	for {
		res, err := cdp.Call(ctx, t, page.GetNavigationHistory, cdp.Empty{})
		if err != nil {
			return nil, err
		}
		href, err := locationHref(ctx, t)
		if err != nil || time.Now().After(deadline) ||
			(res.CurrentIndex >= 0 && res.CurrentIndex < int64(len(res.Entries)) && res.Entries[res.CurrentIndex].URL == href) {
			return &res, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
