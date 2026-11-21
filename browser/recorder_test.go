package browser

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe/actionrpc"
)

// recorderPage is a page that does things after a click, once the step of
// the click has ended: it fetches an API the server fails and writes an
// error, or opens a window.
func recorderPage(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/other":
			fmt.Fprint(w, `<!doctype html><title>o</title><link rel="icon" href="data:,"><p id="o">other</p>`)
		default:
			fmt.Fprint(w, `<!doctype html><title>t</title><link rel="icon" href="data:,"><p id="out">none</p>
<button id="later" onclick="setTimeout(() => { fetch('/api'); console.error('late error'); }, 300)">Later</button>
<button id="pop" onclick="setTimeout(() => window.open('/other'), 300)">Pop</button>
<button id="now" onclick="fetch('/api'); console.error('now error')">Now</button>`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// keptSteps returns a function that runs a step of a kept browser of a run
// of its own, with with's keys besides, going on with the state of the
// step before that passed.
func keptSteps(t *testing.T, name string) func(with map[string]any, actions ...any) (map[string]any, error) {
	t.Helper()
	t.Setenv(CacheDirEnv, t.TempDir())
	a := &Action{log: hclog.NewNullLogger()}
	run := "run-" + name + "-" + strconv.Itoa(os.Getpid())
	t.Cleanup(func() { _ = cleanupSessions(run) })
	var state map[string]any
	return func(with map[string]any, actions ...any) (map[string]any, error) {
		t.Helper()
		w := map[string]any{"session": true, "headless": true, "timeout": "30s", "evidence_dir": t.TempDir(), "actions": actions}
		maps.Copy(w, with)
		ret, next, err := a.RunStep(actionrpc.Call{
			With: w, State: state, Step: actionrpc.Step{RunID: run},
			Cleanups: &actionrpc.Cleanups{}, Processes: &actionrpc.Processes{},
		})
		if err != nil {
			if strings.Contains(err.Error(), "executable file not found") {
				t.Skipf("no Chrome to start: %v", err)
			}
			return nil, err
		}
		if next != nil {
			state = next
		}
		return ret["res"].(map[string]any), nil
	}
}

// mustStep runs a step that is to pass.
func mustStep(t *testing.T, step func(map[string]any, ...any) (map[string]any, error), with map[string]any, actions ...any) map[string]any {
	t.Helper()
	res, err := step(with, actions...)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestRecorder_BetweenTheSteps reports, by the step after, an error of the
// console and a failed request the page made between the steps, once.
func TestRecorder_BetweenTheSteps(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	srv := recorderPage(t)
	step := keptSteps(t, "between")
	first := mustStep(t, step, nil, map[string]any{"name": "navigate", "url": srv.URL}, map[string]any{"name": "click", "selector": "#later"})
	time.Sleep(time.Second)
	second := mustStep(t, step, nil, map[string]any{"name": "title"})
	console := fmt.Sprint(first["console_errors"], second["console_errors"])
	network := fmt.Sprint(first["network_errors"], second["network_errors"])
	if strings.Count(console, "late error") != 1 || !strings.Contains(fmt.Sprint(second["console_errors"]), "late error") {
		t.Errorf("console errors %s, want the late error once, of the second step", console)
	}
	if strings.Count(network, "/api") != 1 || !strings.Contains(fmt.Sprint(second["network_errors"]), "500") {
		t.Errorf("network errors %s, want the API's 500 once, of the second step", network)
	}
}

// TestRecorder_AFailedStepIsReportedByTheNext reports what a step that
// failed saw by the step after it, as the runner keeps no state of a
// failed step, nor its result.
func TestRecorder_AFailedStepIsReportedByTheNext(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	srv := recorderPage(t)
	step := keptSteps(t, "failed")
	mustStep(t, step, nil, map[string]any{"name": "navigate", "url": srv.URL})
	if _, err := step(map[string]any{"timeout": "3s"}, map[string]any{"name": "click", "selector": "#later"}, map[string]any{"name": "wait_visible", "selector": "#never"}); err == nil {
		t.Fatal("the step waiting for what never comes passed")
	}
	third := mustStep(t, step, nil, map[string]any{"name": "title"})
	if !strings.Contains(fmt.Sprint(third["console_errors"]), "late error") || !strings.Contains(fmt.Sprint(third["network_errors"]), "/api") {
		t.Errorf("console errors %v, network errors %v, want those of the failed step", third["console_errors"], third["network_errors"])
	}
}

// TestRecorder_OneTabBetweenTheSteps keeps one_tab on between the steps
// of a job that has it: a window the page opens once the step has ended
// opens in the tab. A step without one_tab turns it off.
func TestRecorder_OneTabBetweenTheSteps(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	srv := recorderPage(t)
	step := keptSteps(t, "onetab")
	oneTab := map[string]any{"one_tab": true}
	mustStep(t, step, oneTab, map[string]any{"name": "navigate", "url": srv.URL}, map[string]any{"name": "click", "selector": "#pop"})
	time.Sleep(time.Second)
	res := mustStep(t, step, oneTab, map[string]any{"name": "url"})
	if got := fmt.Sprint(res["results"]); !strings.Contains(got, "/other") {
		t.Errorf("results %s, want the tab on the page the window was opened for", got)
	}

	mustStep(t, step, nil, map[string]any{"name": "navigate", "url": srv.URL}, map[string]any{"name": "click", "selector": "#pop"})
	time.Sleep(time.Second)
	res = mustStep(t, step, nil, map[string]any{"name": "url"})
	if got := fmt.Sprint(res["results"]); strings.Contains(got, "/other") {
		t.Errorf("results %s, want the tab where it was, one_tab off", got)
	}
}

// TestRecorder_Gone has the steps record while they run once the recorder
// is gone, rather than report a page with no errors, and report what it
// wrote before it went.
func TestRecorder_Gone(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	srv := recorderPage(t)
	step := keptSteps(t, "gone")
	// What the page does once the step has ended, which the recorder
	// writes before it goes.
	mustStep(t, step, nil, map[string]any{"name": "navigate", "url": srv.URL}, map[string]any{"name": "click", "selector": "#later"})
	time.Sleep(time.Second)
	matches, _ := filepath.Glob(filepath.Join(os.Getenv(CacheDirEnv), "sessions", "*", "browser-*", recorderPIDFile))
	if len(matches) != 1 {
		t.Fatalf("recorder PID files %v, want one", matches)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b))
	stopGroup(pid, filepath.Dir(matches[0]))

	if !waitGone(pid) {
		t.Fatalf("the recorder %d did not stop", pid)
	}
	res := mustStep(t, step, nil, map[string]any{"name": "click", "selector": "#now"})
	if c := fmt.Sprint(res["console_errors"]); !strings.Contains(c, "now error") || !strings.Contains(c, "late error") || strings.Count(fmt.Sprint(res["network_errors"]), "/api") != 2 {
		t.Errorf("console errors %v, network errors %v, want those the recorder wrote, and those of the click, recorded by the step", res["console_errors"], res["network_errors"])
	}
	res = mustStep(t, step, nil, map[string]any{"name": "click", "selector": "#now"})
	if !strings.Contains(fmt.Sprint(res["console_errors"]), "now error") {
		t.Errorf("console errors %v of the step after, want the click's", res["console_errors"])
	}
}

func waitGone(pid int) bool {
	for range 50 {
		if !alive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// TestRecorder_GoneWhileTheStepRan fails a step whose recorder went while
// it ran, and the step after reports what it wrote.
func TestRecorder_GoneWhileTheStepRan(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	srv := recorderPage(t)
	step := keptSteps(t, "gonewhile")
	mustStep(t, step, nil, map[string]any{"name": "navigate", "url": srv.URL})
	matches, _ := filepath.Glob(filepath.Join(os.Getenv(CacheDirEnv), "sessions", "*", "browser-*", recorderPIDFile))
	if len(matches) != 1 {
		t.Fatalf("recorder PID files %v, want one", matches)
	}
	b, _ := os.ReadFile(matches[0])
	pid, _ := strconv.Atoi(string(b))
	// The recorder goes while the step waits for a text that comes after
	// the click's error.
	go func() {
		time.Sleep(time.Second)
		stopGroup(pid, filepath.Dir(matches[0]))
	}()
	_, err := step(map[string]any{"timeout": "3s"}, map[string]any{"name": "click", "selector": "#now"}, map[string]any{"name": "wait_visible", "selector": "#never"})
	if err == nil {
		t.Fatal("the step passed with its recorder gone")
	}
	res := mustStep(t, step, nil, map[string]any{"name": "title"})
	if !strings.Contains(fmt.Sprint(res["console_errors"]), "now error") {
		t.Errorf("console errors %v, want the click's, which the recorder wrote", res["console_errors"])
	}
}
