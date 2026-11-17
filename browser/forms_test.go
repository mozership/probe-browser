package browser

import (
	"strings"
	"testing"
	"time"
)

func TestBuildActionTasks_FormsRefused(t *testing.T) {
	for _, tc := range []struct {
		action map[string]any
		want   string
	}{
		{map[string]any{"name": "locate"}, "locate needs locator"},
		{map[string]any{"name": "locate", "locator": map[string]any{"name": "Save"}}, "locate needs locator"},
		{map[string]any{"name": "locate", "locator": map[string]any{"role": "button", "name": "Save", "nth": 2, "of": 2}}, "locate: nth is 2, but it is 0 to 1"},
		{map[string]any{"name": "click", "selector": "b", "locator": map[string]any{"role": "button"}}, "click does not take locator"},
		{map[string]any{"name": "form_input", "value": "x"}, "form_input needs selector, has_text or ref"},
		{map[string]any{"name": "locator"}, "locator needs selector, has_text or ref, to find the element it acts on, or x and y"},
		{map[string]any{"name": "alerts", "x": 1, "y": 2}, "alerts does not take x or y"},
	} {
		req := NewReq()
		if err := req.parseData(map[string]any{"actions": []any{tc.action}}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := req.buildActionTasks(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error = %v, want it to say %q", tc.action, err, tc.want)
		}
	}
}

// TestLocate_AnotherNumberFailsAtOnce locates the second of two buttons on
// a page that has three: waiting would not tell them apart, so it fails at
// once, with how many the page has.
func TestLocate_AnotherNumberFailsAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	start := time.Now()
	_, err := Request(map[string]any{"headless": true, "timeout": "20s", "evidence_dir": t.TempDir(), "actions": []any{
		map[string]any{"name": "navigate", "url": "data:text/html,<button>Delete</button><button>Delete</button><button>Delete</button>"},
		map[string]any{"name": "locate", "locator": map[string]any{"role": "button", "name": "Delete", "nth": 1, "of": 2}},
	}})
	if err != nil && strings.Contains(err.Error(), "executable file not found") {
		t.Skipf("no Chrome to start: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), `locate button "Delete" (2 of 2): the page has 3 of them, where it had 2`) {
		t.Fatalf("err = %v, want the count told", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("it took %s, as if it waited for the timeout", d)
	}
}
