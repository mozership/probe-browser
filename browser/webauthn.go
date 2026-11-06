package browser

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/webauthn"
	"github.com/chromedp/chromedp"
)

// authenticatorOptions is what virtual_authenticator asks of the virtual
// authenticator the browser starts with. The defaults are those of a
// platform authenticator that holds passkeys, such as Touch ID, and passes
// user verification.
type authenticatorOptions struct {
	Transport        string
	ResidentKey      bool
	UserVerification bool
	UserVerified     bool
	BackupEligible   bool
	BackedUp         bool
}

func defaultAuthenticatorOptions() *authenticatorOptions {
	return &authenticatorOptions{
		Transport:        string(webauthn.AuthenticatorTransportInternal),
		ResidentKey:      true,
		UserVerification: true,
		UserVerified:     true,
	}
}

// authenticatorTransports are the transports virtual_authenticator takes.
var authenticatorTransports = []string{
	string(webauthn.AuthenticatorTransportInternal),
	string(webauthn.AuthenticatorTransportUsb),
	string(webauthn.AuthenticatorTransportNfc),
	string(webauthn.AuthenticatorTransportBle),
	string(webauthn.AuthenticatorTransportHybrid),
}

// authenticatorKeys are the keys a map given to virtual_authenticator takes.
var authenticatorKeys = []string{"transport", "resident_key", "user_verification", "user_verified", "backup_eligible", "backed_up"}

// parseVirtualAuthenticator reads virtual_authenticator: true for the
// defaults, false for none, or a map of the options to change. It is read
// here rather than by DecodeParams, which leaves a struct as it is when it
// is given true, and so would start no authenticator without a word. A key
// it does not know is refused, as a misspelt option would be ignored.
func parseVirtualAuthenticator(v any) (*authenticatorOptions, error) {
	switch val := v.(type) {
	case bool:
		if !val {
			return nil, nil
		}
		return defaultAuthenticatorOptions(), nil
	case string:
		// A value from a template, such as "{{vars.passkey}}", is a string.
		b, err := strconv.ParseBool(val)
		if err != nil {
			return nil, fmt.Errorf("virtual_authenticator: %q is not true, false or a map of options", val)
		}
		return parseVirtualAuthenticator(b)
	case map[string]any:
		return parseAuthenticatorMap(val)
	}
	return nil, fmt.Errorf("virtual_authenticator: %v is not true, false or a map of options", v)
}

func parseAuthenticatorMap(m map[string]any) (*authenticatorOptions, error) {
	o := defaultAuthenticatorOptions()
	for key := range m {
		if !slices.Contains(authenticatorKeys, key) {
			return nil, fmt.Errorf("virtual_authenticator: unknown option %q, want one of %s", key, strings.Join(authenticatorKeys, ", "))
		}
	}

	if v, ok := m["transport"]; ok {
		s, isString := v.(string)
		if !isString || !slices.Contains(authenticatorTransports, s) {
			return nil, fmt.Errorf("virtual_authenticator: transport %v is not one of %s", v, strings.Join(authenticatorTransports, ", "))
		}
		o.Transport = s
	}
	for _, f := range []struct {
		key string
		dst *bool
	}{
		{"resident_key", &o.ResidentKey},
		{"user_verification", &o.UserVerification},
		{"backup_eligible", &o.BackupEligible},
		{"backed_up", &o.BackedUp},
	} {
		if err := authenticatorBool(m, f.key, f.dst); err != nil {
			return nil, err
		}
	}
	// An authenticator that cannot verify the user does not pass the check,
	// so user_verified follows user_verification unless it is given.
	o.UserVerified = o.UserVerification
	if err := authenticatorBool(m, "user_verified", &o.UserVerified); err != nil {
		return nil, err
	}

	if o.UserVerified && !o.UserVerification {
		return nil, fmt.Errorf("virtual_authenticator: user_verified needs user_verification, as an authenticator without it verifies no user")
	}
	// The backup state flag may be set only on a credential that is
	// eligible for backup: https://w3c.github.io/webauthn/#sctn-credential-backup
	if o.BackedUp && !o.BackupEligible {
		return nil, fmt.Errorf("virtual_authenticator: backed_up needs backup_eligible, as only a credential eligible for backup can be backed up")
	}
	return o, nil
}

// authenticatorBool reads m[key] into dst when it is given, as true or
// false, or as a string of one from a template.
func authenticatorBool(m map[string]any, key string, dst *bool) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case bool:
		*dst = val
		return nil
	case string:
		b, err := strconv.ParseBool(val)
		if err == nil {
			*dst = b
			return nil
		}
	}
	return fmt.Errorf("virtual_authenticator: %s is %v, want true or false", key, v)
}

