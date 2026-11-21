// Command probe-browser serves a Probe action that drives a browser with
// chromedp. Probe starts it for a step that uses
// github.com/mozership/probe-browser@<commit>.
package main

import (
	"os"

	"github.com/mozership/probe-browser/browser"
)

// version is set at build time.
var version = "dev" //nolint:unused // set by -X main.version in .goreleaser

func main() {
	// A kept browser's recorder is this executable, started by the step
	// that starts the browser.
	if browser.IsRecorder() {
		os.Exit(browser.RunRecorder())
	}
	browser.Serve()
}
