package browser

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// notVisiblePollInterval is how often wait_not_visible looks at the page
// again while an element is still visible.
const notVisiblePollInterval = 100 * time.Millisecond

// typeTarget is what type does with the element it types into.
const (
	// typeField is an input or a textarea, whose value is cleared.
	typeField = "field"
	// typeEditable is a contenteditable element, such as a rich text
	// editor, whose content is selected, so that the keys replace it.
	typeEditable = "editable"
	// typeEditableEmpty is a contenteditable element with no text, where
	// there is nothing to replace.
	typeEditableEmpty = "editable-empty"
)

// typePrepareScript readies the element type types into, and returns which
// of the typeTarget kinds it is, or why it cannot be typed into. The content
// of a contenteditable element is selected rather than removed, as a user's
// Ctrl+A does: an editor such as Lexical keeps its own state, which taking
// the nodes out from under it would break, and typing over a selection goes
// through the editor.
const typePrepareScript = `((sel) => {
	const el = document.querySelector(sel);
	if (!el) {
		return "error:no element matched";
	}
	if (el.tagName === "INPUT" || el.tagName === "TEXTAREA") {
		return "field";
	}
	if (!el.isContentEditable) {
		return "error:the element is a <" + el.tagName.toLowerCase() + ">, which is neither a field nor contenteditable";
	}
	el.focus();
	const range = document.createRange();
	range.selectNodeContents(el);
	const selection = window.getSelection();
	selection.removeAllRanges();
	selection.addRange(range);
	return el.textContent === "" ? "editable-empty" : "editable";
})(%s)`

// typeText returns an action that waits until sel is visible, then replaces
// what it holds with value: the value of an input or a textarea, or the
// content of a contenteditable element.
func typeText(sel, value string) chromedp.Action[chromedp.Void] {
	script := fmt.Sprintf(typePrepareScript, jsString(sel))
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		if _, err := chromedp.WaitVisible(chromedp.CSS(sel))(ctx, t); err != nil {
			return chromedp.Void{}, err
		}
		kind, err := chromedp.Evaluate[string](script)(ctx, t)
		if err != nil {
			return chromedp.Void{}, err
		}
		switch kind {
		case typeField:
			if _, err := chromedp.Clear(sel)(ctx, t); err != nil {
				return chromedp.Void{}, err
			}
			return chromedp.SendKeys(sel, value, chromedp.NodeVisible)(ctx, t)
		case typeEditable, typeEditableEmpty:
			// The element is focused and its content selected, so the keys go
			// to it as they are; SendKeys would focus it again and could lose
			// the selection.
			keys := value
			if keys == "" && kind == typeEditable {
				keys = kb.Backspace
			}
			return chromedp.KeyEvent(keys)(ctx, t)
		default:
			return chromedp.Void{}, fmt.Errorf("type %q: %s", sel, strings.TrimPrefix(kind, "error:"))
		}
	}
}

// keyModifiers are the modifiers a press can hold, by the names it takes.
var keyModifiers = map[string]chromedp.Modifier{
	"Alt":     chromedp.ModifierAlt,
	"Control": chromedp.ModifierCtrl,
	"Meta":    chromedp.ModifierMeta,
	"Shift":   chromedp.ModifierShift,
}

// keyNames are the keys a press takes by name, such as Enter, with the
// character kb encodes each of them by. A key whose name is one character,
// such as a or " ", is given as that character instead, though Space is
// taken too, as " " is hard to read in a workflow.
var keyNames = func() map[string]rune {
	names := map[string]rune{"Space": ' '}
	for r, k := range kb.Keys {
		if len([]rune(k.Key)) > 1 {
			names[k.Key] = r
		}
	}
	return names
}()

// parseKey reads a key press, as Enter, a or Shift+Tab: modifiers joined by
// "+" to the key, which is a key name of KeyboardEvent.key, such as Enter,
// Escape or ArrowDown, or a single character.
func parseKey(s string) (rune, []chromedp.Modifier, error) {
	if s == "" {
		return 0, nil, fmt.Errorf("press needs value, the key to press, such as Enter")
	}
	// "+" alone, and a "+" after the last separator, as in Control++, is
	// the key itself.
	parts := strings.Split(s, "+")
	name := parts[len(parts)-1]
	mods := parts[:len(parts)-1]
	if name == "" && len(parts) >= 2 && parts[len(parts)-2] == "" {
		name, mods = "+", parts[:len(parts)-2]
	}

	var modifiers []chromedp.Modifier
	for _, m := range mods {
		mod, ok := keyModifiers[m]
		if !ok {
			return 0, nil, fmt.Errorf("press %q: %q is not a modifier: use Alt, Control, Meta or Shift", s, m)
		}
		modifiers = append(modifiers, mod)
	}

	if r := []rune(name); len(r) == 1 {
		return r[0], modifiers, nil
	}
	if r, ok := keyNames[name]; ok {
		return r, modifiers, nil
	}
	return 0, nil, fmt.Errorf("press %q: %q is not a key: give a key name, such as Enter, Tab, Escape or ArrowDown, or a single character", s, name)
}

