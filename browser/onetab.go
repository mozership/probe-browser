package browser

import (
	"context"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// oneTabScript installs, once a document, what has a link that would open
// a window, by its target or by that of the page's base, and window.open,
// open their page in the tab instead, while window.__probeOneTab.on is
// true; it turns it on. A step of a kept browser without one_tab turns it
// off, window.open answering as the page's own then.
const oneTabScript = `(() => {
	if (!window.__probeOneTab) {
		const open = window.open;
		const state = { on: false };
		window.__probeOneTab = state;
		document.addEventListener("click", (e) => {
			if (!state.on) {
				return;
			}
			// The link is found up the composed path, as one in a shadow
			// root is retargeted to its host, and its target is its own or
			// the base's.
			const a = e.composedPath().find((n) => n instanceof Element && n.matches("a[href], area[href]"));
			if (!a) {
				return;
			}
			const base = a.ownerDocument.querySelector("base[target]");
			const target = a.getAttribute("target") || (base ? base.getAttribute("target") : "");
			if (target && target !== "_self") {
				a.target = "_self";
			}
		}, true);
		window.open = function (url, ...rest) {
			if (!state.on) {
				return open.call(window, url, ...rest);
			}
			if (url) {
				location.href = url;
			}
			return window;
		};
	}
	window.__probeOneTab.on = true;
})();`

// oneTabOff turns off what oneTabScript installed in the document, if it
// did.
const oneTabOff = `(() => { if (window.__probeOneTab) { window.__probeOneTab.on = false; } })()`

// oneTab returns an action that keeps the step in one tab: the script runs
// in every document the tab opens from now on, until done is called, and
// in the one it shows. id is where the registration is kept for done.
func oneTab(id *page.ScriptIdentifier) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		res, err := chromedp.Call(ctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: oneTabScript})
		if err != nil {
			return chromedp.Void{}, err
		}
		*id = res.Identifier
		return chromedp.Evaluate[chromedp.Void](oneTabScript)(ctx, t)
	}
}

// oneTabDone returns an action that stops the script of id from running in
// the documents the tab opens after the step, and turns it off in the one
// it shows, so that a later step of a kept browser without one_tab has the
// page's own windows.
func oneTabDone(id page.ScriptIdentifier) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		if id != "" {
			if _, err := chromedp.Call(ctx, page.RemoveScriptToEvaluateOnNewDocument, page.RemoveScriptToEvaluateOnNewDocumentParams{Identifier: id}); err != nil {
				return chromedp.Void{}, err
			}
		}
		return chromedp.Evaluate[chromedp.Void](oneTabOff)(ctx, t)
	}
}
