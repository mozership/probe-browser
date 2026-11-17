package browser

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// wheelNotch is how far a notch of the wheel scrolls, in CSS pixels, as a
// mouse's does in Chrome.
const wheelNotch = 100

// pointActions are the actions that take a point of the viewport, x and y,
// in place of an element, and act there with the mouse.
var pointActions = map[string]bool{
	"click": true, "double_click": true, "right_click": true, "hover": true, "wheel": true,
}

// mouseFields are the fields of how click presses the mouse button.
var mouseFields = []string{"button", "count", "modifiers"}

// point is a point of the viewport, in CSS pixels from its top left corner.
type point struct {
	x, y float64
}

// givesPoint tells whether the action was given x and y.
func (a *ChromeDPAction) givesPoint() bool {
	return slices.Contains(a.given, "x") && slices.Contains(a.given, "y")
}

// checkPointer refuses x and y, and how the mouse is pressed or turned, on
// an action that does not take them, and x without y.
func (a *ChromeDPAction) checkPointer() error {
	x, y := slices.Contains(a.given, "x"), slices.Contains(a.given, "y")
	switch {
	case (x || y) && !pointActions[a.Name]:
		return fmt.Errorf("%s does not take x or y: click, double_click, right_click, hover and wheel do", a.Name)
	case x != y:
		return fmt.Errorf("%s takes x and y together, the point of the viewport it acts at", a.Name)
	case (x || y) && (a.Selector != "" || a.HasText != "" || a.Ref != ""):
		return fmt.Errorf("%s takes x and y, or an element, but not both", a.Name)
	case x && (a.X < 0 || a.Y < 0):
		return fmt.Errorf("%s: the point %g, %g is not in the viewport, which x and y count from its top left corner", a.Name, a.X, a.Y)
	case slices.Contains(a.given, "count") && a.Count == 0:
		return fmt.Errorf("%s: the count is 0, but it is 1, 2 or 3", a.Name)
	}
	if a.Name != "click" {
		for _, f := range mouseFields {
			if slices.Contains(a.given, f) {
				return fmt.Errorf("%s does not take %s: only click does", a.Name, f)
			}
		}
	}
	if a.Name != "wheel" && (a.Direction != "" || a.Amount != 0) {
		return fmt.Errorf("%s does not take direction or amount: only wheel does", a.Name)
	}
	return nil
}

// pointScript scrolls the element into view and gives the middle of it, as
// the mouse goes to, or why it cannot.
const pointScript = `((el) => {
	if (!el) {
		return { error: "no element matched" };
	}
	// The scroll is instant, whatever scroll-behavior the page sets, so
	// that the box is measured where the element has come to.
	el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
	// The middle of the first box the element shows, as a link wrapped
	// over two lines has a box on each, and the middle of the two would
	// fall between them, on something else.
	const r = Array.from(el.getClientRects()).find((b) => b.width > 0 && b.height > 0);
	if (!r) {
		return { error: "the element has no box to point at" };
	}
	return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
})(%s)`

// pointOf returns an action that gives where the mouse acts on the target:
// its point, or the middle of its element, once the element is visible,
// scrolled into view.
func pointOf(tg target) func(ctx context.Context, t *chromedp.Target) (point, error) {
	script := chromedp.Evaluate[struct {
		X, Y  float64
		Error string
	}](fmt.Sprintf(pointScript, tg.first()))
	return func(ctx context.Context, t *chromedp.Target) (point, error) {
		if tg.point != nil {
			return *tg.point, nil
		}
		if _, err := chromedp.WaitVisible(tg.path(), tg.opts()...)(ctx, t); err != nil {
			return point{}, err
		}
		p, err := script(ctx, t)
		if err != nil {
			return point{}, err
		}
		if p.Error != "" {
			return point{}, fmt.Errorf("%s: %s", tg, p.Error)
		}
		return point{p.X, p.Y}, nil
	}
}

// mouseButtons are the buttons click presses.
var mouseButtons = []string{"left", "middle", "right"}

// mouseClick returns an action that clicks the target with the mouse
// button, count times, holding the modifiers: a double click is a click and
// a click of count 2, as a mouse sends it.
func mouseClick(tg target, button string, count int, modifiers []string) (chromedp.Action[chromedp.Void], error) {
	if button == "" {
		button = "left"
	}
	if !slices.Contains(mouseButtons, button) {
		return nil, fmt.Errorf("click: the button is %q, but it is left, middle or right", button)
	}
	if count == 0 {
		count = 1
	}
	if count < 1 || count > 3 {
		return nil, fmt.Errorf("click: the count is %d, but it is 1, 2 or 3", count)
	}
	mods, err := parseModifiers(modifiers)
	if err != nil {
		return nil, fmt.Errorf("click: %w", err)
	}
	at := pointOf(tg)
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		p, err := at(ctx, t)
		if err != nil {
			return chromedp.Void{}, err
		}
		for i := 1; i <= count; i++ {
			click := chromedp.MouseClickXY(p.x, p.y, chromedp.Button(button), chromedp.ClickCount(i), chromedp.ButtonModifiers(mods...))
			if _, err := click(ctx, t); err != nil {
				return chromedp.Void{}, err
			}
		}
		return chromedp.Void{}, nil
	}, nil
}

