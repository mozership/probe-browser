package browser

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// textPollInterval is how often wait_text reads the text again while it is
// not the one expected.
const textPollInterval = 100 * time.Millisecond

// textMatch is the text a wait_text waits for.
type textMatch struct {
	equals   string
	contains string
	matches  *regexp.Regexp
}

// textMatch returns what the action's wait_text waits for, or nil when it
// waits only for the element to be visible. Setting more than one of equals,
// contains and matches is refused, since a text that meets one of them and
// not the other would leave which one counts unclear.
func (a *ChromeDPAction) textMatch() (*textMatch, error) {
	set := 0
	for _, s := range []string{a.Equals, a.Contains, a.Matches} {
		if s != "" {
			set++
		}
	}
	switch {
	case set == 0:
		return nil, nil
	case set > 1:
		return nil, fmt.Errorf("wait_text takes only one of equals, contains and matches")
	}

	m := &textMatch{equals: a.Equals, contains: a.Contains}
	if a.Matches != "" {
		re, err := regexp.Compile(a.Matches)
		if err != nil {
			return nil, fmt.Errorf("wait_text matches %q is not a regular expression: %w", a.Matches, err)
		}
		m.matches = re
	}
	return m, nil
}

func (m *textMatch) ok(text string) bool {
	switch {
	case m.matches != nil:
		return m.matches.MatchString(text)
	case m.contains != "":
		return strings.Contains(text, m.contains)
	default:
		return text == m.equals
	}
}

func (m *textMatch) String() string {
	switch {
	case m.matches != nil:
		return fmt.Sprintf("want it to match %q", m.matches.String())
	case m.contains != "":
		return fmt.Sprintf("want it to contain %q", m.contains)
	default:
		return fmt.Sprintf("want %q", m.equals)
	}
}

// waitText returns an action that reads the visible text of sel until it is
// the text m expects, and keeps that text in dst.
func waitText(sel string, m *textMatch, dst *string) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		text, err := pollText(ctx, sel, m, func(ctx context.Context) (string, error) {
			return chromedp.Text(chromedp.CSS(sel), chromedp.NodeVisible)(ctx, t)
		})
		if err != nil {
			return chromedp.Void{}, err
		}
		*dst = text
		return chromedp.Void{}, nil
	}
}

// pollText calls read until the text it returns is the one m expects, or ctx
// ends. read waits for the element to be visible, so a page that renders it
// late is waited for too. When ctx ends, the error gives the last text read,
// which is what a failure is diagnosed from.
func pollText(ctx context.Context, sel string, m *textMatch, read func(context.Context) (string, error)) (string, error) {
	last, seen := "", false
	for {
		text, err := read(ctx)
		if err == nil {
			if m.ok(text) {
				return text, nil
			}
			last, seen = text, true
		} else if ctx.Err() == nil {
			return "", err
		}

		select {
		case <-ctx.Done():
			if !seen {
				return "", fmt.Errorf("wait_text %q: no visible element matched, %s: %w", sel, m, ctx.Err())
			}
			return "", fmt.Errorf("wait_text %q: the text was %q, %s: %w", sel, last, m, ctx.Err())
		case <-time.After(textPollInterval):
		}
	}
}
