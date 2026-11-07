package browser

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestParseKey(t *testing.T) {
	testCases := []struct {
		key       string
		want      rune
		modifiers []chromedp.Modifier
	}{
		{"Enter", '\r', nil},
		{"Tab", '\t', nil},
		{"Escape", '\u001b', nil},
		{"Backspace", '\b', nil},
		{"ArrowDown", '́', nil},
		{"Space", ' ', nil},
		{" ", ' ', nil},
		{"a", 'a', nil},
		{"あ", 'あ', nil},
		{"+", '+', nil},
		{"Shift+Tab", '\t', []chromedp.Modifier{chromedp.ModifierShift}},
		{"Control+a", 'a', []chromedp.Modifier{chromedp.ModifierCtrl}},
		{"Meta+Enter", '\r', []chromedp.Modifier{chromedp.ModifierMeta}},
		{"Control+Alt+Delete", '\u007f', []chromedp.Modifier{chromedp.ModifierCtrl, chromedp.ModifierAlt}},
		{"Control++", '+', []chromedp.Modifier{chromedp.ModifierCtrl}},
	}

	for _, tc := range testCases {
		t.Run(tc.key, func(t *testing.T) {
			got, modifiers, err := parseKey(tc.key)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("key = %q, want %q", got, tc.want)
			}
			if !slices.Equal(modifiers, tc.modifiers) {
				t.Errorf("modifiers = %v, want %v", modifiers, tc.modifiers)
			}
		})
	}
}

func TestParseKey_Refused(t *testing.T) {
	testCases := []struct {
		key  string
		want string
	}{
		{"", "press needs value"},
		{"Return", `"Return" is not a key`},
		{"Ctrl+a", `"Ctrl" is not a modifier`},
		{"Shift+", `"" is not a key`},
		{`\r`, `"\\r" is not a key`},
	}

	for _, tc := range testCases {
		t.Run(tc.key, func(t *testing.T) {
			_, _, err := parseKey(tc.key)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestBuildActionTasks_Input(t *testing.T) {
	testCases := []struct {
		name   string
		action ChromeDPAction
	}{
		{"type", ChromeDPAction{Name: "type", Selector: "#editor", Value: "x"}},
		{"send_keys", ChromeDPAction{Name: "send_keys", Selector: "#q", Value: "x"}},
		{"press on the focused element", ChromeDPAction{Name: "press", Value: "Enter"}},
		{"press on an element", ChromeDPAction{Name: "press", Selector: "#q", Value: "Shift+Tab"}},
		{"set_value", ChromeDPAction{Name: "set_value", Selector: "#date", Value: "2000-01-02"}},
		{"wait_not_visible", ChromeDPAction{Name: "wait_not_visible", Selector: "#panel"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := NewReq()
			req.Actions = []*ChromeDPAction{&tc.action}
			tasks, err := req.buildActionTasks()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// One action waits for the element, if any, and does the input.
			if len(tasks) != 1 {
				t.Errorf("got %d tasks, want 1", len(tasks))
			}
		})
	}
}

func TestBuildActionTasks_PressRefusedBeforeTheBrowser(t *testing.T) {
	req := NewReq()
	req.Actions = []*ChromeDPAction{{Name: "press", Value: "Return"}}
	if _, err := req.buildActionTasks(); err == nil || !strings.Contains(err.Error(), `"Return" is not a key`) {
		t.Errorf("got %v, want the key refused", err)
	}
}

func TestInputScripts_QuoteTheirArguments(t *testing.T) {
	// A selector and a value with quotes in them stay strings rather than
	// break the script.
	sel, value := jsString(`input[name='d']`), jsString(`10" wide`)
	testCases := []struct {
		name   string
		script string
		want   string
	}{
		{"type", fmt.Sprintf(typePrepareScript, sel), `})("input[name='d']")`},
		{"set_value", fmt.Sprintf(setValueScript, sel, value), `})("input[name='d']", "10\" wide")`},
		{"wait_not_visible", fmt.Sprintf(visibleCountScript, sel), `).length)("input[name='d']")`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.HasSuffix(tc.script, tc.want) {
				t.Errorf("script ends with %q, want %q", tc.script[max(0, len(tc.script)-len(tc.want)):], tc.want)
			}
		})
	}
}

func TestPollNotVisible(t *testing.T) {
	counts := []int{2, 1, 0}
	calls := 0
	err := pollNotVisible(context.Background(), "#panel", func(context.Context) (int, error) {
		n := counts[calls]
		calls++
		return n, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Errorf("counted %d times, want 3: until none was visible", calls)
	}
}

func TestPollNotVisible_NoneAtFirst(t *testing.T) {
	// An element that is not in the page is not visible, so there is
	// nothing to wait for.
	calls := 0
	err := pollNotVisible(context.Background(), "#gone", func(context.Context) (int, error) {
		calls++
		return 0, nil
	})
	if err != nil || calls != 1 {
		t.Errorf("got %v after %d counts, want no error after 1", err, calls)
	}
}

func TestPollNotVisible_StillVisible(t *testing.T) {
	testCases := []struct {
		count int
		want  string
	}{
		{1, `wait_not_visible "h1": an element is still visible: context deadline exceeded`},
		{3, `wait_not_visible "h1": 3 elements are still visible: context deadline exceeded`},
	}

	for _, tc := range testCases {
		t.Run(tc.want, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*notVisiblePollInterval)
			defer cancel()
			err := pollNotVisible(ctx, "h1", func(context.Context) (int, error) { return tc.count, nil })
			if err == nil || err.Error() != tc.want {
				t.Errorf("got %v, want %q", err, tc.want)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("got %v, want it to wrap the deadline", err)
			}
		})
	}
}

func TestPollNotVisible_ErrorFailsAtOnce(t *testing.T) {
	// A selector the page cannot read gives the same error every time.
	calls := 0
	start := time.Now()
	err := pollNotVisible(context.Background(), "[", func(context.Context) (int, error) {
		calls++
		return 0, errors.New("SyntaxError: '[' is not a valid selector")
	})
	if err == nil || !strings.Contains(err.Error(), `wait_not_visible "[": SyntaxError`) {
		t.Errorf("got %v, want the page's error", err)
	}
	if calls != 1 || time.Since(start) >= notVisiblePollInterval {
		t.Errorf("counted %d times in %v, want once and no wait", calls, time.Since(start))
	}
}

func TestPollNotVisible_DeadlineBeforeAnyCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := pollNotVisible(ctx, "#panel", func(ctx context.Context) (int, error) { return 0, ctx.Err() })
	if err == nil || err.Error() != `wait_not_visible "#panel": context canceled` {
		t.Errorf("got %v, want the context's error", err)
	}
}
