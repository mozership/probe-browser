package browser

import (
	"fmt"
	"strings"
	"testing"
)

func TestSelectScript_QuotesItsArguments(t *testing.T) {
	// A selector and a value with quotes in them stay strings rather than
	// break the script, as they did when pasted between quotes.
	script := fmt.Sprintf(selectScript, jsString(`select[name='size']`), jsString(`10" wide`))
	want := `})("select[name='size']", "10\" wide")`
	if !strings.HasSuffix(script, want) {
		t.Errorf("script ends with %q, want %q", script[len(script)-len(want):], want)
	}
}

func TestBuildActionTasks_Select(t *testing.T) {
	req := NewReq()
	req.Actions = []*ChromeDPAction{{Name: "select", Selector: "#size", Value: "m"}}
	tasks, err := req.buildActionTasks()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// One action waits for the element and chooses the option.
	if len(tasks) != 1 {
		t.Errorf("got %d tasks, want 1", len(tasks))
	}
}
