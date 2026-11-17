package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
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
			// A runner of CI under -race can take more than 20 seconds to
			// start the browser and fill the field. The page is saved in
			// the test's directory when the step fails.
			With:      map[string]any{"session": true, "headless": true, "timeout": "60s", "evidence_dir": t.TempDir(), "actions": actions},
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
	// The second step names the browser the first kept, beside the
	// requests it leaves pending.
	if again, err := sessionFromState(state2); err != nil || again == nil || *again != *kept {
		t.Errorf("the second step left state %v, %v; want the browser the first kept", state2, err)
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

// TestRunStep_AFailedFirstStepLeavesNoBrowser fails the step that starts the
// browser: Probe keeps no state it returns, so the browser is stopped then.
func TestRunStep_AFailedFirstStepLeavesNoBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	cache := t.TempDir()
	t.Setenv(CacheDirEnv, cache)
	a := &Action{log: hclog.NewNullLogger()}
	run := "run-failed-" + strconv.Itoa(os.Getpid())
	procs := &actionrpc.Processes{}
	_, state, err := a.RunStep(actionrpc.Call{
		With: map[string]any{
			"session": true, "headless": true, "timeout": "3s", "evidence_dir": t.TempDir(),
			"actions": []any{map[string]any{"name": "wait_visible", "selector": "#nothing"}},
		},
		Step:      actionrpc.Step{RunID: run},
		Cleanups:  &actionrpc.Cleanups{},
		Processes: procs,
	})
	if err == nil {
		t.Fatal("the step did not fail")
	}
	if strings.Contains(err.Error(), "executable file not found") {
		t.Skipf("no Chrome to start: %v", err)
	}
	if state != nil {
		t.Errorf("state = %v, want none", state)
	}
	for _, p := range procs.List() {
		deadline := time.Now().Add(stopWait)
		for groupAlive(p.PID) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if groupAlive(p.PID) {
			t.Errorf("the browser %d is still running", p.PID)
		}
	}
	dir, _ := sessionsDir(run)
	if left, _ := filepath.Glob(filepath.Join(dir, "browser-*")); len(left) != 0 {
		t.Errorf("profiles left: %v", left)
	}
}

// startGroup starts sh running script, with args after it on its command
// line, as the leader of a process group of its own, as a kept browser
// runs, and returns its PID, once it has exited when exit is set.
func startGroup(t *testing.T, script string, exit bool, args ...string) int {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", script}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	// The shell is reaped as soon as it exits, as init reaps a kept browser
	// whose step has exited: on Linux, a zombie left unreaped would keep
	// its group alive.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	if exit {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		done <- nil
	}
	return pid
}

func TestStopGroup_LeavesAProcessThatTookTheNumber(t *testing.T) {
	pid := startGroup(t, "sleep 30", false)
	stopGroup(pid, filepath.Join(t.TempDir(), "browser-x"))
	if !alive(pid) {
		t.Error("a process that does not run the browser's profile was stopped")
	}
}

func TestStopGroup_StopsWhatTheBrowserLeft(t *testing.T) {
	// The leader exits and leaves a child in its group, as a browser that
	// crashed can leave its helpers.
	pid := startGroup(t, "sleep 30 & exit 0", true)
	if !groupAlive(pid) {
		t.Fatal("the group has no process left to stop")
	}
	stopGroup(pid, filepath.Join(t.TempDir(), "browser-x"))
	if groupAlive(pid) {
		t.Error("the processes the leader left are still running")
	}
}

func TestNamesProfile(t *testing.T) {
	p := "/c/sessions/ab/browser-1"
	for command, want := range map[string]bool{
		"chrome --headless --user-data-dir=" + p + " about:blank": true,
		"chrome --user-data-dir=" + p:                             true,
		"chrome --user-data-dir=" + p + "2 about:blank":           false,
		"sleep 30": false,
	} {
		if got := namesProfile(command, p); got != want {
			t.Errorf("%q: %v, want %v", command, got, want)
		}
	}
}

// TestCleanupSessions_FindsABrowserWithoutItsPIDFile stops a process that
// runs with a profile that has no PID file, as a browser whose step was
// stopped before it wrote one, under a cache directory whose path holds a
// character of a glob pattern.
func TestCleanupSessions_FindsABrowserWithoutItsPIDFile(t *testing.T) {
	t.Setenv(CacheDirEnv, filepath.Join(t.TempDir(), "cache[1"))
	run := "run-nopid-" + strconv.Itoa(os.Getpid())
	dir, err := sessionsDir(run)
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(dir, "browser-1")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	// The shell stands in for the browser, the profile on its command line;
	// the : after sleep keeps it from replacing itself with sleep.
	pid := startGroup(t, "sleep 30; :", false, "--user-data-dir="+profile, "about:blank")
	if !runsProfile(pid, profile) {
		t.Fatal("the stand-in browser does not show the profile on its command line")
	}
	if err := cleanupSessions(run); err != nil {
		t.Fatal(err)
	}
	if groupAlive(pid) {
		t.Error("the browser without a PID file is still running")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the run's directory is still there: %v", err)
	}
}

