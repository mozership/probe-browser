package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/linyows/probe/mapping"
)

func TestTextMatch(t *testing.T) {
	testCases := []struct {
		name   string
		action ChromeDPAction
		text   string
		want   bool
	}{
		{"equals the whole text", ChromeDPAction{Equals: "Ready"}, "Ready", true},
		{"equals refuses a part", ChromeDPAction{Equals: "Ready"}, "Ready now", false},
		{"contains a part", ChromeDPAction{Contains: "Ready"}, "Ready now", true},
		{"contains refuses another text", ChromeDPAction{Contains: "Ready"}, "Loading", false},
		{"matches a pattern", ChromeDPAction{Matches: `^\d+ items$`}, "3 items", true},
		{"matches refuses another text", ChromeDPAction{Matches: `^\d+ items$`}, "no items", false},
		{"matches an empty text", ChromeDPAction{Matches: `^$`}, "", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := tc.action.textMatch()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := m.ok(tc.text); got != tc.want {
				t.Errorf("ok(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestTextMatch_None(t *testing.T) {
	m, err := (&ChromeDPAction{}).textMatch()
	if err != nil || m != nil {
		t.Errorf("got %v, %v, want no match and no error: wait_text then only waits to be visible", m, err)
	}
}

func TestTextMatch_Refused(t *testing.T) {
	testCases := []struct {
		name   string
		action ChromeDPAction
		want   string
	}{
		{"more than one", ChromeDPAction{Equals: "a", Contains: "a"}, "only one of equals, contains and matches"},
		{"a bad pattern", ChromeDPAction{Matches: "("}, "is not a regular expression"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.action.textMatch()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestPollText(t *testing.T) {
	t.Run("waits until the text is the one expected", func(t *testing.T) {
		texts := []string{"Loading", "Loading", "Ready"}
		calls := 0
		read := func(context.Context) (string, error) {
			text := texts[min(calls, len(texts)-1)]
			calls++
			return text, nil
		}

		got, err := pollText(context.Background(), "#status", &textMatch{equals: "Ready"}, read)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "Ready" || calls != 3 {
			t.Errorf("got %q after %d reads, want %q after 3", got, calls, "Ready")
		}
	})

	t.Run("gives the last text when time runs out", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		read := func(context.Context) (string, error) { return "Loading", nil }

		_, err := pollText(ctx, "#status", &textMatch{contains: "Ready"}, read)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want it to wrap the deadline", err)
		}
		want := `wait_text "#status": the text was "Loading", want it to contain "Ready"`
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to say %s", err, want)
		}
	})

	t.Run("says no element was visible when none was read", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		// Reading waits for the element, so it returns only when ctx ends.
		read := func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}

		_, err := pollText(ctx, "#status", &textMatch{equals: "Ready"}, read)
		want := `wait_text "#status": no visible element matched, want "Ready"`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to say %s", err, want)
		}
	})

	t.Run("reads again after an error", func(t *testing.T) {
		// The page rendered again and replaced the element between finding
		// it and reading it; the next read finds the new one.
		calls := 0
		read := func(context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "", errors.New("could not find node with given id")
			}
			return "Ready", nil
		}

		got, err := pollText(context.Background(), "#status", &textMatch{equals: "Ready"}, read)
		if err != nil || got != "Ready" {
			t.Errorf("got %q, %v, want %q", got, err, "Ready")
		}
	})

	t.Run("gives the last error when no text was read", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		read := func(context.Context) (string, error) {
			return "", errors.New("could not find node with given id")
		}

		_, err := pollText(ctx, "#status", &textMatch{equals: "Ready"}, read)
		want := `wait_text "#status": the text could not be read (could not find node with given id), want "Ready"`
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to say %s and wrap the deadline", err, want)
		}
	})
}

func TestParseData_EmptyTextFields(t *testing.T) {
	testCases := []struct {
		name    string
		action  map[string]any
		wantErr string
	}{
		{"equals \"\" waits for an empty text", map[string]any{"name": "wait_text", "selector": "#s", "equals": ""}, ""},
		{"contains \"\" is refused", map[string]any{"name": "wait_text", "selector": "#s", "contains": ""}, "contains is empty"},
		{"matches \"\" is refused", map[string]any{"name": "wait_text", "selector": "#s", "matches": ""}, "matches is empty"},
		{"equals \"\" with contains is more than one", map[string]any{"name": "wait_text", "selector": "#s", "equals": "", "contains": "a"}, "only one of"},
		{"equals \"\" on another action is refused", map[string]any{"name": "text", "selector": "#s", "equals": ""}, "text does not take equals"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := NewReq()
			if err := req.parseData(map[string]any{"actions": []any{tc.action}}, nil); err != nil {
				t.Fatalf("parseData failed: %v", err)
			}
			_, err := req.buildActionTasks()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				m, _ := req.Actions[0].textMatch()
				if m == nil || !m.ok("") || m.ok("Loading") {
					t.Errorf("match = %v, want one that accepts only an empty text", m)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to say %q", err, tc.wantErr)
			}
		})
	}
}

func TestWaitTextActionMapping(t *testing.T) {
	action := NewChromeDPAction()
	err := mapping.MapToStructByTags(map[string]any{
		"name":     "wait_text",
		"selector": "#status",
		"equals":   "Ready",
		"contains": "Rea",
		"matches":  "^R",
	}, action)
	if err != nil {
		t.Fatalf("MapToStructByTags failed: %v", err)
	}
	if action.Equals != "Ready" || action.Contains != "Rea" || action.Matches != "^R" {
		t.Errorf("got equals %q, contains %q, matches %q", action.Equals, action.Contains, action.Matches)
	}
}

func TestBuildActionTasks_TextMatch(t *testing.T) {
	testCases := []struct {
		name   string
		action ChromeDPAction
		want   string
	}{
		{"on another action", ChromeDPAction{Name: "text", Selector: "h1", Equals: "Hi"}, "text does not take equals, contains or matches"},
		{"a bad pattern", ChromeDPAction{Name: "wait_text", Selector: "h1", Matches: "("}, "is not a regular expression"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := NewReq()
			req.Actions = []*ChromeDPAction{&tc.action}
			_, err := req.buildActionTasks()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestBuildActionTasks_WaitText(t *testing.T) {
	testCases := []struct {
		name   string
		action ChromeDPAction
		tasks  int
	}{
		// Without an expected text it waits to be visible, then reads.
		{"without an expected text", ChromeDPAction{Name: "wait_text", Selector: "h1"}, 2},
		// With one, a single action waits and reads.
		{"with an expected text", ChromeDPAction{Name: "wait_text", Selector: "h1", Equals: "Hi"}, 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := NewReq()
			req.Actions = []*ChromeDPAction{&tc.action}
			tasks, err := req.buildActionTasks()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tasks) != tc.tasks {
				t.Errorf("got %d tasks, want %d", len(tasks), tc.tasks)
			}
			if tc.action.reText == nil {
				t.Error("the text should be kept for res.results")
			}
		})
	}
}
