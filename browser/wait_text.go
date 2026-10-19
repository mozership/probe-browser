package browser

import (
	"context"
	"fmt"
	"regexp"
	"slices"
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

// markGivenTextFields records, on each action, which of equals, contains and
// matches the workflow wrote as a string. The mapper leaves a field empty
// both when it is written as "" and when it is left out, so without this
// equals: "" would not wait for an empty text but skip the check.
func markGivenTextFields(data map[string]any, actions []*ChromeDPAction) {
	raw, ok := data["actions"].([]any)
	if !ok {
		return
	}
	for i, a := range raw {
		action, ok := a.(map[string]any)
		if !ok || i >= len(actions) {
			continue
		}
		for _, key := range []string{"equals", "contains", "matches"} {
			if _, isString := action[key].(string); isString {
				actions[i].given = append(actions[i].given, key)
			}
		}
	}
}

// textFields returns which of equals, contains and matches the action sets:
// those not empty, and those the workflow wrote as "".
func (a *ChromeDPAction) textFields() []string {
	var fields []string
	for _, f := range []struct{ key, value string }{
		{"equals", a.Equals}, {"contains", a.Contains}, {"matches", a.Matches},
	} {
		if f.value != "" || slices.Contains(a.given, f.key) {
			fields = append(fields, f.key)
		}
	}
	return fields
}

// textMatch returns what the action's wait_text waits for, or nil when it
// waits only for the element to be visible. Setting more than one of equals,
// contains and matches is refused, since a text that meets one of them and
// not the other would leave which one counts unclear.
func (a *ChromeDPAction) textMatch() (*textMatch, error) {
	fields := a.textFields()
	switch {
	case len(fields) == 0:
		return nil, nil
	case len(fields) > 1:
		return nil, fmt.Errorf("wait_text takes only one of equals, contains and matches")
	}

	// An empty contains or matches accepts any text, so the step would pass
	// whatever the page shows.
	switch fields[0] {
	case "contains":
		if a.Contains == "" {
			return nil, fmt.Errorf("wait_text contains is empty, which any text contains: use equals: \"\" to wait for an empty text")
		}
	case "matches":
		if a.Matches == "" {
			return nil, fmt.Errorf("wait_text matches is empty, which any text matches: use '^$' to wait for an empty text")
		}
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
// late is waited for too. An error from read is not the end either: a page
// that renders again can replace the element between finding it and reading
// it, and the next read finds the new one. When ctx ends, the error gives
// the last text read, or the last error when no text was, which is what a
// failure is diagnosed from.
func pollText(ctx context.Context, sel string, m *textMatch, read func(context.Context) (string, error)) (string, error) {
	last, seen := "", false
	var lastErr error
	for {
		text, err := read(ctx)
		switch {
		case err == nil:
			if m.ok(text) {
				return text, nil
			}
			last, seen = text, true
		case ctx.Err() == nil:
			lastErr = err
		}

		select {
		case <-ctx.Done():
			switch {
			case seen:
				return "", fmt.Errorf("wait_text %q: the text was %q, %s: %w", sel, last, m, ctx.Err())
			case lastErr != nil:
				return "", fmt.Errorf("wait_text %q: the text could not be read (%v), %s: %w", sel, lastErr, m, ctx.Err())
			default:
				return "", fmt.Errorf("wait_text %q: no visible element matched, %s: %w", sel, m, ctx.Err())
			}
		case <-time.After(textPollInterval):
		}
	}
}
