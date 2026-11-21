package browser

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildActionTasks_FormsRefused(t *testing.T) {
	for _, tc := range []struct {
		action map[string]any
		want   string
	}{
		{map[string]any{"name": "locate"}, "locate needs locator"},
		{map[string]any{"name": "locate", "locator": map[string]any{"name": "Save"}}, "locate needs locator"},
		{map[string]any{"name": "locate", "locator": map[string]any{"role": "button", "name": "Save", "nth": 2, "of": 2}}, "locate: nth is 2, but it is 0 to 1"},
		{map[string]any{"name": "locate", "locator": map[string]any{"role": "button", "name": "Save", "nth": 1}}, "locate: nth is 1, but it is 0 to 0"},
		{map[string]any{"name": "locate", "locator": map[string]any{"role": "button", "name": "Save", "of": -1}}, "locate: of is -1"},
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
// once, with how many the page has, rather than at the timeout, whose
// error would say the time ran out. How long it took is not checked, as a
// runner of CI can take most of a minute to start Chrome.
func TestLocate_AnotherNumberFailsAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	_, err := Request(map[string]any{"headless": true, "timeout": "60s", "evidence_dir": t.TempDir(), "actions": []any{
		map[string]any{"name": "navigate", "url": "data:text/html,<button>Delete</button><button>Delete</button><button>Delete</button>"},
		map[string]any{"name": "locate", "locator": map[string]any{"role": "button", "name": "Delete", "nth": 1, "of": 2}},
	}})
	if err != nil && strings.Contains(err.Error(), "executable file not found") {
		t.Skipf("no Chrome to start: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), `locate button "Delete" (2 of 2): the page has 3 of them, where it had 2`) {
		t.Fatalf("err = %v, want the count told", err)
	}
}

// reviewFormsPage holds what form_input, locator and alerts are easy to get
// wrong.
const reviewFormsPage = `<!doctype html><title>t</title><body style="margin:0">
<input id="date" type="date">
<label><input type="radio" name="p" id="r1" checked> A</label>
<select id="s"><optgroup label="G" disabled><option value="x">X</option></optgroup><option value="y">Y</option></select>
<select id="dup"><option value="v" disabled>Old</option><option value="v">New</option></select>
<div id="ed" contenteditable="true">t</div><p id="edchanged">no</p>
<x-field></x-field><p id="composed">no</p>
<div style="opacity:0"><button id="hidden-twin">Twin</button></div><button id="twin">Twin</button>
<div style="opacity:0"><div role="alert">Hidden alert</div></div><div role="alert">Shown alert</div><div role="alert status">Second alert</div>
<x-btn style="position:absolute;left:0;top:400px"></x-btn>
<script>
document.getElementById("ed").addEventListener("change", () => { document.getElementById("edchanged").textContent = "yes"; });
document.addEventListener("input", (e) => { if (e.composedPath()[0].id === "inner") document.getElementById("composed").textContent = "yes"; });
customElements.define("x-field", class extends HTMLElement {
  constructor() { super(); this.attachShadow({mode: "open"}).innerHTML = '<input id="inner">'; }
});
customElements.define("x-btn", class extends HTMLElement {
  constructor() { super(); this.attachShadow({mode: "open"}).innerHTML = '<button style="width:100px;height:40px">Shadow button</button>'; }
});
</script>`

func TestForms_WhatIsEasyToGetWrong(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, reviewFormsPage) }))
	defer srv.Close()
	run := func(actions ...any) (map[string]string, error) {
		ret, err := Request(map[string]any{"headless": true, "timeout": "30s", "evidence_dir": t.TempDir(),
			"actions": append([]any{map[string]any{"name": "navigate", "url": srv.URL}}, actions...)})
		if err != nil {
			return nil, err
		}
		return ret["res"].(map[string]any)["results"].(map[string]string), nil
	}
	res, err := run(
		map[string]any{"name": "form_input", "id": "radio", "selector": "#r1", "value": "false"},
		map[string]any{"name": "form_input", "selector": "#ed", "value": "new"},
		map[string]any{"name": "text", "id": "edchanged", "selector": "#edchanged"},
		map[string]any{"name": "form_input", "selector": "#inner", "value": "typed"},
		map[string]any{"name": "text", "id": "composed", "selector": "#composed"},
		map[string]any{"name": "locator", "id": "twin", "selector": "#twin"},
		map[string]any{"name": "locator", "id": "hidden", "selector": "#hidden-twin"},
		map[string]any{"name": "form_input", "selector": "#dup", "value": "New"},
		map[string]any{"name": "read_page", "id": "dup", "filter": "all"},
		map[string]any{"name": "locator", "id": "shadow", "x": 10, "y": 410},
		map[string]any{"name": "alerts", "id": "alerts"},
	)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skipf("no Chrome to start: %v", err)
		}
		t.Fatal(err)
	}
	// The option chosen by its label is the one of that label, not the
	// first of its value, which is disabled.
	if !strings.Contains(res["dup"], `value="New" options="Old | New"`) {
		t.Errorf("the page reads\n%s\nwant New chosen", res["dup"])
	}
	for k, want := range map[string]string{
		// A radio is unchecked when asked to be.
		"radio": `Unchecked "#r1".`,
		// A contenteditable element fires change, and an input of a shadow
		// root an input the page outside it hears.
		"edchanged": "yes",
		"composed":  "yes",
		// A twin hidden by what holds it is not counted, and not described,
		// as its locator would name the other.
		"twin":   `{"role":"button","name":"Twin"}`,
		"hidden": "",
		// A point over a button of a shadow root is that button.
		"shadow": `{"role":"button","name":"Shadow button"}`,
		// An alert hidden by what holds it is not read.
		"alerts": "Shown alert\nSecond alert",
	} {
		if res[k] != want {
			t.Errorf("%s: %q, want %q", k, res[k], want)
		}
	}

	for _, tc := range []struct {
		action map[string]any
		want   string
	}{
		{map[string]any{"name": "form_input", "selector": "#date", "value": "2000-13-01"}, `did not take the value, and holds ""`},
		{map[string]any{"name": "form_input", "selector": "#s", "value": "X"}, `the option "X" is disabled`},
	} {
		if _, err := run(tc.action); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want it to say %q", tc.action, err, tc.want)
		}
	}
}
