// Package outline reads a page as an outline: lines that each name an
// element by its role and its accessible name, and end with a ref, such as
//
//	textbox "Email" type=email required [ref_3]
//
// and the text the page shows, the text of open shadow roots included. A
// ref names its element while the element is in the document, so a later
// read or step that drives the same document finds it again. Reading
// writes nothing to the page.
//
// probe-browser's read_page and page_text read the page with it. It is a
// package of its own for another Go tool that drives Chrome, such as
// probe-agent, to read a page as they do.
package outline

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chromedp/chromedp"
)

// Script installs window.__probeOutline in the document it is evaluated in,
// once: evaluating it again does nothing.
//
//go:embed outline.js
var Script string

// DefaultDepth is how far containers nest in a read given no depth.
const DefaultDepth = 15

// The filters of Read.
const (
	// Viewport reads the elements shown in the viewport.
	Viewport = ""
	// All reads the elements shown, in the viewport or out of it.
	All = "all"
	// Interactive reads the elements shown in the viewport that one can
	// act on.
	Interactive = "interactive"
)

// Expr returns an expression that calls fn of window.__probeOutline with
// args, evaluating Script first, as a document can lack it: one opened
// before it was evaluated in it, or one a navigation replaced.
func Expr(fn string, args ...any) (string, error) {
	encoded := make([]string, len(args))
	for i, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			return "", err
		}
		encoded[i] = string(b)
	}
	return Script + ";\nwindow.__probeOutline." + fn + "(" + strings.Join(encoded, ", ") + ")", nil
}

// CheckFilter returns an error for a filter Read does not take.
func CheckFilter(filter string) error {
	switch filter {
	case Viewport, All, Interactive:
		return nil
	}
	return fmt.Errorf("the filter is %q, but it is all, interactive, or none for the viewport", filter)
}

// Read returns an action that reads the page as lines: the elements shown
// in the viewport, or those filter names; depth bounds how far containers
// nest, DefaultDepth when it is less than 1; and ref reads only the element
// of that ref and what it holds. A page longer than 50,000 characters is
// cut, with a line that says so.
func Read(filter string, depth int, ref string) chromedp.Action[string] {
	if err := CheckFilter(filter); err != nil {
		return failed[string](err)
	}
	if depth < 1 {
		depth = DefaultDepth
	}
	return eval[string]("read", filter, depth, ref)
}

// Text returns an action that reads the text the page shows, its main
// content first, the text of open shadow roots included.
func Text() chromedp.Action[string] {
	return eval[string]("text")
}

func eval[T any](fn string, args ...any) chromedp.Action[T] {
	expr, err := Expr(fn, args...)
	if err != nil {
		return failed[T](err)
	}
	return chromedp.Evaluate[T](expr)
}

func failed[T any](err error) chromedp.Action[T] {
	return func(_ context.Context, _ *chromedp.Target) (T, error) {
		var zero T
		return zero, err
	}
}
