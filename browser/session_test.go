package browser

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe/actionrpc"
)

func TestSessionFromState(t *testing.T) {
	kept := &session{WSURL: "ws://127.0.0.1:9222/devtools/browser/x", TargetID: "T1", Dir: "/tmp/p"}
	got, err := sessionFromState(kept.state())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *got != *kept {
		t.Errorf("read back %+v, want %+v", got, kept)
	}

	for _, state := range []map[string]any{nil, {}, {"session": nil}} {
		if s, err := sessionFromState(state); s != nil || err != nil {
			t.Errorf("%v: got %+v, %v, want none", state, s, err)
		}
	}

	if _, err := sessionFromState(map[string]any{"session": map[string]any{"dir": "/tmp/p"}}); err == nil {
		t.Error("a state without an address or a tab was taken")
	}
}

func TestRunStep_SessionNeedsCleanups(t *testing.T) {
	a := &Action{log: hclog.NewNullLogger()}
	_, state, err := a.RunStep(actionrpc.Call{With: map[string]any{
		"session": true,
		"actions": []any{map[string]any{"name": "navigate", "url": "about:blank"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "v1.30.0") {
		t.Errorf("err = %v, want one naming the Probe that cleans up", err)
	}
	if state != nil {
		t.Errorf("state = %v, want none", state)
	}
}

func TestRunStep_SessionRefusesPasskeys(t *testing.T) {
	a := &Action{log: hclog.NewNullLogger()}
	_, _, err := a.RunStep(actionrpc.Call{
		With: map[string]any{
			"session":               true,
			"virtual_authenticator": true,
			"actions":               []any{map[string]any{"name": "navigate", "url": "about:blank"}},
		},
		Step:     actionrpc.Step{RunID: "r"},
		Cleanups: &actionrpc.Cleanups{},
	})
	if err == nil || !strings.Contains(err.Error(), "virtual_authenticator") {
		t.Errorf("err = %v, want a refusal of virtual_authenticator", err)
	}
}

// TestRunStep_KeepsTheBrowser runs two steps of a job with session, as Probe
// does, the second given the state the first left, and cleans up the run.
func TestBuildActionTasks_AttachingSkipsTheStorageState(t *testing.T) {
	path := t.TempDir() + "/state.json"
	if _, err := writeStorageState(testState, path); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		sc   *sessionCall
		want int
	}{
		{"a step that starts the browser", &sessionCall{}, 1},
		{"a step that attaches to it", &sessionCall{kept: &session{WSURL: "ws://x", TargetID: "T"}}, 0},
	} {
		req := NewReq()
		req.Session, req.StorageState, req.session = true, path, tc.sc
		tasks, err := req.buildActionTasks()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(tasks) != tc.want {
			t.Errorf("%s: %d tasks, want %d", tc.name, len(tasks), tc.want)
		}
	}
}

func TestRunStep_KeepsTheBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	t.Setenv(CacheDirEnv, t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>t</title><h1>Kept</h1><input id="q" value="old">`)
	}))
	defer srv.Close()

	a := &Action{log: hclog.NewNullLogger()}
	run := "run-" + strconv.Itoa(os.Getpid())
	procs := &actionrpc.Processes{}
	call := func(state map[string]any, actions ...any) (map[string]any, map[string]any, error) {
		return a.RunStep(actionrpc.Call{
			With:      map[string]any{"session": true, "headless": true, "timeout": "20s", "actions": actions},
			State:     state,
			Step:      actionrpc.Step{RunID: run},
			Cleanups:  &actionrpc.Cleanups{},
			Processes: procs,
		})
	}

	_, state, err := call(nil,
		map[string]any{"name": "navigate", "url": srv.URL},
		map[string]any{"name": "set_value", "selector": "#q", "value": "kept"},
	)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skipf("no Chrome to start: %v", err)
		}
		t.Fatalf("first step: %v", err)
	}
	kept, err := sessionFromState(state)
	if err != nil || kept == nil {
		t.Fatalf("the first step left no browser: %v, %v", state, err)
	}
	t.Cleanup(func() { _ = cleanupSessions(run) })
	if got := procs.List(); len(got) != 1 || got[0].PID <= 0 {
		t.Fatalf("processes = %v, want the browser", got)
	}
	pid := procs.List()[0].PID

	ret, state2, err := call(state, map[string]any{"name": "value", "id": "q", "selector": "#q"})
	if err != nil {
		t.Fatalf("second step: %v", err)
	}
	if state2 != nil {
		t.Errorf("the second step left state %v, want the first's kept", state2)
	}
	if got := fmt.Sprint(ret["res"]); !strings.Contains(got, "q:kept") {
		t.Errorf("res = %s, want q kept from the first step", got)
	}

	if err := a.Cleanup(actionrpc.CleanupCall{RunID: run}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if alive(pid) {
		t.Errorf("the browser %d is still running after the cleanup", pid)
	}
	if _, err := os.Stat(kept.Dir); !os.IsNotExist(err) {
		t.Errorf("the profile %s is still there: %v", kept.Dir, err)
	}

	if _, _, err := call(state, map[string]any{"name": "value", "id": "q", "selector": "#q"}); err == nil ||
		!strings.Contains(err.Error(), "is gone") {
		t.Errorf("a step after the cleanup: %v, want the browser gone", err)
	}
}
