package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	jsonv2 "github.com/chromedp/cdproto/cdp/jsonv2"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	cdptarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
	"github.com/mozership/probe/actionrpc"
)

// A kept browser has a recorder: a process of probe-browser of its own,
// which stays connected to the tab the steps drive from when the browser
// starts until it stops, and writes what the page does, its requests and
// its console, to a file in the profile, a line each, as it comes. The
// steps connect to the tab and leave it, and read the file: what came
// while they ran, and what came between them, which no step would see
// else. The recorder also keeps one_tab on between the steps of a job
// that has it, in every document the tab opens.

// recorderEnv is set, to what the recorder is to record, in the
// environment of probe-browser started as a recorder.
const recorderEnv = "PROBE_BROWSER_RECORDER"

// recorderFile is the file of the profile the recorder writes, and
// recorderPIDFile the one that holds its PID, for Cleanup to stop it by;
// oneTabFile is there while the steps keep one_tab on.
const (
	recorderFile    = "probe-browser-recorder.jsonl"
	recorderPIDFile = "probe-browser-recorder.pid"
	oneTabFile      = "probe-browser-one-tab"
	// oneTabState is where the recorder says whether it keeps one_tab on,
	// "on" or "off", once it has done as oneTabFile says.
	oneTabState = "probe-browser-one-tab.state"
)

// recorderReady bounds how long a step that starts a browser waits for its
// recorder to connect, before it goes on without one.
const recorderReady = 10 * time.Second

// recorderJob is what a recorder records: the tab of the browser, and the
// profile it writes in.
type recorderJob struct {
	WSURL    string `json:"ws_url"`
	TargetID string `json:"target_id"`
	Dir      string `json:"dir"`
}

// recordedLine is a line of the recorder's file: an event of the page's
// requests, of the method, or a message of its console, or the mark that
// the recorder is connected.
type recordedLine struct {
	Kind    string           `json:"k"`
	Method  string           `json:"m,omitempty"`
	Params  json.RawMessage  `json:"p,omitempty"`
	Console *recordedConsole `json:"c,omitempty"`
}

// recordedConsole is what is kept of a message of the console.
type recordedConsole struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	URL    string `json:"url,omitempty"`
	Line   int64  `json:"line,omitempty"`
	Column int64  `json:"column,omitempty"`
	Source string `json:"source,omitempty"`
}

const (
	lineReady   = "ready"
	lineNetwork = "net"
	lineConsole = "console"
)

// IsRecorder reports whether this process of probe-browser was started as
// the recorder of a kept browser.
func IsRecorder() bool {
	return os.Getenv(recorderEnv) != ""
}

