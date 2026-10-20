package browser

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// ConsoleError is an error the page wrote to its console while the actions
// ran: a call of console.error, a console.assert that failed, an exception
// nothing caught, or a promise rejection nothing handled. The browser's own
// messages, such as a resource that failed to load, are not among them.
type ConsoleError struct {
	// Type is "error", "assert" or "exception".
	Type string `map:"type"`
	// Text is what the DevTools console shows. An exception's begins with
	// "Uncaught", or "Uncaught (in promise)" for a rejection.
	Text string `map:"text"`
	// URL, Line and Column are where it came from, the line and the column
	// counted from 1 as DevTools shows them. They are empty or 0 when the
	// browser does not say.
	URL    string `map:"url"`
	Line   int64  `map:"line"`
	Column int64  `map:"column"`
}

// String returns the text and where it came from.
func (e ConsoleError) String() string {
	if e.URL == "" {
		return e.Text
	}
	return fmt.Sprintf("%s (%s:%d:%d)", e.Text, e.URL, e.Line, e.Column)
}

// consoleError returns the error m is, and false for a message that is not
// one.
func consoleError(m chromedp.ConsoleMessage) (ConsoleError, bool) {
	// An entry of the browser log has a source; it is the browser speaking,
	// not the page.
	if m.Source != "" {
		return ConsoleError{}, false
	}
	switch m.Type {
	case chromedp.ConsoleError, "assert", chromedp.ConsoleException:
	default:
		return ConsoleError{}, false
	}
	e := ConsoleError{Type: string(m.Type), Text: m.Text, URL: m.URL}
	if m.URL != "" {
		e.Line, e.Column = m.Line+1, m.Column+1
	}
	return e, true
}

// consoleEnd is the text of the message that marks the end of the messages
// written while the actions ran.
const consoleEnd = "probe-browser: end of the actions"

// consoleDrain bounds waiting for the messages to be read once the actions
// have run, as for a page that replaced console.debug and never sends the
// mark.
const consoleDrain = time.Second

// consoleRecorder keeps the console errors of the page from when it starts.
type consoleRecorder struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	errs   []ConsoleError
}

// recordConsole starts reading messages until it reads the end mark, they
// end, or ctx ends.
func recordConsole(ctx context.Context, messages func(context.Context) iter.Seq2[chromedp.ConsoleMessage, error]) *consoleRecorder {
	ctx, cancel := context.WithCancel(ctx)
	r := &consoleRecorder{cancel: cancel, done: make(chan struct{})}
	seq := messages(ctx)
	go func() {
		defer close(r.done)
		for m, err := range seq {
			if err != nil {
				return
			}
			if m.Type == chromedp.ConsoleDebug && m.Text == consoleEnd {
				return
			}
			if e, ok := consoleError(m); ok {
				r.mu.Lock()
				r.errs = append(r.errs, e)
				r.mu.Unlock()
			}
		}
	}()
	return r
}

// stop has the page write the end mark through run, and returns the errors
// read before it; a nil recorder, as for a runner that reads no console, has
// none. The browser sends the messages in order, so every one the
// actions caused comes before the mark.
func (r *consoleRecorder) stop(ctx context.Context, run func(context.Context, ...chromedp.Action[chromedp.Void]) error) []ConsoleError {
	if r == nil {
		return []ConsoleError{}
	}
	ctx, cancel := context.WithTimeout(ctx, consoleDrain)
	defer cancel()

	mark := chromedp.Evaluate[chromedp.Void](fmt.Sprintf("console.debug(%s)", jsString(consoleEnd)))
	if run(ctx, mark) == nil {
		select {
		case <-r.done:
		case <-ctx.Done():
		}
	}
	r.cancel()
	<-r.done

	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ConsoleError{}, r.errs...)
}

// maxConsoleErrors is how many console errors a failure's message names.
const maxConsoleErrors = 5

// withConsoleErrors adds the console errors to the error of failed actions,
// as one of them is often why an action failed.
func withConsoleErrors(err error, errs []ConsoleError) error {
	if len(errs) == 0 {
		return err
	}
	shown := make([]string, 0, maxConsoleErrors)
	for _, e := range errs[:min(len(errs), maxConsoleErrors)] {
		shown = append(shown, fmt.Sprintf("%q", e.String()))
	}
	more := ""
	if n := len(errs) - len(shown); n > 0 {
		more = fmt.Sprintf(" and %d more", n)
	}
	return fmt.Errorf("%w (console errors: %s%s)", err, strings.Join(shown, ", "), more)
}
