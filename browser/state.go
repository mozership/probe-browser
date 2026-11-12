package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chromedp/chromedp"

	"github.com/mozership/probe-browser/storagestate"
)

// storageState is what save_storage_state writes and storage_state reads.
// It is kept in package storagestate, which probe-agent shares.
type (
	storageState = storagestate.State
	stateCookie  = storagestate.Cookie
	stateOrigin  = storagestate.Origin
	stateItem    = storagestate.Item
)

// readStorageState reads the file storage_state names. It is read before
// the browser starts, so that a missing file, as when the step that writes
// it failed, fails the step at once.
func readStorageState(path string) (*storageState, error) {
	s, err := storagestate.Read(path)
	if err != nil {
		return nil, fmt.Errorf("storage_state: %w", err)
	}
	return s, nil
}

// loadStorageState returns an action that gives the browser the cookies and
// the localStorage of s, before any page is opened.
func loadStorageState(s *storageState) chromedp.Action[chromedp.Void] {
	return wrapAction("storage_state", storagestate.Load(s))
}

// saveStorageState returns an action that reads the cookies of the browser
// and the localStorage of the page open, and keeps them in dst.
func saveStorageState(dst *storageState) chromedp.Action[chromedp.Void] {
	return wrapAction("save_storage_state", storagestate.Save(dst))
}

// wrapAction puts the name of what failed before the error of a.
func wrapAction(name string, a chromedp.Action[chromedp.Void]) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		v, err := a(ctx, t)
		if err != nil {
			return v, fmt.Errorf("%s: %w", name, err)
		}
		return v, nil
	}
}

// writeStorageState writes s to path, or to a new file in the temporary
// directory when path is empty, and returns where it went. The file holds
// the session cookies, which sign in as the user, so only the user who ran
// Probe can read it.
func writeStorageState(s *storageState, path string) (string, error) {
	return writePrivateJSON(s, path, "probe-browser-state-*.json")
}

// writePrivateJSON writes v as indented JSON to path, or to a new file in
// the temporary directory named after pattern when path is empty, and
// returns where it went. Only the user who ran Probe can read the file, as
// what is written signs in as the user.
func writePrivateJSON(v any, path, pattern string) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')

	if path == "" {
		f, err := os.CreateTemp("", pattern)
		if err != nil {
			return "", err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return f.Name(), err
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
	}
	// OpenFile keeps the mode of a file that is there already, so the mode
	// of one an earlier run wrote with another is set before it is written.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return "", err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return path, err
}
