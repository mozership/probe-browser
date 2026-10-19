package browser

import (
	"context"
	"fmt"

	"github.com/chromedp/chromedp"
)

// selectScript chooses the option of a <select> whose value, or else whose
// label, is the one given, and fires input and change as a user's choice
// does, so that the page's handlers run. It returns why it could not, or ""
// when it did. It is wrapped in a function, as the scripts of hover and
// right_click are, so that nothing is left in the page's global scope.
const selectScript = `((sel, want) => {
	const el = document.querySelector(sel);
	if (!el) {
		return "no element matched";
	}
	if (el.tagName !== "SELECT") {
		return "the element is a <" + el.tagName.toLowerCase() + ">, not a <select>: use type to fill a field";
	}
	// :disabled, unlike the disabled property, also holds for a <select> in
	// a <fieldset disabled> and an option in an <optgroup disabled>.
	if (el.matches(":disabled")) {
		return "the <select> is disabled";
	}
	const options = Array.from(el.options);
	const option = options.find(o => o.value === want) ?? options.find(o => o.label === want);
	if (!option) {
		return "no option has the value or label " + JSON.stringify(want) + "; the values are " + JSON.stringify(options.map(o => o.value));
	}
	if (option.matches(":disabled")) {
		return "the option " + JSON.stringify(want) + " is disabled";
	}
	option.selected = true;
	el.dispatchEvent(new Event("input", { bubbles: true }));
	el.dispatchEvent(new Event("change", { bubbles: true }));
	return "";
})(%s, %s)`

// selectOption returns an action that waits until sel is visible, then
// chooses the option value names in it.
func selectOption(sel, value string) chromedp.Action[chromedp.Void] {
	script := fmt.Sprintf(selectScript, jsString(sel), jsString(value))
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		if _, err := chromedp.WaitVisible(chromedp.CSS(sel))(ctx, t); err != nil {
			return chromedp.Void{}, err
		}
		reason, err := chromedp.Evaluate[string](script)(ctx, t)
		if err != nil {
			return chromedp.Void{}, err
		}
		if reason != "" {
			return chromedp.Void{}, fmt.Errorf("select %q: %s", sel, reason)
		}
		return chromedp.Void{}, nil
	}
}
