package browser

import (
	"context"
	"fmt"

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
		res, err := cdp.Call(ctx, t, page.GetNavigationHistory, cdp.Empty{})
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
