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
	"github.com/mozership/probe/sdk"
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
	// Recorder is the file its recorder writes, empty for a browser that
	// has none, as when it did not start.
	Recorder string `json:"recorder,omitempty"`
}

// sessionCall is what a step that keeps its browser is given by the runner:
// the browser an earlier step of the job kept, if any, and where to record
// the one it starts.
type sessionCall struct {
	runID     string
	kept      *session
	processes *sdk.Processes
	// started is the browser the step started, for the state it leaves.
	started *session
	// pending are the requests the step before left, and pendingOut those
	// the step leaves to the next; used is a step that kept its browser,
	// whose state names them.
	pending    []pendingRequest
	pendingOut []pendingRequest
	used       bool
	// stopped is when the step before stopped recording, and stoppedOut
	// when the step does, for the next, in milliseconds of the Unix
	// epoch, 0 when not known.
	stopped, stoppedOut float64
	// offset is where in the file of the recorder the step before stopped
	// reading, and offsetOut where the step does, for the next.
	offset, offsetOut int64
}

// dropRecorder leaves the recorder of the browser out of the state the
// step leaves, as it is gone.
func (sc *sessionCall) dropRecorder() {
	for _, s := range []*session{sc.kept, sc.started} {
		if s != nil {
			s.Recorder = ""
		}
	}
}

// offsetFromState reads where in the file of the recorder the step before
// stopped reading, as the state of the job names it.
func offsetFromState(state map[string]any) int64 {
	switch v := state["recorder_offset"].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// stoppedFromState reads when the step before stopped recording, as the
// state of the job names it, 0 when it does not.
func stoppedFromState(state map[string]any) float64 {
	// The runner gives a number of no fraction as an int64.
	switch v := state["network_stopped"].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	}
	return 0
}

// pendingFromState reads the requests the state of the job names pending.
func pendingFromState(state map[string]any) []pendingRequest {
	v, ok := state["pending"]
	if !ok || v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []pendingRequest
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// stateWith returns the state of the job that names s, the requests
// pending, and when the step stopped recording, when known.
func (s *session) stateWith(pending []pendingRequest, stopped float64, offset int64) map[string]any {
	state := s.state()
	list := make([]any, len(pending))
	for i, p := range pending {
		b, _ := json.Marshal(p)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		list[i] = m
	}
	state["pending"] = list
	if stopped > 0 {
		state["network_stopped"] = stopped
	}
	if s.Recorder != "" {
		state["recorder_offset"] = offset
	}
	return state
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
		"recorder":  s.Recorder,
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
		req.session.processes.Add(sdk.Process{PID: p.Pid})
	}
	wsURL, _ := chromedp.KeptOpen(ctx)
	s := &session{WSURL: wsURL, TargetID: string(c.Target.TargetID), Dir: profile}
	// Without a recorder, the steps record while they run, and what comes
	// between them is read from the page's timings.
	deadline, _ := base.Deadline()
	if path, err := startRecorder(s, req.session.processes, deadline.Add(-evidenceGrace)); err == nil {
		s.Recorder = path
	} else {
		req.debugLog("the recorder of the browser did not start: %v", err)
	}
	req.session.started = s
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
	// The directory is read rather than globbed, so that a cache directory
	// whose path holds a character of a pattern, such as [, is taken as it
	// is.
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "browser-") {
			stopProfile(filepath.Join(dir, e.Name()))
		}
	}
	return os.RemoveAll(dir)
}

// stopProfile stops the browser whose profile is profile: the one its PID
// file names, and any other process running with the profile, as a browser
// whose step was stopped before it wrote the file.
func stopProfile(profile string) {
	groups := profileGroups(profile)
	for _, f := range []string{pidFile, recorderPIDFile} {
		if b, err := os.ReadFile(filepath.Join(profile, f)); err == nil {
			if pid, err := strconv.Atoi(string(b)); err == nil {
				groups = append(groups, pid)
			}
		}
	}
	for _, g := range groups {
		stopGroup(g, profile)
	}
}

// profileGroups returns the process groups of the processes that run with
// the profile, as their command lines name it.
func profileGroups(profile string) []int {
	out, err := exec.Command("ps", "-ax", "-o", "pgid=,command=").Output()
	if err != nil {
		return nil
	}
	var groups []int
	for line := range strings.Lines(string(out)) {
		pgid, command, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !namesProfile(command, profile) {
			continue
		}
		if g, err := strconv.Atoi(pgid); err == nil && g > 1 {
			groups = append(groups, g)
		}
	}
	return groups
}

// namesProfile reports whether the command line runs a browser with the
// profile, and not with another whose path begins with it.
func namesProfile(command, profile string) bool {
	flag := "--user-data-dir=" + profile
	return strings.Contains(command, flag+" ") || strings.HasSuffix(strings.TrimSpace(command), flag)
}

// discardSession stops the browser a step started and removes its profile,
// for a step that failed: Probe keeps no state a failed step returns, so no
// later step could reach the browser.
func discardSession(s *session) {
	stopProfile(s.Dir)
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
	return err == nil && namesProfile(string(out), profile)
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
