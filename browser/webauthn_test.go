package browser

import (
	"reflect"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/webauthn"
)

func TestParseVirtualAuthenticator(t *testing.T) {
	defaults := defaultAuthenticatorOptions()

	tests := []struct {
		name string
		in   any
		want *authenticatorOptions
	}{
		{"true gives the defaults", true, defaults},
		{"a string from a template", "true", defaults},
		{"false gives none", false, nil},
		{"a string false gives none", "false", nil},
		{"an empty map gives the defaults", map[string]any{}, defaults},
		{
			"a map changes the options it names",
			map[string]any{"transport": "usb", "resident_key": false, "backup_eligible": true, "backed_up": "true"},
			&authenticatorOptions{Transport: "usb", ResidentKey: false, UserVerification: true, UserVerified: true, BackupEligible: true, BackedUp: true},
		},
		{
			"user_verified follows user_verification",
			map[string]any{"user_verification": false},
			&authenticatorOptions{Transport: "internal", ResidentKey: true},
		},
		{
			"user verification that fails",
			map[string]any{"user_verified": false},
			&authenticatorOptions{Transport: "internal", ResidentKey: true, UserVerification: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVirtualAuthenticator(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParseVirtualAuthenticator_Refused(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nothing", nil, "is not true, false or a map"},
		{"a number", 1, "is not true, false or a map"},
		{"a word", "yes please", "is not true, false or a map"},
		{"a misspelt option", map[string]any{"user_verify": true}, `unknown option "user_verify"`},
		{"a transport it does not know", map[string]any{"transport": "carrier-pigeon"}, "transport carrier-pigeon is not one of"},
		{"a transport that is not a string", map[string]any{"transport": 1}, "transport 1 is not one of"},
		{"an option that is not a boolean", map[string]any{"resident_key": "maybe"}, "resident_key is maybe, want true or false"},
		{"verified without verification", map[string]any{"user_verification": false, "user_verified": true}, "user_verified needs user_verification"},
		{"backed up without eligibility", map[string]any{"backed_up": true}, "backed_up needs backup_eligible"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVirtualAuthenticator(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to say %q", err, tc.want)
			}
			if !strings.HasPrefix(err.Error(), "virtual_authenticator: ") {
				t.Errorf("error = %v, want it to name the parameter", err)
			}
			if got != nil {
				t.Errorf("got %#v, want no options", got)
			}
		})
	}
}

func TestAuthenticatorOptions_CDPOptions(t *testing.T) {
	got := defaultAuthenticatorOptions().cdpOptions()
	want := &webauthn.VirtualAuthenticatorOptions{
		Protocol:                    webauthn.AuthenticatorProtocolCtap2,
		Transport:                   webauthn.AuthenticatorTransportInternal,
		HasResidentKey:              true,
		HasUserVerification:         true,
		IsUserVerified:              true,
		AutomaticPresenceSimulation: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestParseData_VirtualAuthenticator(t *testing.T) {
	t.Run("given", func(t *testing.T) {
		req := NewReq()
		err := req.parseData(map[string]any{
			"actions":               []any{map[string]any{"name": "navigate", "url": "http://localhost/"}},
			"virtual_authenticator": map[string]any{"user_verified": false},
		}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.VirtualAuthenticator == nil || req.VirtualAuthenticator.UserVerified {
			t.Errorf("VirtualAuthenticator = %#v, want one that fails user verification", req.VirtualAuthenticator)
		}
	})

	t.Run("left out", func(t *testing.T) {
		req := NewReq()
		err := req.parseData(map[string]any{
			"actions": []any{map[string]any{"name": "navigate", "url": "http://localhost/"}},
		}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.VirtualAuthenticator != nil {
			t.Errorf("VirtualAuthenticator = %#v, want none", req.VirtualAuthenticator)
		}
	})
}

func TestRequest_VirtualAuthenticatorRefused(t *testing.T) {
	_, err := Request(map[string]any{
		"actions":               []any{map[string]any{"name": "navigate", "url": "http://localhost/"}},
		"virtual_authenticator": map[string]any{"transport": "carrier-pigeon"},
	})
	if err == nil || !strings.Contains(err.Error(), "virtual_authenticator") {
		t.Fatalf("error = %v, want the step refused before the browser starts", err)
	}
}

func TestBuildActionTasks_VirtualAuthenticator(t *testing.T) {
	path, err := writeStorageState(testState, t.TempDir()+"/state.json")
	if err != nil {
		t.Fatal(err)
	}
	req := NewReq()
	req.VirtualAuthenticator = defaultAuthenticatorOptions()
	req.StorageState = path
	req.Actions = []*ChromeDPAction{{Name: "navigate", URL: "http://localhost/"}}

	tasks, err := req.buildActionTasks()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("got %d tasks, want the authenticator, the state and the navigate", len(tasks))
	}
}
