package browser

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// consoleMock is a MockRunner whose page wrote messages to its console.
// Without stream, the messages end after those given, as when the page
// closed; with it, they go on until the context ends, as a live page's do.
type consoleMock struct {
	*MockRunner
	messages []chromedp.ConsoleMessage
	stream   bool
}

func (m *consoleMock) Console(ctx context.Context) iter.Seq2[chromedp.ConsoleMessage, error] {
	return func(yield func(chromedp.ConsoleMessage, error) bool) {
		for _, msg := range m.messages {
			if !yield(msg, nil) {
				return
			}
		}
		if m.stream {
			<-ctx.Done()
			yield(chromedp.ConsoleMessage{}, ctx.Err())
		}
	}
}

var (
	boot = chromedp.ConsoleMessage{
		Type: chromedp.ConsoleError, Text: "boot: config failed",
		URL: "http://app.test/app.js", Line: 2, Column: 4,
	}
	uncaught = chromedp.ConsoleMessage{
		Type: chromedp.ConsoleException, Text: "Uncaught TypeError: x is not a function",
		URL: "http://app.test/app.js", Line: 9, Column: 0,
	}
	endMark = chromedp.ConsoleMessage{Type: chromedp.ConsoleDebug, Text: consoleEnd}
)

func TestConsoleError(t *testing.T) {
	testCases := []struct {
		name string
		m    chromedp.ConsoleMessage
		want ConsoleError
		ok   bool
	}{
		{"console.error", boot, ConsoleError{Type: "error", Text: "boot: config failed", URL: "http://app.test/app.js", Line: 3, Column: 5}, true},
		{"an uncaught exception", uncaught, ConsoleError{Type: "exception", Text: "Uncaught TypeError: x is not a function", URL: "http://app.test/app.js", Line: 10, Column: 1}, true},
		{"a failed console.assert", chromedp.ConsoleMessage{Type: "assert", Text: "Assertion failed: x"}, ConsoleError{Type: "assert", Text: "Assertion failed: x"}, true},
		{"console.log", chromedp.ConsoleMessage{Type: chromedp.ConsoleLog, Text: "hello"}, ConsoleError{}, false},
		{"console.warn", chromedp.ConsoleMessage{Type: chromedp.ConsoleWarning, Text: "careful"}, ConsoleError{}, false},
		{"the browser's own error", chromedp.ConsoleMessage{Type: chromedp.ConsoleError, Text: "Failed to load resource", Source: "network"}, ConsoleError{}, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := consoleError(tc.m)
			if ok != tc.ok || got != tc.want {
				t.Errorf("consoleError() = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestWithConsoleErrors(t *testing.T) {
	base := errors.New("click \"#buy\": timeout")

	t.Run("none leaves the error as it was", func(t *testing.T) {
		if got := withConsoleErrors(base, nil); got != base {
			t.Errorf("error = %v", got)
		}
	})

	t.Run("names them with where they came from", func(t *testing.T) {
		got := withConsoleErrors(base, []ConsoleError{{Text: "Uncaught TypeError: x", URL: "http://app.test/app.js", Line: 3, Column: 5}, {Text: "no place"}})
		want := `click "#buy": timeout (console errors: "Uncaught TypeError: x (http://app.test/app.js:3:5)", "no place")`
		if got.Error() != want {
			t.Errorf("error = %s\nwant    %s", got, want)
		}
		if !errors.Is(got, base) {
			t.Error("the error should wrap the actions' one")
		}
	})

	t.Run("counts the ones past the first few", func(t *testing.T) {
		errs := make([]ConsoleError, maxConsoleErrors+2)
		for i := range errs {
			errs[i] = ConsoleError{Text: "e"}
		}
		if got := withConsoleErrors(base, errs).Error(); !strings.HasSuffix(got, `"e" and 2 more)`) || strings.Count(got, `"e"`) != maxConsoleErrors {
			t.Errorf("error = %s", got)
		}
	})
}

func TestDo_ConsoleErrors(t *testing.T) {
	t.Run("the result has them, and nothing past the end mark", func(t *testing.T) {
		mock := &consoleMock{MockRunner: NewMockRunner(), messages: []chromedp.ConsoleMessage{
			{Type: chromedp.ConsoleLog, Text: "ready"}, boot, uncaught, endMark,
			{Type: chromedp.ConsoleError, Text: "after the actions"},
		}}
		req := NewReq()
		req.browserRunner = mock
		req.Actions = []*ChromeDPAction{{Name: "navigate", URL: "http://app.test/"}}

		ret, err := req.do()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := ret.Res.ConsoleErrors
		if len(got) != 2 || got[0].Text != boot.Text || got[1].Type != "exception" {
			t.Errorf("console errors = %+v, want the console.error and the exception", got)
		}
		// The end mark is written through the runner, after the actions.
		if calls := mock.GetCallCount(); calls != 2 {
			t.Errorf("got %d runs, want the actions and the end mark", calls)
		}
	})

	t.Run("a page with none gives an empty list", func(t *testing.T) {
		req := NewReq()
		req.browserRunner = &consoleMock{MockRunner: NewMockRunner()}
		req.Actions = []*ChromeDPAction{{Name: "navigate", URL: "http://app.test/"}}

		ret, err := req.do()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ret.Res.ConsoleErrors == nil || len(ret.Res.ConsoleErrors) != 0 {
			t.Errorf("console errors = %#v, want an empty list", ret.Res.ConsoleErrors)
		}
	})

	t.Run("a failure names them", func(t *testing.T) {
		mock := &consoleMock{MockRunner: NewMockRunner(), messages: []chromedp.ConsoleMessage{uncaught}}
		calls := 0
		mock.SetRunFunc(func(ctx context.Context, actions ...chromedp.Action[chromedp.Void]) error {
			calls++
			if calls == 1 {
				return errors.New("click \"#buy\": timeout")
			}
			return nil
		})
		req := NewReq()
		req.browserRunner = mock
		req.capture = func(ctx context.Context) ([]byte, string, string, error) {
			return nil, "", "", errors.New("no page")
		}
		req.Actions = []*ChromeDPAction{{Name: "click", Selector: "#buy"}}

		_, err := req.do()
		if err == nil || !strings.Contains(err.Error(), `console errors: "Uncaught TypeError: x is not a function (http://app.test/app.js:10:1)"`) {
			t.Errorf("error = %v, want it to name the exception", err)
		}
	})

	t.Run("a page that never sends the end mark is not waited for", func(t *testing.T) {
		mock := &consoleMock{MockRunner: NewMockRunner(), messages: []chromedp.ConsoleMessage{boot}, stream: true}
		req := NewReq()
		req.browserRunner = mock
		req.Actions = []*ChromeDPAction{{Name: "navigate", URL: "http://app.test/"}}

		start := time.Now()
		ret, err := req.do()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if elapsed := time.Since(start); elapsed > consoleDrain+time.Second {
			t.Errorf("took %v, want about %v at most", elapsed, consoleDrain)
		}
		if len(ret.Res.ConsoleErrors) != 1 {
			t.Errorf("console errors = %+v, want the one read", ret.Res.ConsoleErrors)
		}
	})

	t.Run("a runner that reads no console gives an empty list", func(t *testing.T) {
		req := NewReq()
		req.browserRunner = NewMockRunner()
		req.Actions = []*ChromeDPAction{{Name: "navigate", URL: "http://app.test/"}}

		ret, err := req.do()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ret.Res.ConsoleErrors) != 0 {
			t.Errorf("console errors = %+v", ret.Res.ConsoleErrors)
		}
	})
}
