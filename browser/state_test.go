package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var testState = &storageState{
	Cookies: []stateCookie{{
		Name: "session", Value: "abc123", Domain: "localhost", Path: "/",
		Expires: -1, HTTPOnly: true, SameSite: "Lax",
	}},
	Origins: []stateOrigin{{
		Origin:       "http://localhost:8090",
		LocalStorage: []stateItem{{Name: "token", Value: "t-42"}},
	}},
}

func TestWriteStorageState_ReadsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth", "state.json")

	got, err := writeStorageState(testState, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != path {
		t.Errorf("path = %s, want %s", got, path)
	}

	read, err := readStorageState(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(read, testState) {
		t.Errorf("read back\n%#v\nwant\n%#v", read, testState)
	}
}

func TestWriteStorageState_PlaywrightShape(t *testing.T) {
	path, err := writeStorageState(testState, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, _ := os.ReadFile(path)

	// The keys are those of Playwright's storage state, so that either tool
	// reads what the other writes.
	var raw struct {
		Cookies []map[string]any `json:"cookies"`
		Origins []map[string]any `json:"origins"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	for _, key := range []string{"name", "value", "domain", "path", "expires", "httpOnly", "secure", "sameSite"} {
		if _, ok := raw.Cookies[0][key]; !ok {
			t.Errorf("the cookie has no %q: %s", key, data)
		}
	}
	for _, key := range []string{"origin", "localStorage"} {
		if _, ok := raw.Origins[0][key]; !ok {
			t.Errorf("the origin has no %q: %s", key, data)
		}
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Error("the file should end with a newline")
	}
}

func TestWriteStorageState_OnlyTheUserReadsIt(t *testing.T) {
	t.Run("a new file", func(t *testing.T) {
		path, err := writeStorageState(testState, filepath.Join(t.TempDir(), "state.json"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600: the file signs in as the user", info.Mode().Perm())
		}
	})

	t.Run("a file an earlier run left readable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := writeStorageState(testState, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
	})

	t.Run("no path given", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		path, err := writeStorageState(testState, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(filepath.Base(path), "probe-browser-state-") || filepath.Ext(path) != ".json" {
			t.Errorf("path = %s, want a probe-browser-state-*.json in the temporary directory", path)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
	})
}

func TestSetLocalStorage_NothingToKeep(t *testing.T) {
	// An origin without items opens no page, so no target is needed.
	origins := []stateOrigin{{Origin: "http://localhost:8090", LocalStorage: []stateItem{}}}
	if err := setLocalStorage(t.Context(), nil, origins); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReadStorageState_Refused(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("cookies"), 0o600); err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		name string
		path string
		want string
	}{
		{"a missing file", filepath.Join(dir, "missing.json"), "storage_state: open"},
		{"not JSON", bad, "is not a storage state"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readStorageState(tc.path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestBuildActionTasks_StorageState(t *testing.T) {
	t.Run("a missing file fails before the browser starts", func(t *testing.T) {
		req := NewReq()
		req.StorageState = filepath.Join(t.TempDir(), "missing.json")
		req.Actions = []*ChromeDPAction{{Name: "navigate", URL: "http://app.test/"}}
		mock := NewMockRunner()
		req.browserRunner = mock

		_, err := req.do()
		if err == nil || !strings.Contains(err.Error(), "storage_state") {
			t.Fatalf("error = %v, want the file refused", err)
		}
		if mock.GetCallCount() != 0 {
			t.Error("no action should run")
		}
	})

	t.Run("the state is loaded before the first action", func(t *testing.T) {
		path, err := writeStorageState(testState, filepath.Join(t.TempDir(), "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		req := NewReq()
		req.StorageState = path
		req.Actions = []*ChromeDPAction{{Name: "navigate", URL: "http://app.test/"}}

		tasks, err := req.buildActionTasks()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tasks) != 2 {
			t.Errorf("got %d tasks, want the load and the navigate", len(tasks))
		}
	})
}

func TestCollectResults_SaveStorageState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	action := &ChromeDPAction{Name: "save_storage_state", ID: "login", Path: path, reState: testState}
	req := NewReq()
	req.Actions = []*ChromeDPAction{action}

	_, filePaths, err := req.collectResults()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filePaths["login"] != path {
		t.Errorf("filepaths = %v, want login at %s", filePaths, path)
	}
	if _, err := readStorageState(path); err != nil {
		t.Errorf("the file should read back: %v", err)
	}
}