// TestRunStep_ARefGoneWhileWaitedForFails waits for the element of a ref to
// be shown, and the page takes it out meanwhile: the action fails as soon
// as the ref is gone, with the reason, rather than once the timeout runs
// out. A click that takes its own button out succeeds.
func TestRunStep_ARefGoneWhileWaitedForFails(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	t.Setenv(CacheDirEnv, t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>t</title>
<button id="b">Go</button>
<button id="hider" onclick="b.style.display='none'; setTimeout(() => b.remove(), 1000)">Hide</button>
<button id="self" onclick="this.remove()">Close</button>`)
	}))
	defer srv.Close()

	a := &Action{log: hclog.NewNullLogger()}
	run := "run-gone-" + strconv.Itoa(os.Getpid())
	t.Cleanup(func() { _ = cleanupSessions(run) })
	call := func(state map[string]any, timeout string, actions ...any) (map[string]any, map[string]any, error) {
		return a.RunStep(actionrpc.Call{
			With:      map[string]any{"session": true, "headless": true, "timeout": timeout, "evidence_dir": t.TempDir(), "actions": actions},
			State:     state,
			Step:      actionrpc.Step{RunID: run},
			Cleanups:  &actionrpc.Cleanups{},
			Processes: &actionrpc.Processes{},
		})
	}
	ret, state, err := call(nil, "20s",
		map[string]any{"name": "navigate", "url": srv.URL},
		map[string]any{"name": "read_page", "id": "outline"},
	)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skipf("no Chrome to start: %v", err)
		}
		t.Fatal(err)
	}
	outline := fmt.Sprint(ret["res"])
	refOf := func(name string) string {
		i := strings.Index(outline, `button "`+name+`" [`)
		if i < 0 {
			t.Fatalf("no button %q in %s", name, outline)
		}
		rest := outline[i+len(`button "`+name+`" [`):]
		return rest[:strings.Index(rest, "]")]
	}

	if _, _, err := call(state, "20s", map[string]any{"name": "click", "ref": refOf("Close")}); err != nil {
		t.Errorf("a click that takes its button out: %v", err)
	}

	start := time.Now()
	_, _, err = call(state, "20s",
		map[string]any{"name": "click", "selector": "#hider"},
		map[string]any{"name": "click", "ref": refOf("Go")},
	)
	if err == nil || !strings.Contains(err.Error(), "is not on the page any more") {
		t.Fatalf("err = %v, want the ref gone", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the click took %s to fail, as long as its timeout", d)
	}
}

// TestTarget_ARefGone: a script that runs once on the element of a ref
// gone fails with the reason, while the query of an action waits, finding
// none, as wait_not_visible needs.
func TestTarget_ARefGone(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.NoSandbox, chromedp.Flag("headless", true))
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	if err := chromedp.Do(ctx); err != nil {
		t.Skipf("no Chrome to start: %v", err)
	}
	ctx, tcancel := context.WithTimeout(ctx, 20*time.Second)
	defer tcancel()

	if err := chromedp.Do(ctx, chromedp.Navigate("data:text/html,<p>A page</p>")); err != nil {
		t.Fatal(err)
	}
	tg := target{ref: "ref_abcd1"}
	_, err := chromedp.Run(ctx, chromedp.Evaluate[chromedp.Void]("("+tg.first()+", undefined)"))
	if err == nil || !strings.Contains(err.Error(), "ref_abcd1 is not on the page any more") {
		t.Errorf("a script on a ref gone: %v, want the reason", err)
	}
	if err := chromedp.Do(ctx, chromedp.WaitNotPresent(tg.path(), tg.opts()...)); err != nil {
		t.Errorf("the query of a ref gone found something: %v", err)
	}
}

// TestRunStep_GoesOnWithARequestPending sends a request in one step of a
// kept browser that answers in the next: the next step, given the state
// the first left, records it, timed from when it was sent.
func TestRunStep_GoesOnWithARequestPending(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	t.Setenv(CacheDirEnv, t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(1500 * time.Millisecond)
			fmt.Fprint(w, "slow")
			return
		}
		fmt.Fprint(w, `<!doctype html><title>t</title><link rel="icon" href="data:,"><p id="out">none</p>
<button id="go" onclick="fetch('/slow').then((r) => r.text()).then((t) => document.getElementById('out').textContent = t)">Go</button>`)
	}))
	defer srv.Close()

	a := &Action{log: hclog.NewNullLogger()}
	run := "run-pending-" + strconv.Itoa(os.Getpid())
	t.Cleanup(func() { _ = cleanupSessions(run) })
	var state map[string]any
	step := func(actions ...any) map[string]any {
		t.Helper()
		ret, next, err := a.RunStep(actionrpc.Call{
			With:      map[string]any{"session": true, "headless": true, "timeout": "30s", "evidence_dir": t.TempDir(), "actions": actions},
			State:     state,
			Step:      actionrpc.Step{RunID: run},
			Cleanups:  &actionrpc.Cleanups{},
			Processes: &actionrpc.Processes{},
		})
		if err != nil {
			if strings.Contains(err.Error(), "executable file not found") {
				t.Skipf("no Chrome to start: %v", err)
			}
			t.Fatal(err)
		}
		if next != nil {
			state = next
		}
		return ret["res"].(map[string]any)
	}
	step(map[string]any{"name": "navigate", "url": srv.URL}, map[string]any{"name": "click", "selector": "#go"})
	if p := pendingFromState(state); len(p) != 1 || !strings.HasSuffix(p[0].URL, "/slow") {
		t.Fatalf("pending %+v, want the slow request", p)
	}
	res := step(map[string]any{"name": "wait_text", "selector": "#out", "equals": "slow"})
	got := fmt.Sprint(res["requests"])
	if !strings.Contains(got, "/slow") {
		t.Fatalf("requests %s, want the slow one the step before sent", got)
	}
	slow := res["requests"].(map[string]any)["slowest"].([]any)[0].(map[string]any)
	if ms, _ := slow["duration_ms"].(int64); ms < 1400 {
		t.Errorf("the slow request took %v ms, want it timed from when it was sent", slow["duration_ms"])
	}
	if p := pendingFromState(state); len(p) != 0 {
		t.Errorf("pending %+v after it ended", p)
	}
}
