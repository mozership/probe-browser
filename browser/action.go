package browser

import (
	"fmt"

	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe/actionrpc"
)

type Action struct {
	log hclog.Logger
}

var (
	_ actionrpc.StepAction = (*Action)(nil)
	_ actionrpc.Cleaner    = (*Action)(nil)
)

// Run runs a step without a run, which has no browser to keep; Probe calls
// RunStep.
func (a *Action) Run(with map[string]any) (map[string]any, error) {
	return a.run(with, nil)
}

// RunStep runs a step, in the browser an earlier step of the job kept when
// it sets session, and leaves the browser it starts in the job's state.
func (a *Action) RunStep(call actionrpc.Call) (map[string]any, map[string]any, error) {
	var sc *sessionCall
	// A runner that does not clean up would leave a kept browser running,
	// so a step that keeps one is refused under it.
	if call.Cleanups != nil {
		kept, err := sessionFromState(call.State)
		if err != nil {
			return nil, nil, err
		}
		sc = &sessionCall{runID: call.Step.RunID, kept: kept, processes: call.Processes}
	}
	ret, err := a.run(call.With, sc)
	if sc == nil || sc.started == nil {
		return ret, nil, err
	}
	if err != nil {
		// Probe keeps no state a failed step returns, so the browser it
		// started could not be reached again: the next step with session
		// starts another.
		discardSession(sc.started)
		return ret, nil, err
	}
	return ret, sc.started.state(), nil
}

// Cleanup stops the browsers the steps of the run kept.
func (a *Action) Cleanup(call actionrpc.CleanupCall) error {
	return cleanupSessions(call.RunID)
}

func (a *Action) run(with map[string]any, sc *sessionCall) (map[string]any, error) {
	actionrpc.LogParams(a.log, "received browser action request", with)

	within := WithInBrowser(func(s string, i ...any) {
		a.log.Debug("chromedp", "message", fmt.Sprintf(s, i...))
	})
	before := WithBefore(func(req *Req) {
		a.log.Debug("chromedp request prepared", "request", req)
	})
	after := WithAfter(func(res *Res) {
		a.log.Debug("chromedp response received", "response", res)
	})

	ret, err := request(with, sc, within, before, after)

	actionrpc.LogOutcome(a.log, "browser request", ret, err)

	return ret, err
}

func Serve() {
	actionrpc.Serve(func(log hclog.Logger) actionrpc.Action {
		log.Debug("Starting browser action server")
		return &Action{log: log}
	})
}

// Params returns the keys the browser action takes in with: those of Req,
// and timeout and virtual_authenticator, which it reads apart from Req.
func Params() []string {
	return append(actionrpc.ParamNames(Req{}), "timeout", "virtual_authenticator")
}
