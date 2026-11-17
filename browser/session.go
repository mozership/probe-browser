package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	cdptarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
	"github.com/mozership/probe/actionrpc"
)

// CacheDirEnv overrides the directory the browsers kept for the jobs of a
// run are kept in.
const CacheDirEnv = "PROBE_BROWSER_CACHE_DIR"

// pidFile is the file in the profile of a kept browser that holds the PID
// of its process, for Cleanup to stop it by.
const pidFile = "probe-browser.pid"

// stopWait is how long a kept browser is given to exit on SIGTERM before it
// is killed.
const stopWait = 5 * time.Second

// session is a browser a step of a job keeps open for the steps after it,
// as its state names it.
type session struct {
	// WSURL is the DevTools websocket of the browser.
	WSURL string `json:"ws_url"`
	// TargetID is the tab the steps drive.
	TargetID string `json:"target_id"`
	// Dir is the profile of the browser, which Cleanup removes.
	Dir string `json:"dir"`
}

// sessionCall is what a step that keeps its browser is given by the runner:
// the browser an earlier step of the job kept, if any, and where to record
// the one it starts.
type sessionCall struct {
	runID     string
	kept      *session
	processes *actionrpc.Processes
	// started is the browser the step started, for the state it leaves.
	started *session
}

// sessionFromState reads the browser the state of the job names, or nil
// when it names none.
func sessionFromState(state map[string]any) (*session, error) {
	v, ok := state["session"]
	if !ok || v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var s session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("reading the browser the job kept: %w", err)
	}
	if s.WSURL == "" || s.TargetID == "" {
		return nil, errors.New("reading the browser the job kept: it has no address or tab")
	}
	return &s, nil
}

// state returns the state of the job that names s.
func (s *session) state() map[string]any {
	return map[string]any{"session": map[string]any{
		"ws_url":    s.WSURL,
		"target_id": s.TargetID,
		"dir":       s.Dir,
	}}
}

// sessionsDir is the directory the browsers of the run are kept in. The
// run's ID is hashed, so that it names a directory whatever it holds.
func sessionsDir(runID string) (string, error) {
	root := os.Getenv(CacheDirEnv)
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(cache, "probe-browser")
	}
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(root, "sessions", hex.EncodeToString(sum[:])[:16]), nil
}

// startSession starts a browser that outlives the step, in a session of its
// own with a profile under the run's directory, and returns the context of
// its tab. Cancelling the context leaves the browser and the tab open.
func (req *Req) startSession(base context.Context) (context.Context, context.CancelFunc, error) {
	dir, err := sessionsDir(req.session.runID)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	profile, err := os.MkdirTemp(dir, "browser-")
	if err != nil {
		return nil, nil, err
	}
	opts := append(req.buildChromeDPOptions(),
		chromedp.KeepOpen,
		remote.WebSocket,
		chromedp.UserDataDir(profile),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(base, opts...)
	ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithDetachOnCancel(), req.debugf())
	cancelFunc := func() {
		cancel()
		allocCancel()
	}
	// KeepOpen leaves the browser running when the contexts are
	// cancelled, so one that started but cannot be used is stopped here,
	// before its profile, by which Cleanup would find it, is removed.
	discard := func() {
		if c := chromedp.FromContext(ctx); c != nil && c.Browser != nil {
			if p := c.Browser.Process(); p != nil {
				stopGroup(p.Pid, profile)
			}
		}
		cancelFunc()
		_ = os.RemoveAll(profile)
	}
	if err := req.browserRunner.Start(ctx); err != nil {
		discard()
		return nil, nil, err
	}
	c := chromedp.FromContext(ctx)
	if p := c.Browser.Process(); p != nil {
		if err := os.WriteFile(filepath.Join(profile, pidFile), []byte(strconv.Itoa(p.Pid)), 0o600); err != nil {
			discard()
			return nil, nil, err
		}
		req.session.processes.Add(actionrpc.Process{PID: p.Pid})
	}
	wsURL, _ := chromedp.KeptOpen(ctx)
	req.session.started = &session{WSURL: wsURL, TargetID: string(c.Target.TargetID), Dir: profile}
	return ctx, cancelFunc, nil
}

// attachSession returns the context of the tab of the browser an earlier
// step of the job kept. Cancelling it detaches from the tab and leaves it
// open.
func (req *Req) attachSession(base context.Context) (context.Context, context.CancelFunc, error) {
	s := req.session.kept
	allocCtx, allocCancel := remote.NewAllocator(base, s.WSURL, remote.NoModifyURL)
	ctx, cancel := chromedp.NewContext(allocCtx,
		chromedp.WithTargetID(cdptarget.ID(s.TargetID)),
		chromedp.WithDetachOnCancel(),
		req.debugf(),
	)
	cancelFunc := func() {
		cancel()
		allocCancel()
	}
	if err := req.browserRunner.Start(ctx); err != nil {
		cancelFunc()
		return nil, nil, fmt.Errorf("the browser an earlier step of the job kept is gone: %w", err)
	}
	return ctx, cancelFunc, nil
}

// cleanupSessions stops the browsers the steps of the run kept and removes
// their profiles. It does not fail for a browser that is gone already.
func cleanupSessions(runID string) error {
	dir, err := sessionsDir(runID)
	if err != nil {
		return err
	}
	profiles, err := filepath.Glob(filepath.Join(dir, "browser-*"))
	if err != nil {
		return err
	}
	for _, p := range profiles {
		b, err := os.ReadFile(filepath.Join(p, pidFile))
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(string(b)); err == nil {
			stopGroup(pid, p)
		}
	}
	return os.RemoveAll(dir)
}

// discardSession stops the browser a step started and removes its profile,
// for a step that failed: Probe keeps no state a failed step returns, so no
// later step could reach the browser.
func discardSession(s *session) {
	b, err := os.ReadFile(filepath.Join(s.Dir, pidFile))
	if err == nil {
		if pid, err := strconv.Atoi(string(b)); err == nil {
			stopGroup(pid, s.Dir)
		}
	}
	_ = os.RemoveAll(s.Dir)
}

// stopGroup stops the process group of the browser whose profile is
// profile, which its process pid leads: SIGTERM first, then SIGKILL for one
// that has not exited. The group is the browser's while pid runs the
// browser with that profile, or, once pid has exited, while the group has
// processes left, as a process group's ID is not reused while it has any;
// a pid that runs anything else is another process that took the number.
func stopGroup(pid int, profile string) {
	if pid <= 0 || !groupAlive(pid) {
		return
	}
	if alive(pid) && !runsProfile(pid, profile) {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	if waitGroupGone(pid, stopWait) {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	waitGroupGone(pid, stopWait)
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func groupAlive(pgid int) bool {
	return syscall.Kill(-pgid, 0) == nil
}

// runsProfile reports whether the process pid runs a browser with the
// profile, as its command line names it.
func runsProfile(pid int, profile string) bool {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.Contains(string(out), "--user-data-dir="+profile)
}

// waitGroupGone reports whether every process of the group exits within d.
func waitGroupGone(pgid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !groupAlive(pgid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !groupAlive(pgid)
}
