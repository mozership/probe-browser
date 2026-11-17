package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mozership/probe-browser/outline"
)

// Locator describes an element by its role and its accessible name, and,
// when other elements have both, by which of how many it is, as locator
// gives it and locate finds it again, on a page drawn anew.
type Locator struct {
	Role string `map:"role" json:"role"`
	Name string `map:"name" json:"name"`
	// Nth is which of Of elements of the role and the name it is, from 0.
	Nth int `map:"nth" json:"nth,omitempty"`
	Of  int `map:"of" json:"of,omitempty"`
}

func (l Locator) String() string {
	s := l.Role + " " + strconv.Quote(l.Name)
	if l.Of > 1 {
		s += fmt.Sprintf(" (%d of %d)", l.Nth+1, l.Of)
	}
	return s
}

// outlineCall is an expression that calls fn of the outline's script with
// args, which are expressions themselves, as an element.
func outlineCall(fn string, args ...string) string {
	return fmt.Sprintf("(() => { %s; return window.__probeOutline.%s(%s); })()", outline.Script, fn, strings.Join(args, ", "))
}

// formInput returns an action that sets the value of the form field of the
// target, once it is visible, as a user's input does, firing input and
// change, and keeps what it did in dst: chooses an option of a select by
// its value or its label, checks or unchecks a checkbox or a radio by
// true or false, and sets the value of an input or a textarea. A
// contenteditable element, as of a rich text editor, is typed into, as
// type does, through the editor, which keeps a state of its own that
// setting its text would leave behind; change follows, as for a field.
// An element the page takes out between the wait and the setting, as when
// it renders it anew, is waited for again.
func formInput(tg target, value string, dst *string) chromedp.Action[chromedp.Void] {
	// What it did names the field by its ref, or by its selector.
	name := tg.ref
	if name == "" {
		name = tg.String()
	}
	kind := chromedp.Evaluate[string](fmt.Sprintf(`((el) => !el ? "missing" :
		(el.isContentEditable && !["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName)) ? "editable" : "field")(%s)`, tg.first()))
	fill := chromedp.Evaluate[*string](fmt.Sprintf("(() => { %s; const el = %s; return el ? window.__probeOutline.fill(el, %s, %s) : null; })()",
		outline.Script, tg.first(), jsString(value), jsString(name)))
	typed := typeText(tg, value)
	change := chromedp.Evaluate[chromedp.Void](fmt.Sprintf(`((el) => { if (el) el.dispatchEvent(new Event("change", { bubbles: true })); })(%s)`, tg.first()))
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		for {
			if _, err := chromedp.WaitVisible(tg.path(), tg.opts()...)(ctx, t); err != nil {
				return chromedp.Void{}, err
			}
			k, err := kind(ctx, t)
			if err != nil {
				return chromedp.Void{}, fmt.Errorf("form_input %s: %w", tg, err)
			}
			switch k {
			case "missing":
				continue
			case "editable":
				if _, err := typed(ctx, t); err != nil {
					return chromedp.Void{}, fmt.Errorf("form_input %s: %w", tg, err)
				}
				if _, err := change(ctx, t); err != nil {
					return chromedp.Void{}, fmt.Errorf("form_input %s: %w", tg, err)
				}
				*dst = "Set the text of " + name + "."
				return chromedp.Void{}, nil
			}
			said, err := fill(ctx, t)
			if err != nil {
				return chromedp.Void{}, fmt.Errorf("form_input %s: %w", tg, err)
			}
			if said == nil {
				continue
			}
			*dst = *said
			return chromedp.Void{}, nil
		}
	}
}

