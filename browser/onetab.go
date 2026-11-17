package browser

import (
	"context"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// oneTabScript has a link that would open a window, by its target, and
// window.open, open their page in the tab instead. It writes to the page,
// which is why one_tab asks for it, and does it once a document.
const oneTabScript = `(() => {
	if (window.__probeOneTab) {
		return;
	}
	window.__probeOneTab = true;
	document.addEventListener("click", (e) => {
		// The link is found up the composed path, as one in a shadow root
		// is retargeted to its host.
		const a = e.composedPath().find((n) => n instanceof Element && n.matches("a[target]"));
		if (a && a.target !== "_self") {
			a.target = "_self";
		}
	}, true);
	window.open = (url) => {
		if (url) {
			location.href = url;
		}
		return window;
	};
})();`

// oneTab returns an action that keeps the step in one tab: the script runs
// in every document the tab opens from now on, and in the one it shows.
func oneTab() chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		if _, err := chromedp.Call(ctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: oneTabScript}); err != nil {
			return chromedp.Void{}, err
		}
		return chromedp.Evaluate[chromedp.Void](oneTabScript)(ctx, t)
	}
}
