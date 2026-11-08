package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
)

// selectorRetryInterval is how often a query looks at the page again while
// no element matches. chromedp's default of 5ms is meant for a lookup the
// browser does natively; a selector here is a script that walks the shadow
// roots and reads texts, which a page should not run 200 times a second.
const selectorRetryInterval = 50 * time.Millisecond

// findScript defines the functions the selectors of the actions run in the
// page. It is an expression for an object of them, so that a script can use
// it as it is, without leaving anything in the page's global scope.
//
//   - all(css, text) gives the elements that match css, in the document and
//     in every open shadow root, in the order of the page, the elements of a
//     shadow root right after its host. A closed shadow root is out of reach
//     of a script, so its elements are not found. With text, it keeps those
//     whose visible text contains text, spaces aside, and of them only the
//     innermost, so that a button is found rather than the card around it.
//     Without css, every element is a candidate.
//   - text(el) gives the visible text of el, as innerText does, and the text
//     of the open shadow roots in it, which innerText leaves out.
const findScript = `(() => {
	const box = (el) => Boolean(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
	const squash = (s) => s.replace(/\s+/g, " ").trim();
	// localName, as the title of an SVG is "title" and not "TITLE".
	const skipped = new Set(["script", "style", "template", "noscript", "head", "title", "desc"]);

	// each calls f on the elements of root in the order of the page, those
	// of a shadow root right after its host.
	const each = (root, f) => {
		for (const el of root.querySelectorAll("*")) {
			f(el);
			if (el.shadowRoot) {
				each(el.shadowRoot, f);
			}
		}
	};

	// hosts gives el, when it has an open shadow root, and the elements
	// under it that have one, those in the shadow roots included.
	const hosts = (el) => {
		const out = el.shadowRoot ? [el] : [];
		each(el.shadowRoot ?? el, (n) => n.shadowRoot && out.push(n));
		if (el.shadowRoot) {
			each(el, (n) => n.shadowRoot && out.push(n));
		}
		return out;
	};

	const inline = (el) => /^(inline|contents|ruby)/.test(getComputedStyle(el).display);

	// rendered gives the text of a node of the flat tree, with a line break
	// around each block, as innerText puts them: an element's innerText when
	// no shadow root is under it, and otherwise the text of the children of
	// its shadow root, or of its own, and of the nodes slotted into it.
	const rendered = (node) => {
		if (node.nodeType === Node.TEXT_NODE) {
			// A text node in a shadow root takes its style from the host.
			const parent = node.parentElement ?? node.parentNode?.host;
			if (!parent || !box(parent)) {
				return "";
			}
			const style = getComputedStyle(parent);
			if (style.visibility !== "visible") {
				return "";
			}
			return style.whiteSpace.startsWith("pre") ? node.nodeValue : node.nodeValue.replace(/\s+/g, " ");
		}
		if (node.nodeType !== Node.ELEMENT_NODE || skipped.has(node.localName)) {
			return "";
		}
		if (node.localName === "br") {
			return "\n";
		}
		if (node.localName === "slot") {
			return node.assignedNodes({ flatten: true }).map(rendered).join("");
		}
		if (!box(node)) {
			return "";
		}
		let t;
		if (hosts(node).length === 0) {
			t = leaf(node);
		} else {
			const children = node.shadowRoot ? node.shadowRoot.childNodes : node.childNodes;
			t = Array.from(children, rendered).join("");
		}
		return inline(node) ? t : "\n" + t + "\n";
	};

	// leaf gives the visible text of an element with no shadow root under
	// it. An SVG has no innerText, and shows only the text of its text
	// elements, not that of its title.
	const leaf = (el) => {
		if (el instanceof HTMLElement) {
			return el.innerText;
		}
		if (el instanceof SVGElement) {
			const texts = el.localName === "text" ? [el] : Array.from(el.querySelectorAll("text"));
			return texts.map((t) => t.textContent).join(" ");
		}
		return el.textContent ?? "";
	};

	const text = (el) => {
		if (!box(el)) {
			return "";
		}
		if (hosts(el).length === 0) {
			return leaf(el);
		}
		// innerText leaves out the shadow roots, so the lines of the parts are
		// put together the way it would: trimmed, and the empty ones dropped.
		return rendered(el).split("\n").map((l) => l.replace(/[ \t]+/g, " ").trim()).filter((l) => l !== "").join("\n");
	};

	// contains tells whether b is under a, across shadow roots.
	const contains = (a, b) => {
		for (let n = b; n; n = n.parentNode ?? n.host) {
			if (n === a) {
				return true;
			}
		}
		return false;
	};

	const all = (css, want) => {
		const found = [];
		each(document, (el) => (css === "" || el.matches(css)) && found.push(el));
		if (want === "") {
			return found;
		}
		const needle = squash(want);
		// textContent is read first, as it needs no layout. It holds the
		// characters of the visible text, but not the line breaks between
		// blocks, so it is compared without spaces.
		const bare = needle.replace(/ /g, "");
		const content = (el) => (el.textContent + hosts(el).map((h) => h.shadowRoot.textContent).join("")).replace(/\s+/g, "");
		const matched = found.filter((el) => !skipped.has(el.localName) && content(el).includes(bare) && squash(text(el)).includes(needle));
		return matched.filter((el) => !matched.some((other) => other !== el && contains(el, other)));
	};

	return { all, text, visible: box };
})()`

