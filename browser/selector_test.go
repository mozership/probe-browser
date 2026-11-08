package browser

import (
	"strings"
	"testing"

	"github.com/mozership/probe/actionrpc"
)

func TestTarget_String(t *testing.T) {
	testCases := []struct {
		target target
		want   string
	}{
		{target{css: "#size"}, `"#size"`},
		{target{css: "button", text: "保存"}, `"button" with text "保存"`},
		{target{text: "保存"}, `with text "保存"`},
	}

	for _, tc := range testCases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.target.String(); got != tc.want {
				t.Errorf("String() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestTarget_QuotesItsArguments(t *testing.T) {
	// A selector and a text with quotes in them stay strings rather than
	// break the script.
	tg := target{css: `a[href='/x']`, text: `say "hi"`}
	want := `.all("a[href='/x']", "say \"hi\"")`
	if got := tg.all(); !strings.HasSuffix(got, want) {
		t.Errorf("all() ends with %q, want %q", got[max(0, len(got)-len(want)):], want)
	}
	if got := string(tg.path()); !strings.HasSuffix(got, want+"[0] ?? null)") {
		t.Errorf("path() = ...%q, want the first of all()", got[max(0, len(got)-40):])
	}
}

func TestCheckTarget(t *testing.T) {
	testCases := []struct {
		name   string
		action ChromeDPAction
		want   string
	}{
		{"selector", ChromeDPAction{Name: "click", Selector: "button"}, ""},
		{"has_text alone", ChromeDPAction{Name: "click", HasText: "保存"}, ""},
		{"both", ChromeDPAction{Name: "click", Selector: "button", HasText: "保存"}, ""},
		{"press on the focused element", ChromeDPAction{Name: "press", Value: "Enter"}, ""},
		{"press with has_text", ChromeDPAction{Name: "press", HasText: "検索", Value: "Enter"}, ""},
		{"no element", ChromeDPAction{Name: "click"}, "click needs selector or has_text"},
		{"no element to read", ChromeDPAction{Name: "wait_text"}, "wait_text needs selector or has_text"},
		{"has_text on navigate", ChromeDPAction{Name: "navigate", URL: "http://localhost", HasText: "x"}, "navigate does not take has_text"},
		{"has_text on get_cookie", ChromeDPAction{Name: "get_cookie", CookieName: "sid", HasText: "x"}, "get_cookie does not take has_text"},
		// wait_ready has always ignored selector, so it still does.
		{"selector on wait_ready", ChromeDPAction{Name: "wait_ready", Selector: "main"}, ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := NewReq()
			req.Actions = []*ChromeDPAction{&tc.action}
			_, err := req.buildActionTasks()
			if tc.want == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestBuildActionTasks_EveryElementActionTakesHasText(t *testing.T) {
	for name := range elementActions {
		t.Run(name, func(t *testing.T) {
			action := &ChromeDPAction{Name: name, HasText: "保存", Value: "x", Attribute: []string{"href"}}
			if name == "press" {
				action.Value = "Enter"
			}
			req := NewReq()
			req.Actions = []*ChromeDPAction{action}
			if _, err := req.buildActionTasks(); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestHasTextActionMapping(t *testing.T) {
	action := NewChromeDPAction()
	err := actionrpc.DecodeParams(map[string]any{"name": "click", "selector": "button", "has_text": "保存"}, action)
	if err != nil {
		t.Fatalf("MapToStructByTags failed: %v", err)
	}
	if got := action.target(); got != (target{css: "button", text: "保存"}) {
		t.Errorf("target = %v, want button with text 保存", got)
	}
}
