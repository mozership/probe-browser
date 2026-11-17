package browser

import (
	"context"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// oneTabScript installs, once a document, what has a link that would open
// a window and window.open open their page in the tab instead, while
// window.__probeOneTab.on is true; it turns it on. A link opens a window by
// its target or that of the page's base, by a click with Control, Meta or
// Shift, or by the middle button. The link is not changed: a listener of
// the window, which hears a click after the page's own handlers, moves the
// tab for a click they did not cancel. A step of a kept browser without
// one_tab turns it off, window.open answering as the page's own then.
const oneTabScript = `(() => {
	if (!window.__probeOneTab) {
		const open = window.open;
		const state = { on: false };
		window.__probeOneTab = state;
		const follow = (e) => {
			if (!state.on || e.defaultPrevented || (e.type === "auxclick" && e.button !== 1)) {
				return;
			}
			// The link is found up the composed path, as one in a shadow
			// root is retargeted to its host.
			const a = e.composedPath().find((n) => n instanceof Element && n.matches("a[href], area[href]"));
			if (!a || a.hasAttribute("download") || a.protocol === "javascript:") {
				return;
			}
			const base = a.ownerDocument.querySelector("base[target]");
			const target = (a.getAttribute("target") || (base ? base.getAttribute("target") : "") || "").toLowerCase();
			const opens = (target && !["_self", "_top", "_parent"].includes(target)) ||
				e.type === "auxclick" || e.ctrlKey || e.metaKey || e.shiftKey;
			if (!opens) {
				return;
			}
			e.preventDefault();
			location.href = a.href;
		};
		addEventListener("click", follow);
		addEventListener("auxclick", follow);
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

// oneTabOff turns off what oneTabScript installed in the document, and in
// the frames of it a script of the page can reach, those of its origin.
const oneTabOff = `(() => {
	const off = (w) => {
		try {
			if (w.__probeOneTab) {
				w.__probeOneTab.on = false;
			}
			for (let i = 0; i < w.frames.length; i++) {
				off(w.frames[i]);
			}
		} catch (e) {
			// A frame of another origin is out of reach.
		}
	};
	off(window);
})()`

// oneTab returns an action that keeps the step in one tab: the script runs
// in every document the tab opens from now on, until done is called, and
// in the one it shows, its frames of its origin included. id is where the registration is kept for done.
func oneTab(id *page.ScriptIdentifier) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		res, err := chromedp.Call(ctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: oneTabScript})
		if err != nil {
			return chromedp.Void{}, err
		}
		*id = res.Identifier
		return chromedp.Evaluate[chromedp.Void](oneTabOn)(ctx, t)
	}
}

// oneTabOn runs oneTabScript in the document and in the frames of it a
// script of the page can reach, those of its origin, as in a kept browser
// they were loaded before the step.
var oneTabOn = `(() => {
	const on = (w) => {
		try {
			w.eval(` + jsString(oneTabScript) + `);
			for (let i = 0; i < w.frames.length; i++) {
				on(w.frames[i]);
			}
		} catch (e) {
			// A frame of another origin is out of reach.
		}
	};
	on(window);
})()`

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