// describe returns an action that keeps in dst the locator of the target,
// as JSON, or "" for a point with nothing one can act on, or an element it
// cannot describe, as one with no role, or one the page hides. An element
// the page takes out between the wait and the reading is waited for again.
func describe(tg target, dst *string) chromedp.Action[chromedp.Void] {
	expr := fmt.Sprintf("(() => { %s; const el = %s; return el ? { found: true, locator: window.__probeOutline.locatorOf(el) } : { found: false }; })()",
		outline.Script, tg.first())
	if tg.point != nil {
		expr = fmt.Sprintf("(() => { %s; return { found: true, locator: window.__probeOutline.locator(null, %v, %v) }; })()",
			outline.Script, tg.point.x, tg.point.y)
	}
	read := chromedp.Evaluate[struct {
		Found   bool     `json:"found"`
		Locator *Locator `json:"locator"`
	}](expr)
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		for {
			if tg.point == nil {
				if _, err := chromedp.WaitVisible(tg.path(), tg.opts()...)(ctx, t); err != nil {
					return chromedp.Void{}, err
				}
			}
			got, err := read(ctx, t)
			if err != nil {
				return chromedp.Void{}, fmt.Errorf("locator %s: %w", tg, err)
			}
			if !got.Found {
				continue
			}
			*dst = ""
			if got.Locator != nil {
				b, err := json.Marshal(got.Locator)
				if err != nil {
					return chromedp.Void{}, err
				}
				*dst = string(b)
			}
			return chromedp.Void{}, nil
		}
	}
}

// locatePoll is how often locate looks for the element again.
const locatePoll = 200 * time.Millisecond

// locate returns an action that keeps in dst the ref of the element l
// describes, waiting for it to appear, as on a page still being drawn. It
// fails at once when the page has another number of elements of the role
// and the name than l counted, which waiting does not tell apart.
func locate(l Locator, dst *string) chromedp.Action[chromedp.Void] {
	b, _ := json.Marshal(l)
	find := chromedp.Evaluate[struct {
		Ref   string `json:"ref"`
		Count int    `json:"count"`
	}](outlineCall("locate", string(b)))
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		for {
			found, err := find(ctx, t)
			switch {
			case err == nil && found.Ref != "":
				*dst = found.Ref
				return chromedp.Void{}, nil
			case err == nil && found.Count > 0:
				return chromedp.Void{}, fmt.Errorf("locate %s: the page has %d of them, where it had %d", l, found.Count, max(l.Of, 1))
			}
			select {
			case <-ctx.Done():
				if err != nil {
					return chromedp.Void{}, fmt.Errorf("locate %s: %w", l, err)
				}
				return chromedp.Void{}, fmt.Errorf("locate %s: the page has none: %w", l, ctx.Err())
			case <-time.After(locatePoll):
			}
		}
	}
}

// alerts returns an action that keeps in dst the texts of the alerts the
// page shows, those with role alert or alertdialog, one a line.
func alerts(dst *string) chromedp.Action[chromedp.Void] {
	read := chromedp.Evaluate[[]string](outlineCall("alerts"))
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		texts, err := read(ctx, t)
		if err != nil {
			return chromedp.Void{}, err
		}
		*dst = strings.Join(texts, "\n")
		return chromedp.Void{}, nil
	}
}

// checkLocate refuses a locator on an action that does not take it, and a
// locate without one.
func (a *ChromeDPAction) checkLocate() error {
	switch {
	case a.Name == "locate" && a.Locator.Role == "":
		return fmt.Errorf("locate needs locator, with the role and the name of the element, as locator gives them")
	case a.Name != "locate" && a.givesAny("locator"):
		return fmt.Errorf("%s does not take locator: only locate does", a.Name)
	case a.Name == "locate" && a.Locator.Of < 0:
		return fmt.Errorf("locate: of is %d, but it is how many elements have the role and the name, 1 or more", a.Locator.Of)
	case a.Name == "locate" && (a.Locator.Nth < 0 || a.Locator.Nth >= max(a.Locator.Of, 1)):
		return fmt.Errorf("locate: nth is %d, but it is 0 to %d, of the %d elements", a.Locator.Nth, max(a.Locator.Of, 1)-1, max(a.Locator.Of, 1))
	}
	return nil
}
