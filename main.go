// Command probe-browser serves a Probe action that drives a browser with
// chromedp. Probe starts it for a step that uses
// github.com/mozership/probe-browser@<commit>.
package main

import "github.com/mozership/probe-browser/browser"

// version is set at build time.
var version = "dev"

func main() {
	browser.Serve()
}
