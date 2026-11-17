package browser

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestBuildActionTasks_PointerRefused(t *testing.T) {
	testCases := []struct {
		name   string
		action map[string]any
		want   string
	}{
		{"x on an action of no point", map[string]any{"name": "text", "selector": "h1", "x": 1, "y": 2}, "text does not take x or y"},
		{"x without y", map[string]any{"name": "click", "x": 1}, "click takes x and y together"},
		{"a point beside an element", map[string]any{"name": "click", "selector": "h1", "x": 1, "y": 2}, "click takes x and y, or an element, but not both"},
		{"a button on another action", map[string]any{"name": "hover", "x": 1, "y": 2, "button": "right"}, "hover does not take button: only click does"},
		{"another button", map[string]any{"name": "click", "x": 1, "y": 2, "button": "back"}, `click: the button is "back"`},
		{"a count of 4", map[string]any{"name": "click", "x": 1, "y": 2, "count": 4}, "click: the count is 4"},
		{"a count of 0", map[string]any{"name": "click", "x": 1, "y": 2, "count": 0}, "click: the count is 0"},
		{"a point out of the viewport", map[string]any{"name": "hover", "x": -1, "y": 2}, "hover: the point -1, 2 is not in the viewport"},
		{"another modifier", map[string]any{"name": "click", "x": 1, "y": 2, "modifiers": "Hyper"}, `click: "Hyper" is not a modifier`},
		{"a wheel with no direction", map[string]any{"name": "wheel", "x": 1, "y": 2}, `wheel: the direction is ""`},
		{"a wheel with no point", map[string]any{"name": "wheel", "direction": "down"}, "wheel needs selector, has_text or ref, to find the element it acts on, or x and y"},
		{"a direction on another action", map[string]any{"name": "click", "x": 1, "y": 2, "direction": "down"}, "click does not take direction or amount"},
		{"repeat on another action", map[string]any{"name": "type", "value": "a", "repeat": 2}, "type does not take repeat"},
		{"repeat of 0 on another action", map[string]any{"name": "type", "value": "a", "repeat": 0}, "type does not take repeat"},
		{"an empty direction on another action", map[string]any{"name": "click", "x": 1, "y": 2, "direction": ""}, "click does not take direction or amount"},
		{"an amount of 0", map[string]any{"name": "wheel", "x": 1, "y": 2, "direction": "up", "amount": 0}, "wheel: the amount is 0"},
		{"a repeat of 0", map[string]any{"name": "press", "value": "Enter", "repeat": 0}, "press: repeat is 0"},
		{"a key that is none", map[string]any{"name": "press", "value": "Control+a Nope"}, `"Nope" is not a key`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := NewReq()
			if err := req.parseData(map[string]any{"actions": []any{tc.action}}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := req.buildActionTasks(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestBuildActionTasks_PointerTaken(t *testing.T) {
	for _, action := range []map[string]any{
		{"name": "click", "x": 0, "y": 0},
		{"name": "click", "ref": "ref_abcd1", "button": "middle", "count": 3, "modifiers": []any{"ctrl", "Shift"}},
		{"name": "double_click", "x": 1.5, "y": 2},
		{"name": "right_click", "x": 1, "y": 2},
		{"name": "hover", "x": 1, "y": 2},
		{"name": "wheel", "x": 1, "y": 2, "direction": "left", "amount": 1},
		{"name": "wheel", "selector": "#list", "direction": "down"},
		{"name": "type", "value": "typed into the focus"},
		{"name": "press", "value": "Control+a Backspace", "repeat": 3},
		{"name": "press", "value": " "},
		{"name": "press", "value": "Control+ "},
	} {
		req := NewReq()
		if err := req.parseData(map[string]any{"actions": []any{action}}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := req.buildActionTasks(); err != nil {
			t.Errorf("%v: %v", action, err)
		}
	}
	// A point of 0, 0 is the top left corner, not a point left out.
	req := NewReq()
	if err := req.parseData(map[string]any{"actions": []any{map[string]any{"name": "click", "x": 0, "y": 0}}}, nil); err != nil {
		t.Fatal(err)
	}
	if tg := req.Actions[0].target(); tg.point == nil || tg.String() != "the point 0, 0" {
		t.Errorf("the target is %s", tg)
	}
}

// TestPointScript_KeepsAShownWrapperOfASlot measures, keeping it where it
// is, a wrapper in a shadow root whose middle shows a node slotted into it.
func TestPointScript_KeepsAShownWrapperOfASlot(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.NoSandbox, chromedp.Flag("headless", true))
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	if err := chromedp.Do(ctx); err != nil {
		t.Skipf("no Chrome to start: %v", err)
	}
	ctx, tcancel := context.WithTimeout(ctx, 20*time.Second)
	defer tcancel()
	page := `<div style="height: 300px"></div><x-card><p style="margin: 0; height: 100px">Slotted</p></x-card><div style="height: 3000px"></div>
<script>customElements.define('x-card', class extends HTMLElement {
  constructor() { super(); this.attachShadow({mode: 'open'}).innerHTML = '<div id="wrap"><slot></slot></div>'; }
});</script>`
	if err := chromedp.Do(ctx, chromedp.Navigate("data:text/html,"+url.PathEscape(page))); err != nil {
		t.Fatal(err)
	}
	wrap := `document.querySelector("x-card").shadowRoot.querySelector("#wrap")`
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[chromedp.Void](fmt.Sprintf(pointScript, wrap, true))); err != nil {
		t.Fatal(err)
	}
	if y, err := chromedp.Run(ctx, chromedp.Evaluate[float64]("scrollY")); err != nil || y != 0 {
		t.Errorf("the page scrolled to %g, %v; want the wrapper shown left where it is", y, err)
	}
}
