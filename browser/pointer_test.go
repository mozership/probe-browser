package browser

import (
	"strings"
	"testing"
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
		{"another modifier", map[string]any{"name": "click", "x": 1, "y": 2, "modifiers": "Hyper"}, `click: "Hyper" is not a modifier`},
		{"a wheel with no direction", map[string]any{"name": "wheel", "x": 1, "y": 2}, `wheel: the direction is ""`},
		{"a wheel with no point", map[string]any{"name": "wheel", "direction": "down"}, "wheel needs selector, has_text or ref, to find the element it acts on, or x and y"},
		{"a direction on another action", map[string]any{"name": "click", "x": 1, "y": 2, "direction": "down"}, "click does not take direction or amount"},
		{"repeat on another action", map[string]any{"name": "type", "value": "a", "repeat": 2}, "type does not take repeat"},
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