// RunRecorder records what the environment names until the browser goes
// away or the process is told to stop, and returns the code to exit with.
func RunRecorder() int {
	var job recorderJob
	if err := json.Unmarshal([]byte(os.Getenv(recorderEnv)), &job); err != nil {
		fmt.Fprintln(os.Stderr, "probe-browser recorder:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := record(ctx, job); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "probe-browser recorder:", err)
		return 1
	}
	return 0
}

// record connects to the tab of job and writes what the page does to the
// file of its profile until ctx ends or the browser goes away.
func record(ctx context.Context, job recorderJob) error {
	f, err := os.OpenFile(filepath.Join(job.Dir, recorderFile), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	var mu sync.Mutex
	write := func(l recordedLine) {
		b, err := json.Marshal(l)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		// One write a line, so that a reader never sees half of one but
		// at the end.
		_, _ = f.Write(append(b, '\n'))
	}

	allocCtx, allocCancel := remote.NewAllocator(ctx, job.WSURL, remote.NoModifyURL)
	defer allocCancel()
	tabCtx, tabCancel := chromedp.NewContext(allocCtx, chromedp.WithTargetID(cdptarget.ID(job.TargetID)), chromedp.WithDetachOnCancel())
	defer tabCancel()
	if err := chromedp.Do(tabCtx); err != nil {
		return err
	}
	c := chromedp.FromContext(tabCtx)

	var wg sync.WaitGroup
	for _, m := range networkMethods {
		events, unsubscribe := c.Target.Subscribe(m)
		wg.Go(func() {
			defer unsubscribe()
			for {
				select {
				case <-tabCtx.Done():
					return
				case raw, ok := <-events:
					if !ok {
						return
					}
					write(recordedLine{Kind: lineNetwork, Method: m, Params: json.RawMessage(raw)})
				}
			}
		})
	}
	console := chromedp.Console(tabCtx)
	wg.Go(func() {
		for m, err := range console {
			if err != nil {
				return
			}
			write(recordedLine{Kind: lineConsole, Console: &recordedConsole{
				Type: string(m.Type), Text: m.Text, URL: m.URL, Line: m.Line, Column: m.Column, Source: m.Source,
			}})
		}
	})
	wg.Go(func() { keepOneTab(tabCtx, job.Dir) })
	write(recordedLine{Kind: lineReady})

	select {
	case <-ctx.Done():
	case <-c.Browser.LostConnection:
	}
	tabCancel()
	wg.Wait()
	return ctx.Err()
}

// oneTabPoll is how often the recorder looks whether the steps keep
// one_tab on.
const oneTabPoll = 50 * time.Millisecond

// keepOneTab has one_tab's script run in every document the tab opens
// while the file of dir says the steps keep it on, as between the steps of
// a job that has it, which leave the tab.
func keepOneTab(ctx context.Context, dir string) {
	var id page.ScriptIdentifier
	tick := time.NewTicker(oneTabPoll)
	defer tick.Stop()
	for {
		_, err := os.Stat(filepath.Join(dir, oneTabFile))
		on := err == nil
		switch {
		case on && id == "":
			if res, err := chromedp.Call(ctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: oneTabScript}); err == nil {
				id = res.Identifier
			}
		case !on && id != "":
			if _, err := chromedp.Call(ctx, page.RemoveScriptToEvaluateOnNewDocument, page.RemoveScriptToEvaluateOnNewDocumentParams{Identifier: id}); err == nil {
				id = ""
			}
		}
		st := "off"
		if id != "" {
			st = "on"
		}
		_ = os.WriteFile(filepath.Join(dir, oneTabState), []byte(st), 0o600)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// startRecorder starts the recorder of the browser of s, as a process of
// this executable in a group of its own, which the runner stops once the
// run is over, and waits for it to connect. It returns the file it
// writes.
func startRecorder(s *session, processes *actionrpc.Processes, deadline time.Time) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	job, err := json.Marshal(recorderJob{WSURL: s.WSURL, TargetID: s.TargetID, Dir: s.Dir})
	if err != nil {
		return "", err
	}
	logFile, err := os.OpenFile(filepath.Join(s.Dir, "probe-browser-recorder.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	defer logFile.Close()
	// The profile on its command line, as on the browser's, by which
	// Cleanup finds it.
	cmd := exec.Command(exe, "record", "--user-data-dir="+s.Dir)
	cmd.Env = append(os.Environ(), recorderEnv+"="+string(job))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	pid := cmd.Process.Pid
	// The runner, or Cleanup, stops it; it is reaped here while this
	// process lasts, as it outlives it once the step is done.
	go func() { _ = cmd.Wait() }()
	if err := os.WriteFile(filepath.Join(s.Dir, recorderPIDFile), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		stopGroup(pid, s.Dir)
		return "", err
	}
	processes.Add(actionrpc.Process{PID: pid})
	path := filepath.Join(s.Dir, recorderFile)
	// A third of what is left of the step at most, so that a step whose
	// recorder does not come has the rest to go on without one.
	wait := recorderReady
	if !deadline.IsZero() {
		wait = min(wait, time.Until(deadline)/3)
	}
	until := time.Now().Add(wait)
	for time.Now().Before(until) {
		if b, err := os.ReadFile(path); err == nil && bytes.Contains(b, []byte(`"k":"`+lineReady+`"`)) {
			return path, nil
		}
		if !alive(pid) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stopGroup(pid, s.Dir)
	return "", fmt.Errorf("the recorder of the browser did not connect")
}

// recordedTail reads what the recorder wrote from an offset of its file,
// a whole line at a time, as it goes on.
type recordedTail struct {
	path   string
	offset int64
	// events are those of each method of the requests, in the order they
	// came; console those of the console.
	events  map[string][]jsonv2.Value
	console []recordedConsole
	// inflight are the requests sent, and whether each has ended; last is
	// when a line of the requests was last read.
	inflight map[string]bool
	last     time.Time
}

func newRecordedTail(path string, offset int64) *recordedTail {
	return &recordedTail{path: path, offset: offset, events: map[string][]jsonv2.Value{}, inflight: map[string]bool{}, last: time.Now()}
}

// read reads the lines written since it last did.
func (t *recordedTail) read() error {
	f, err := os.Open(t.path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, 0); err != nil {
		return err
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(f); err != nil {
		return err
	}
	b := buf.Bytes()
	end := bytes.LastIndexByte(b, '\n')
	if end < 0 {
		return nil
	}
	for line := range bytes.Lines(b[:end+1]) {
		var l recordedLine
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		switch l.Kind {
		case lineNetwork:
			raw := jsonv2.Value(l.Params)
			t.events[l.Method] = append(t.events[l.Method], raw)
			r := networkRecorder{inflight: t.inflight}
			r.track(l.Method, raw)
			t.last = time.Now()
		case lineConsole:
			if l.Console != nil {
				t.console = append(t.console, *l.Console)
			}
		}
	}
	t.offset += int64(end + 1)
	return nil
}

// marked returns whether the console has had the message text.
func (t *recordedTail) marked(text string) bool {
	for _, m := range t.console {
		if m.Type == string(chromedp.ConsoleDebug) && m.Text == text {
			return true
		}
	}
	return false
}

// consoleErrors returns the errors of the console read, but the marks.
func (t *recordedTail) consoleErrors() []ConsoleError {
	errs := []ConsoleError{}
	for _, m := range t.console {
		e, ok := consoleError(chromedp.ConsoleMessage{Type: chromedp.ConsoleType(m.Type), Text: m.Text, URL: m.URL, Line: m.Line, Column: m.Column, Source: m.Source})
		if ok {
			errs = append(errs, e)
		}
	}
	return errs
}

// recordedMark has the page write a mark of its own to the console, and
// waits for the recorder to have written it, consoleDrain at most: the
// browser sends the messages in order, so every one before is in. It
// returns whether the mark came.
func (t *recordedTail) recordedMark(ctx context.Context, run func(context.Context, ...chromedp.Action[chromedp.Void]) error) bool {
	ctx, cancel := context.WithTimeout(ctx, consoleDrain)
	defer cancel()
	text := fmt.Sprintf("%s %d", consoleEnd, time.Now().UnixNano())
	if run(ctx, chromedp.Evaluate[chromedp.Void](fmt.Sprintf("console.debug(%s)", jsString(text)))) != nil {
		return false
	}
	for {
		_ = t.read()
		if t.marked(text) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// settle waits for the requests sent since its offset to end,
// networkSettle at most, and then for no line of the requests to come for
// networkIdle, networkDrain at most, reading as it goes; neither past
// deadline, unless it is zero. A request sent while it waits for the quiet
// is waited for too.
func (t *recordedTail) settle(deadline time.Time) {
	limit := func(d time.Time) time.Time {
		if !deadline.IsZero() && deadline.Before(d) {
			return deadline
		}
		return d
	}
	settle := limit(time.Now().Add(networkSettle))
	var drain time.Time
	for {
		_ = t.read()
		r := networkRecorder{inflight: t.inflight}
		quiet, waiting := time.Since(t.last), r.waiting()
		now := time.Now()
		if waiting && now.Before(settle) {
			drain = time.Time{}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if drain.IsZero() {
			drain = limit(now.Add(networkDrain))
		}
		if quiet >= networkIdle || !now.Before(drain) {
			return
		}
		time.Sleep(max(time.Millisecond, min(networkIdle-quiet, time.Until(drain))))
	}
}

// requests puts together the requests of the lines read, going on with
// those pending a step before left.
func (t *recordedTail) requests(pending []pendingRequest) ([]NetworkError, Requests, []pendingRequest) {
	set := &requestSet{byID: map[network.RequestID]*netRequest{}, errs: []NetworkError{}, requests: Requests{Slowest: []SlowRequest{}}}
	set.carry(pending)
	for _, m := range networkMethods {
		for _, raw := range t.events[m] {
			_ = set.apply(m, raw)
		}
	}
	set.settle()
	return set.errs, set.requests, set.pending()
}

// syncedOffset returns where in the file of the recorder what comes from
// now on begins: once the page's mark is in, and the requests are quiet,
// so that what came before, as of the setting up of the step, is left out.
func syncedOffset(ctx context.Context, path string, run func(context.Context, ...chromedp.Action[chromedp.Void]) error) (int64, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	t := newRecordedTail(path, st.Size())
	if !t.recordedMark(ctx, run) {
		return 0, false
	}
	t.settle(time.Now().Add(networkDrain))
	return t.offset, true
}
