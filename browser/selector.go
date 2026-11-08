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
//     that have a box and whose visible text contains text, spaces aside, and
//     of them only the innermost in the flat tree, so that a button is found
//     rather than the card around it, and an element slotted in rather than
//     the element of the shadow root around the slot. Without css, every
//     element is a candidate.
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

	// slot tells whether el is a slot of a shadow root. A slot does its work
	// only there; elsewhere it is an element like any other.
	const slot = (el) => el.localName === "slot" && el.getRootNode() instanceof ShadowRoot;

	// flat tells whether el is, or has under it, an element with an open
	// shadow root or a slot of one. innerText leaves out the text of a shadow
	// root, and that of the nodes slotted into a slot, so the text of such an
	// element is read from the flat tree.
	const flat = (el) => {
		if (el.shadowRoot || slot(el)) {
			return true;
		}
		for (const n of el.querySelectorAll("*")) {
			if (n.shadowRoot || slot(n)) {
				return true;
			}
		}
		return false;
	};

	const inline = (el) => /^(inline|contents|ruby)/.test(getComputedStyle(el).display);

	// parent gives the element a node takes its style from in the flat tree:
	// the slot it is slotted into, its parent, or, at the top of a shadow
	// root, the host.
	const parent = (node) => node.assignedSlot ?? node.parentElement ?? node.parentNode?.host ?? null;

	// shows tells whether el is rendered. An element with display: contents,
	// as a slot has, has no box of its own but shows what it holds, unless
	// what holds it is not shown. An element out of the flat tree, as a child
	// of a host that no slot shows, has no display, and so no box.
	const shows = (el) => {
		const display = getComputedStyle(el).display;
		if (display === "none") {
			return false;
		}
		if (display !== "contents") {
			return box(el);
		}
		const p = parent(el);
		return p ? shows(p) : true;
	};

	// Text read from the flat tree is put together the way innerText puts
	// it, with characters of the private use area marking what tidy is to
	// leave or to make into line breaks: a space, a tab and a line break that
	// are kept as they are (U+E000 to U+E002), a line break of a <br>
	// (U+E004), and the line breaks around a block (U+E005) and around a
	// paragraph, which a blank line parts from what is around it (U+E006).
	// Those characters of the page itself are escaped first, with U+E003, so
	// that they come back as they were.
	const marks = "abcdefg";
	const esc = (s) => s.replace(/[\uE000-\uE006]/g, (c) => "\uE003" + marks[c.charCodeAt(0) - 0xE000]);
	const keep = (s) => esc(s).replace(/ /g, "\uE000").replace(/\t/g, "\uE001").replace(/\n/g, "\uE002");
	const unkeep = (s) => s.replace(/\uE000/g, " ").replace(/\uE001/g, "\t").replace(/[\uE002\uE004]/g, "\n")
		.replace(/\uE003([a-g])/g, (_, c) => String.fromCharCode(0xE000 + marks.indexOf(c)));

	// tidy puts a text read from the flat tree together: the spaces of the
	// texts run together and dropped at the ends of lines, the line breaks
	// around blocks run together, as many as the most any of them asks for,
	// and dropped at the ends, and what is kept left as it is.
	const tidy = (s) => unkeep(s
		.replace(/ +/g, " ")
		.replace(/ ?([\uE004-\uE006]) ?/g, "$1")
		.replace(/[\uE005\uE006]+/g, (run) => (run.includes("\uE006") ? "\n\n" : "\n"))
		.replace(/^\n+|\n+$/g, "")
		.replace(/^ | $/g, ""));

	// collapse gives how a style treats the white space of a text.
	const collapse = (style) => style.whiteSpaceCollapse ||
		{ pre: "preserve", "pre-wrap": "preserve", "break-spaces": "break-spaces", "pre-line": "preserve-breaks" }[style.whiteSpace] ||
		"collapse";

	// transformed gives s with the text-transform of style, as innerText
	// gives it. A word is taken to start where s does.
	const transformed = (s, style) => {
		switch (style.textTransform) {
		case "uppercase":
			return s.toUpperCase();
		case "lowercase":
			return s.toLowerCase();
		case "capitalize":
			return s.replace(/(^|[^\p{L}\p{N}'’])(\p{L})/gu, (_, before, letter) => before + letter.toUpperCase());
		default:
			return s;
		}
	};

	// spaced gives the text of a text node as its style shows it: spaces and
	// line breaks kept, line breaks alone kept, as pre-line does, or both run
	// together. Only the white space of CSS runs together, not a no-break
	// space.
	const spaced = (s, style) => {
		s = transformed(s, style);
		const mode = collapse(style);
		if (mode === "preserve" || mode === "break-spaces" || mode === "preserve-spaces") {
			return keep(s);
		}
		if (mode === "preserve-breaks") {
			return esc(s).replace(/[ \t\r\f]*\n[ \t\r\f]*/g, "\n").replace(/[ \t\r\f]+/g, " ").replace(/\n/g, "\uE002");
		}
		return esc(s).replace(/[ \t\n\r\f]+/g, " ");
	};

	// kept gives the innerText of an element to put among the texts around
	// it: kept as it is, but for the spaces at its ends, which run together
	// with those around it as they do on the screen, unless its style keeps
	// them.
	const kept = (t, style) => {
		if (collapse(style) !== "collapse") {
			return keep(t);
		}
		const [, before, middle, after] = /^( *)([\s\S]*?)( *)$/.exec(t);
		return before + keep(middle) + after;
	};

	// rendered gives the text of a node of the flat tree, marked for tidy:
	// an element's innerText when no shadow root or slot is under it, and
	// otherwise the text of the children of its shadow root, or of its own,
	// and of the nodes slotted into it, or a slot's own children where
	// nothing is.
	const rendered = (node) => {
		if (node.nodeType === Node.TEXT_NODE) {
			const p = parent(node);
			if (!p || !shows(p)) {
				return "";
			}
			const style = getComputedStyle(p);
			if (style.visibility !== "visible") {
				return "";
			}
			return spaced(node.nodeValue, style);
		}
		if (node.nodeType !== Node.ELEMENT_NODE || skipped.has(node.localName)) {
			return "";
		}
		if (node.localName === "br") {
			return getComputedStyle(node).display === "none" ? "" : "\uE004";
		}
		if (!shows(node)) {
			return "";
		}
		let t;
		if (slot(node)) {
			// A slot slotted into another is read as itself, so that its
			// display counts.
			const assigned = node.assignedNodes();
			t = Array.from(assigned.length > 0 ? assigned : node.childNodes, rendered).join("");
		} else if (!flat(node) && getComputedStyle(node).display !== "contents") {
			t = kept(leaf(node), getComputedStyle(node));
		} else {
			const children = node.shadowRoot ? node.shadowRoot.childNodes : node.childNodes;
			t = Array.from(children, rendered).join("");
		}
		// A paragraph is parted by a blank line whatever its display, as
		// innerText parts it.
		if (node.localName === "p" && node instanceof HTMLElement) {
			return "\uE006" + t + "\uE006";
		}
		return inline(node) ? t : "\uE005" + t + "\uE005";
	};

	// svgText gives the text an SVG shows: that of its text elements, as
	// their style shows it, without those not displayed, the parts hidden,
	// or its title.
	const svgText = (el) => {
		const parts = [];
		const walk = (node, into) => {
			if (node.nodeType === Node.TEXT_NODE) {
				const style = getComputedStyle(node.parentElement);
				if (into && style.visibility === "visible") {
					into.push(spaced(node.nodeValue, style));
				}
				return;
			}
			if (node.nodeType !== Node.ELEMENT_NODE || skipped.has(node.localName) || getComputedStyle(node).display === "none") {
				return;
			}
			if (!into && node.localName === "text") {
				const text = [];
				for (const child of node.childNodes) {
					walk(child, text);
				}
				const t = tidy(text.join(""));
				if (t) {
					parts.push(t);
				}
				return;
			}
			for (const child of node.childNodes) {
				walk(child, into);
			}
		};
		walk(el, null);
		return parts.join(" ");
	};

	// leaf gives the visible text of an element with no shadow root or slot
	// under it. An SVG has no innerText.
	const leaf = (el) => {
		if (el instanceof HTMLElement) {
			return el.innerText;
		}
		if (el instanceof SVGElement) {
			return svgText(el);
		}
		return el.textContent ?? "";
	};

	const text = (el) => {
		if (!shows(el)) {
			return "";
		}
		if (!flat(el) && getComputedStyle(el).display !== "contents") {
			return leaf(el);
		}
		return tidy(rendered(el));
	};

	// contains tells whether b is under a in the flat tree, across shadow
	// roots and into the slot b is slotted into.
	const contains = (a, b) => {
		for (let n = b; n; n = n.assignedSlot ?? n.parentNode ?? n.host) {
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
		// blocks, so it is compared without spaces. It holds nothing of a
		// shadow root, nor of what a slot shows, and the two need not come
		// in the order the page shows them, so an element of the flat tree
		// has its text read.
		const bare = needle.replace(/ /g, "");
		const content = (el) => flat(el) || el.textContent.replace(/\s+/g, "").includes(bare);
		// An element with no box, as a slot or another element with display:
		// contents, is not taken: an action can neither click it nor see it,
		// and the element around it shows the same text.
		const matched = found.filter((el) => !skipped.has(el.localName) && content(el) && box(el) && squash(text(el)).includes(needle));
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