// cdpOptions returns the options of the protocol's addVirtualAuthenticator.
func (o *authenticatorOptions) cdpOptions() *webauthn.VirtualAuthenticatorOptions {
	return &webauthn.VirtualAuthenticatorOptions{
		Protocol:            webauthn.AuthenticatorProtocolCtap2,
		Transport:           webauthn.AuthenticatorTransport(o.Transport),
		HasResidentKey:      o.ResidentKey,
		HasUserVerification: o.UserVerification,
		IsUserVerified:      o.UserVerified,
		// The protocol says this defaults to true, but the field is sent
		// even when false, and an authenticator without it never answers.
		AutomaticPresenceSimulation: true,
		DefaultBackupEligibility:    o.BackupEligible,
		DefaultBackupState:          o.BackedUp,
	}
}

// virtualAuthenticator is the virtual authenticator of a step, as the
// actions that use it see it once it is added.
type virtualAuthenticator struct {
	id webauthn.AuthenticatorID
	// events, when not nil, receives what the authenticator does, as
	// passkeyRegistration or passkeyAuthentication, for wait_passkey.
	events chan string
}

// What wait_passkey returns, by what the authenticator did.
const (
	passkeyRegistration   = "registration"
	passkeyAuthentication = "authentication"
)

// passkeyEventBuffer is how many of what the authenticator does are kept
// for wait_passkey before it waits for them.
const passkeyEventBuffer = 64

// addVirtualAuthenticator returns an action that has the page's tab answer
// WebAuthn with a virtual authenticator of o in place of the platform's,
// before any page is opened, holding the credentials of creds. It belongs to
// the tab, so a popup the page opens has none. It keeps the authenticator's
// ID in va, for the actions that use it, and starts to record what it does
// when va has events.
func addVirtualAuthenticator(o *authenticatorOptions, creds []*webauthn.Credential, va *virtualAuthenticator) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		// The browser's own WebAuthn dialogs would wait for a user.
		enableUI := false
		if _, err := cdp.Call(ctx, t, webauthn.Enable, webauthn.EnableParams{EnableUI: &enableUI}); err != nil {
			return chromedp.Void{}, fmt.Errorf("virtual_authenticator: enabling WebAuthn: %w", err)
		}
		res, err := cdp.Call(ctx, t, webauthn.AddVirtualAuthenticator, webauthn.AddVirtualAuthenticatorParams{Options: o.cdpOptions()})
		if err != nil {
			return chromedp.Void{}, fmt.Errorf("virtual_authenticator: adding the authenticator: %w", err)
		}
		va.id = res.AuthenticatorID
		for i, c := range creds {
			if _, err := cdp.Call(ctx, t, addCredential, newAddCredentialParams(va.id, c)); err != nil {
				return chromedp.Void{}, fmt.Errorf("passkeys: adding passkey %d for %s: %w", i, c.RpID, err)
			}
		}
		// Recording starts after the passkeys of the file are added, so that
		// wait_passkey waits for what the page does only. The events are
		// subscribed to here, before any page opens, and so a registration
		// that ends before its wait_passkey starts is not missed.
		if va.events != nil {
			recordPasskeyEvents(ctx, cdp.Events(ctx, t, webauthn.CredentialAdded), passkeyRegistration, va.events)
			recordPasskeyEvents(ctx, cdp.Events(ctx, t, webauthn.CredentialAsserted), passkeyAuthentication, va.events)
		}
		return chromedp.Void{}, nil
	}
}

// recordPasskeyEvents sends kind to dst for each of events, until ctx, that
// of the actions, ends.
func recordPasskeyEvents[E any](ctx context.Context, events iter.Seq2[E, error], kind string, dst chan<- string) {
	go func() {
		for _, err := range events {
			if err != nil {
				return
			}
			select {
			case dst <- kind:
			case <-ctx.Done():
				return
			}
		}
	}()
}

// waitPasskey returns an action that waits until the authenticator has
// registered or authenticated with a passkey, one an earlier wait_passkey
// has not waited for, and keeps which it did in dst.
func waitPasskey(va *virtualAuthenticator, dst *string) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		select {
		case kind := <-va.events:
			*dst = kind
			return chromedp.Void{}, nil
		case <-ctx.Done():
			return chromedp.Void{}, fmt.Errorf("wait_passkey: the page registered no passkey and signed in with none: %w", ctx.Err())
		}
	}
}

// setUserVerified returns an action that has user verification succeed or
// fail from then on, as when a user's fingerprint stops matching.
func setUserVerified(va *virtualAuthenticator, verified bool) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		if _, err := cdp.Call(ctx, t, webauthn.SetUserVerified, webauthn.SetUserVerifiedParams{AuthenticatorID: va.id, IsUserVerified: verified}); err != nil {
			return chromedp.Void{}, fmt.Errorf("set_user_verified: %w", err)
		}
		return chromedp.Void{}, nil
	}
}