// target is the element an action acts on: the elements that match a CSS
// selector, with a text when has_text is given.
type target struct {
	css  string
	text string
}

func (a *ChromeDPAction) target() target {
	return target{css: a.Selector, text: a.HasText}
}

// empty tells whether the action names no element.
func (tg target) empty() bool {
	return tg.css == "" && tg.text == ""
}

// String describes the target in errors, as "#size", or as "button" with
// text "Save" when it has a text.
func (tg target) String() string {
	switch {
	case tg.text == "":
		return fmt.Sprintf("%q", tg.css)
	case tg.css == "":
		return fmt.Sprintf("with text %q", tg.text)
	default:
		return fmt.Sprintf("%q with text %q", tg.css, tg.text)
	}
}

// all is a script expression for the elements the target matches.
func (tg target) all() string {
	return fmt.Sprintf("%s.all(%s, %s)", findScript, jsString(tg.css), jsString(tg.text))
}

// first is a script expression for the first element the target matches,
// or null.
func (tg target) first() string {
	return fmt.Sprintf("(%s[0] ?? null)", tg.all())
}

// path is the target as a chromedp selector, so that chromedp's actions find
// the element through the script, in shadow roots too, and wait for it.
func (tg target) path() chromedp.JSPath {
	return chromedp.JSPath(tg.first())
}

// opts are the query options of an action on the target, the extra ones
// after the default retry interval.
func (tg target) opts(extra ...chromedp.QueryOption) []chromedp.QueryOption {
	return append([]chromedp.QueryOption{chromedp.RetryInterval(selectorRetryInterval)}, extra...)
}

// textScript is a script expression for the visible text of the target's
// first element, the text of the shadow roots in it included, or null when
// no element matches, as when the page has just rendered it again.
func (tg target) textScript() string {
	return fmt.Sprintf(`((find, el) => el ? find.text(el) : null)(%s, %s)`, findScript, tg.first())
}

// elementActions are the actions that act on an element, and so need
// selector or has_text. press also takes them, but acts on the element that
// has the focus without them.
var elementActions = map[string]bool{
	"wait_visible": true, "wait_not_visible": true, "wait_enabled": true, "wait_text": true,
	"text": true, "value": true, "get_attribute": true, "get_html": true,
	"click": true, "double_click": true, "right_click": true, "hover": true,
	"type": true, "send_keys": true, "set_value": true, "select": true,
	"focus": true, "submit": true, "scroll": true, "screenshot": true,
	"press": true,
}

// checkTarget refuses an action on an element that names none, and
// has_text on an action that acts on no element, where it would be ignored.
func (a *ChromeDPAction) checkTarget() error {
	switch {
	case !elementActions[a.Name]:
		if a.HasText != "" {
			return fmt.Errorf("%s does not take has_text, as it acts on no element", a.Name)
		}
	case a.Name != "press" && a.target().empty():
		return fmt.Errorf("%s needs selector or has_text, to find the element it acts on", a.Name)
	}
	return nil
}

// readText returns an action that waits until the target is in the page,
// or visible when visible is true, then keeps its visible text in dst, the
// text of the shadow roots in it included.
func readText(tg target, visible bool, dst *string) chromedp.Action[chromedp.Void] {
	wait := chromedp.WaitReady(tg.path(), tg.opts()...)
	if visible {
		wait = chromedp.WaitVisible(tg.path(), tg.opts()...)
	}
	read := chromedp.Evaluate[*string](tg.textScript())
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		// The element can be gone between the wait and the read, as a page
		// renders it again. That is not an empty text, which would pass
		// equals: "" and not_contains, so the wait starts over.
		for {
			if _, err := wait(ctx, t); err != nil {
				return chromedp.Void{}, err
			}
			text, err := read(ctx, t)
			if err != nil {
				return chromedp.Void{}, err
			}
			if text != nil {
				*dst = *text
				return chromedp.Void{}, nil
			}
		}
	}
}