// pressKey returns an action that presses key: on sel, once it is visible
// and focused, or on the element that has the focus when sel is empty.
func pressKey(sel, key string) (chromedp.Action[chromedp.Void], error) {
	r, modifiers, err := parseKey(key)
	if err != nil {
		return nil, err
	}
	press := chromedp.KeyEvent(string(r), chromedp.KeyModifiers(modifiers...))
	if sel == "" {
		return press, nil
	}
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		if _, err := chromedp.Focus(sel, chromedp.NodeVisible)(ctx, t); err != nil {
			return chromedp.Void{}, err
		}
		return press(ctx, t)
	}, nil
}

// setValueScript sets the value of a field and fires input and change, as a
// user's edit does. The value is set through the setter of the element's
// prototype, since React tracks the value the instance's own property was
// last given, and drops an input event for a value set that way. It returns
// why it could not, or "" when it did.
const setValueScript = `((sel, value) => {
	const el = document.querySelector(sel);
	if (!el) {
		return "no element matched";
	}
	const protos = [HTMLInputElement, HTMLTextAreaElement, HTMLSelectElement];
	const proto = protos.find(p => el instanceof p);
	if (!proto) {
		return "the element is a <" + el.tagName.toLowerCase() + ">, not an <input>, a <textarea> or a <select>";
	}
	if (el.matches(":disabled")) {
		return "the element is disabled";
	}
	Object.getOwnPropertyDescriptor(proto.prototype, "value").set.call(el, value);
	// A value the field does not take, such as 2000-13-01 of a date, leaves
	// it empty rather than failing, so it is checked here.
	if (el.value !== value) {
		return "the element did not take the value, and holds " + JSON.stringify(el.value);
	}
	el.dispatchEvent(new Event("input", { bubbles: true }));
	el.dispatchEvent(new Event("change", { bubbles: true }));
	return "";
})(%s, %s)`

// setValue returns an action that waits until sel is visible, then sets its
// value to value.
func setValue(sel, value string) chromedp.Action[chromedp.Void] {
	script := fmt.Sprintf(setValueScript, jsString(sel), jsString(value))
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		if _, err := chromedp.WaitVisible(chromedp.CSS(sel))(ctx, t); err != nil {
			return chromedp.Void{}, err
		}
		reason, err := chromedp.Evaluate[string](script)(ctx, t)
		if err != nil {
			return chromedp.Void{}, err
		}
		if reason != "" {
			return chromedp.Void{}, fmt.Errorf("set_value %q: %s", sel, reason)
		}
		return chromedp.Void{}, nil
	}
}

// visibleCountScript counts the elements matching a selector that are
// visible, by the test chromedp's wait_visible makes: an element with a
// size or a box.
const visibleCountScript = `((sel) => Array.from(document.querySelectorAll(sel)).filter(el =>
	Boolean(el.offsetWidth || el.offsetHeight || el.getClientRects().length)
).length)(%s)`

// waitNotVisible returns an action that waits until no element matching sel
// is visible. An element taken out of the page is not visible, so it does
// not wait for one to be there, as chromedp's WaitNotVisible does.
func waitNotVisible(sel string) chromedp.Action[chromedp.Void] {
	script := fmt.Sprintf(visibleCountScript, jsString(sel))
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		return chromedp.Void{}, pollNotVisible(ctx, sel, func(ctx context.Context) (int, error) {
			return chromedp.Evaluate[int](script)(ctx, t)
		})
	}
}

// pollNotVisible calls count until it returns 0, or ctx ends. An error from
// count fails at once, since it comes of a selector the page cannot read,
// which reading again would not mend.
func pollNotVisible(ctx context.Context, sel string, count func(context.Context) (int, error)) error {
	last := 0
	for {
		n, err := count(ctx)
		switch {
		case err == nil && n == 0:
			return nil
		case err == nil:
			last = n
		case ctx.Err() == nil:
			return fmt.Errorf("wait_not_visible %q: %w", sel, err)
		}

		select {
		case <-ctx.Done():
			switch last {
			case 0:
				return fmt.Errorf("wait_not_visible %q: %w", sel, ctx.Err())
			case 1:
				return fmt.Errorf("wait_not_visible %q: an element is still visible: %w", sel, ctx.Err())
			default:
				return fmt.Errorf("wait_not_visible %q: %d elements are still visible: %w", sel, last, ctx.Err())
			}
		case <-time.After(notVisiblePollInterval):
		}
	}
}
