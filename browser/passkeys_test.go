package browser

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/webauthn"
)

// testKey returns a private key of curve in PKCS#8.
func testKey(t *testing.T, curve elliptic.Curve) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func testCredential(t *testing.T) *webauthn.Credential {
	return &webauthn.Credential{
		CredentialID:         []byte{0xfb, 0xff, 0x01, 0x02},
		IsResidentCredential: true,
		RpID:                 "localhost",
		PrivateKey:           testKey(t, elliptic.P256()),
		UserHandle:           []byte("user-7"),
		SignCount:            3,
		UserName:             "user-7",
		UserDisplayName:      "User 7",
	}
}

// writeTestPasskeys writes passkeys as a passkey file and returns its path.
func writeTestPasskeys(t *testing.T, passkeys ...any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"passkeys": passkeys})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "passkeys.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWritePasskeys_ReadsBack(t *testing.T) {
	c := testCredential(t)
	f := &passkeyFile{Passkeys: []passkey{passkeyOf(c)}}
	path := filepath.Join(t.TempDir(), "auth", "passkeys.json")

	got, err := writePasskeys(f, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != path {
		t.Errorf("path = %s, want %s", got, path)
	}
	read, err := readPasskeys(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(read, []*webauthn.Credential{c}) {
		t.Errorf("read back\n%#v\nwant\n%#v", read[0], c)
	}
}

func TestWritePasskeys_Shape(t *testing.T) {
	f := &passkeyFile{Passkeys: []passkey{passkeyOf(testCredential(t))}}
	path, err := writePasskeys(f, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(path)

	if !strings.HasPrefix(filepath.Base(path), "probe-browser-passkeys-") {
		t.Errorf("path = %s, want a probe-browser-passkeys-*.json", path)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want only the user to read it", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	var raw struct {
		Passkeys []map[string]any `json:"passkeys"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	// Bytes are in base64url without padding, as relying parties keep them.
	if got := raw.Passkeys[0]["credentialId"]; got != "-_8BAg" {
		t.Errorf("credentialId = %v, want base64url", got)
	}
	if got := raw.Passkeys[0]["userHandle"]; got != base64.RawURLEncoding.EncodeToString([]byte("user-7")) {
		t.Errorf("userHandle = %v, want base64url", got)
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Error("the file should end with a newline")
	}
}

func TestReadPasskeys_WrittenByHand(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(testKey(t, elliptic.P256()))
	// A file written by hand may keep the padding, and may leave out
	// isResidentCredential, as every passkey is one.
	path := writeTestPasskeys(t, map[string]any{
		"credentialId": "AQI=",
		"rpId":         "example.com",
		"privateKey":   strings.NewReplacer("+", "-", "/", "_").Replace(key),
		"signCount":    -1,
	})
	creds, err := readPasskeys(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(creds) != 1 || !creds[0].IsResidentCredential || creds[0].SignCount != -1 || string(creds[0].CredentialID) != "\x01\x02" {
		t.Errorf("got %#v", creds[0])
	}
}

func TestReadPasskeys_Refused(t *testing.T) {
	good := passkeyOf(testCredential(t))
	with := func(f func(p map[string]any)) map[string]any {
		data, _ := json.Marshal(good)
		var p map[string]any
		_ = json.Unmarshal(data, &p)
		f(p)
		return p
	}
	p384Key := base64.RawURLEncoding.EncodeToString(testKey(t, elliptic.P384()))

	tests := []struct {
		name    string
		passkey map[string]any
		want    string
	}{
		{"no credential ID", with(func(p map[string]any) { delete(p, "credentialId") }), "credentialId is missing"},
		{"a credential ID not in base64url", with(func(p map[string]any) { p["credentialId"] = "a+b/" }), "credentialId is not base64url"},
		{"no rpId", with(func(p map[string]any) { delete(p, "rpId") }), "rpId is missing"},
		{"no private key", with(func(p map[string]any) { delete(p, "privateKey") }), "privateKey is missing"},
		{"a private key that is not a key", with(func(p map[string]any) { p["privateKey"] = "AQID" }), "privateKey is not a PKCS#8 key"},
		{"a key of another curve", with(func(p map[string]any) { p["privateKey"] = p384Key }), "want an ECDSA P-256 key"},
		{"a user handle not in base64url", with(func(p map[string]any) { p["userHandle"] = "a b" }), "userHandle is not base64url"},
		{"a counter below -1", with(func(p map[string]any) { p["signCount"] = -2 }), "signCount is -2"},
		{"backed up without eligibility", with(func(p map[string]any) { p["backupState"] = true }), "backupState needs backupEligibility"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readPasskeys(writeTestPasskeys(t, good, tc.passkey))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to say %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "passkey 1: ") {
				t.Errorf("error = %v, want it to name the passkey", err)
			}
		})
	}

	t.Run("a missing file", func(t *testing.T) {
		_, err := readPasskeys(filepath.Join(t.TempDir(), "missing.json"))
		if err == nil || !strings.HasPrefix(err.Error(), "passkeys: ") {
			t.Errorf("error = %v, want the file refused", err)
		}
	})
	t.Run("a file that is not a passkey file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.json")
		_ = os.WriteFile(path, []byte(`[1, 2]`), 0o600)
		_, err := readPasskeys(path)
		if err == nil || !strings.Contains(err.Error(), "is not a passkey file") {
			t.Errorf("error = %v, want the file refused", err)
		}
	})
}

func TestBuildActionTasks_Passkeys(t *testing.T) {
	navigate := &ChromeDPAction{Name: "navigate", URL: "http://localhost/"}

	t.Run("passkeys alone starts an authenticator", func(t *testing.T) {
		req := NewReq()
		req.Passkeys = writeTestPasskeys(t, passkeyOf(testCredential(t)))
		req.Actions = []*ChromeDPAction{navigate, {Name: "save_passkeys"}}

		tasks, err := req.buildActionTasks()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tasks) != 3 {
			t.Errorf("got %d tasks, want the authenticator, the navigate and the save", len(tasks))
		}
	})

	t.Run("a missing file fails before the browser starts", func(t *testing.T) {
		req := NewReq()
		req.VirtualAuthenticator = defaultAuthenticatorOptions()
		req.Passkeys = filepath.Join(t.TempDir(), "missing.json")
		req.Actions = []*ChromeDPAction{navigate}
		mock := NewMockRunner()
		req.browserRunner = mock

		_, err := req.do()
		if err == nil || !strings.Contains(err.Error(), "passkeys") {
			t.Fatalf("error = %v, want the file refused", err)
		}
		if mock.GetCallCount() != 0 {
			t.Error("no action should run")
		}
	})

	t.Run("save_passkeys needs an authenticator", func(t *testing.T) {
		req := NewReq()
		req.Actions = []*ChromeDPAction{navigate, {Name: "save_passkeys"}}

		_, err := req.buildActionTasks()
		if err == nil || !strings.Contains(err.Error(), "save_passkeys needs virtual_authenticator or passkeys") {
			t.Errorf("error = %v, want save_passkeys refused", err)
		}
	})
}

func TestParseData_PasskeysWithoutAuthenticator(t *testing.T) {
	req := NewReq()
	err := req.parseData(map[string]any{
		"actions":               []any{map[string]any{"name": "navigate", "url": "http://localhost/"}},
		"passkeys":              "passkeys.json",
		"virtual_authenticator": false,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "passkeys needs a virtual authenticator") {
		t.Errorf("error = %v, want passkeys refused", err)
	}
}

func TestCollectResults_SavePasskeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passkeys.json")
	f := &passkeyFile{Passkeys: []passkey{passkeyOf(testCredential(t))}}
	req := NewReq()
	req.Actions = []*ChromeDPAction{{Name: "save_passkeys", ID: "keys", Path: path, rePasskeys: f}}

	_, filePaths, err := req.collectResults()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filePaths["keys"] != path {
		t.Errorf("filepaths = %v, want keys at %s", filePaths, path)
	}
	if _, err := readPasskeys(path); err != nil {
		t.Errorf("the file should read back: %v", err)
	}
}

func TestAddCredentialParams_NoCMTG(t *testing.T) {
	data, err := json.Marshal(newAddCredentialParams("auth-1", testCredential(t)))
	if err != nil {
		t.Fatal(err)
	}
	// The browser refuses a credential that names the cmtgKey extension from
	// an authenticator without it, even as false.
	if strings.Contains(strings.ToLower(string(data)), "cmtg") {
		t.Errorf("the params name the cmtgKey extension: %s", data)
	}
	var raw struct {
		AuthenticatorID string `json:"authenticatorId"`
		Credential      struct {
			CredentialID string `json:"credentialId"`
			RpID         string `json:"rpId"`
			UserHandle   string `json:"userHandle"`
		} `json:"credential"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	// The protocol takes its binary values in base64.
	if raw.AuthenticatorID != "auth-1" || raw.Credential.CredentialID != "+/8BAg==" || raw.Credential.RpID != "localhost" || raw.Credential.UserHandle != "dXNlci03" {
		t.Errorf("params = %s", data)
	}
}
