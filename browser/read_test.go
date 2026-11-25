package browser

import (
	"strings"
	"testing"

	"github.com/mozership/probe/sdk"
)

func TestBuildActionTasks_ReadPageRefused(t *testing.T) {
	testCases := []struct {
		name   string
		action ChromeDPAction
		want   string
	}{
		{"another filter", ChromeDPAction{Name: "read_page", Filter: "visible"}, `read_page: the filter is "visible"`},
		{"a filter on another action", ChromeDPAction{Name: "page_text", Filter: "all"}, "page_text does not take filter or depth"},
		{"a ref on an action on no element", ChromeDPAction{Name: "navigate", URL: "about:blank", Ref: "ref_abcd1"}, "navigate does not take ref"},
		{"a ref beside a selector", ChromeDPAction{Name: "click", Selector: "button", Ref: "ref_abcd1"}, "click takes ref, or selector and has_text, but not both"},
		{"a ref beside a text", ChromeDPAction{Name: "text", HasText: "Save", Ref: "ref_abcd1"}, "text takes ref, or selector and has_text, but not both"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := NewReq()
			req.Actions = []*ChromeDPAction{&tc.action}
			if _, err := req.buildActionTasks(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestReadPageActionMapping(t *testing.T) {
	action := NewChromeDPAction()
	err := sdk.DecodeParams(map[string]any{"name": "read_page", "id": "o", "filter": "interactive", "depth": 3, "ref": "ref_2"}, action)
	if err != nil {
		t.Fatal(err)
	}
	if action.Filter != "interactive" || action.Depth != 3 || action.Ref != "ref_2" {
		t.Errorf("got %+v", action)
	}
}

func TestCollectResults_ReadPage(t *testing.T) {
	req := NewReq()
	req.Actions = []*ChromeDPAction{{Name: "read_page", ID: "outline"}, {Name: "page_text"}}
	if _, err := req.buildActionTasks(); err != nil {
		t.Fatal(err)
	}
	*req.Actions[0].reText = `heading "Title" level=1 [ref_1]`
	*req.Actions[1].reText = "Title"
	results, _, err := req.collectResults()
	if err != nil || results["outline"] != `heading "Title" level=1 [ref_1]` || results["page_text"] != "Title" {
		t.Errorf("got %v, %v", results, err)
	}
}

func TestBuildActionTasks_ARefIsGuarded(t *testing.T) {
	req := NewReq()
	req.Actions = []*ChromeDPAction{{Name: "click", Ref: "ref_abcd1"}}
	tasks, err := req.buildActionTasks()
	if err != nil {
		t.Fatal(err)
	}
	// The click, guarded by the check of its ref.
	if len(tasks) != 1 {
		t.Errorf("%d tasks, want the click", len(tasks))
	}
	if got := req.Actions[0].target().String(); got != "ref ref_abcd1" {
		t.Errorf("the target is described as %q", got)
	}
}