// mouseMove returns an action that moves the mouse onto the target.
func mouseMove(tg target) chromedp.Action[chromedp.Void] {
	at := pointOf(tg)
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		p, err := at(ctx, t)
		if err != nil {
			return chromedp.Void{}, err
		}
		return chromedp.MouseEvent(input.DispatchMouseEventTypeMouseMoved, p.x, p.y)(ctx, t)
	}
}

// wheel returns an action that turns the mouse wheel on the target, amount
// notches toward direction, 3 when amount is 0.
func wheel(tg target, direction string, amount int) (chromedp.Action[chromedp.Void], error) {
	if amount == 0 {
		amount = 3
	}
	if amount < 1 {
		return nil, fmt.Errorf("wheel: the amount is %d, but it is a number of notches, 1 or more", amount)
	}
	d := float64(amount * wheelNotch)
	var dx, dy float64
	switch direction {
	case "up":
		dy = -d
	case "down":
		dy = d
	case "left":
		dx = -d
	case "right":
		dx = d
	default:
		return nil, fmt.Errorf("wheel: the direction is %q, but it is up, down, left or right", direction)
	}
	at := pointOf(tg)
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		p, err := at(ctx, t)
		if err != nil {
			return chromedp.Void{}, err
		}
		_, err = cdp.Call(ctx, t, dispatchWheel, wheelParams{
			Type: input.DispatchMouseEventTypeMouseWheel, X: p.x, Y: p.y, DeltaX: dx, DeltaY: dy,
		})
		return chromedp.Void{}, err
	}, nil
}

// wheelParams are those of Input.dispatchMouseEvent for a wheel. Chrome
// wants both deltas of a wheel, and cdproto leaves out one of 0.
type wheelParams struct {
	Type   input.DispatchMouseEventType `json:"type"`
	X      float64                      `json:"x"`
	Y      float64                      `json:"y"`
	DeltaX float64                      `json:"deltaX"`
	DeltaY float64                      `json:"deltaY"`
}

var dispatchWheel = cdp.Command[wheelParams, cdp.Empty]{Method: input.CommandDispatchMouseEvent}

// modifierAliases are other names of the modifiers, as a person or a model
// writes them, beside those of KeyboardEvent.
var modifierAliases = map[string]chromedp.Modifier{
	"alt": chromedp.ModifierAlt, "option": chromedp.ModifierAlt,
	"control": chromedp.ModifierCtrl, "ctrl": chromedp.ModifierCtrl,
	"meta": chromedp.ModifierMeta, "cmd": chromedp.ModifierMeta, "command": chromedp.ModifierMeta,
	"shift": chromedp.ModifierShift,
}

// modifierOf reads a modifier by its name, as Control, or as ctrl, the case
// aside.
func modifierOf(name string) (chromedp.Modifier, bool) {
	m, ok := modifierAliases[strings.ToLower(name)]
	return m, ok
}

func parseModifiers(names []string) ([]chromedp.Modifier, error) {
	var mods []chromedp.Modifier
	for _, n := range names {
		m, ok := modifierOf(n)
		if !ok {
			return nil, fmt.Errorf("%q is not a modifier: use Alt, Control, Meta or Shift", n)
		}
		mods = append(mods, m)
	}
	return mods, nil
}

// pointerFieldKeys are the fields of the mouse whose zero value a workflow
// can mean, as x: 0, so that whether it wrote them is kept.
var pointerFieldKeys = []string{"x", "y", "button", "count", "modifiers"}

// markGivenPointerFields records which of the fields of the mouse each
// action of data was written with.
func markGivenPointerFields(data map[string]any, actions []*ChromeDPAction) {
	raw, ok := data["actions"].([]any)
	if !ok {
		return
	}
	for i, a := range raw {
		action, ok := a.(map[string]any)
		if !ok || i >= len(actions) {
			continue
		}
		for _, key := range pointerFieldKeys {
			if _, written := action[key]; written {
				actions[i].given = append(actions[i].given, key)
			}
		}
	}
}

// givesAny tells whether the action was written with any of the fields.
func (a *ChromeDPAction) givesAny(fields ...string) bool {
	for _, f := range fields {
		if slices.Contains(a.given, f) {
			return true
		}
	}
	return false
}
