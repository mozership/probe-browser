package browser

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/webauthn"
	"github.com/chromedp/chromedp"
)

// passkeyFile is what save_passkeys writes and passkeys reads: the
// credentials a virtual authenticator holds, private keys included, so that
// a passkey registered in one step signs in in another. The bytes are in
// base64url, as relying parties keep credential IDs and user handles.
type passkeyFile struct {
	Passkeys []passkey `json:"passkeys"`
}

type passkey struct {
	CredentialID    string `json:"credentialId"`
	RPID            string `json:"rpId"`
	UserHandle      string `json:"userHandle,omitempty"`
	UserName        string `json:"userName,omitempty"`
	UserDisplayName string `json:"userDisplayName,omitempty"`
	// PrivateKey is the ECDSA P-256 private key, in PKCS#8.
	PrivateKey string `json:"privateKey"`
	// SignCount is the signature counter, or -1 for a credential that has
	// none and always reports 0.
	SignCount int64 `json:"signCount"`
	// IsResidentCredential is whether the credential is discoverable, as a
	// passkey is. Left out of a file written by hand, it is true.
	IsResidentCredential *bool `json:"isResidentCredential,omitempty"`
	BackupEligibility    bool  `json:"backupEligibility"`
	BackupState          bool  `json:"backupState"`
}

// decodeBase64URL reads s in base64url, with or without the padding.
func decodeBase64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// readPasskeys reads the file passkeys names, and checks each passkey well
// enough that a file written by hand fails the step before the browser
// starts, rather than as a sign-in the page refuses.
func readPasskeys(path string) ([]*webauthn.Credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("passkeys: %w", err)
	}
	var f passkeyFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("passkeys %s is not a passkey file: %w", path, err)
	}
	creds := make([]*webauthn.Credential, len(f.Passkeys))
	for i, p := range f.Passkeys {
		c, err := p.credential()
		if err != nil {
			return nil, fmt.Errorf("passkeys %s: passkey %d: %w", path, i, err)
		}
		creds[i] = c
	}
	return creds, nil
}

// credential returns p as the protocol's credential.
func (p passkey) credential() (*webauthn.Credential, error) {
	if p.CredentialID == "" {
		return nil, fmt.Errorf("credentialId is missing")
	}
	id, err := decodeBase64URL(p.CredentialID)
	if err != nil {
		return nil, fmt.Errorf("credentialId is not base64url: %w", err)
	}
	if p.RPID == "" {
		return nil, fmt.Errorf("rpId is missing")
	}
	if p.PrivateKey == "" {
		return nil, fmt.Errorf("privateKey is missing")
	}
	key, err := decodeBase64URL(p.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("privateKey is not base64url: %w", err)
	}
	if err := checkPrivateKey(key); err != nil {
		return nil, err
	}
	var handle []byte
	if p.UserHandle != "" {
		if handle, err = decodeBase64URL(p.UserHandle); err != nil {
			return nil, fmt.Errorf("userHandle is not base64url: %w", err)
		}
	}
	if p.SignCount < -1 {
		return nil, fmt.Errorf("signCount is %d, want -1 or more", p.SignCount)
	}
	resident := true
	if p.IsResidentCredential != nil {
		resident = *p.IsResidentCredential
	}
	if p.BackupState && !p.BackupEligibility {
		return nil, fmt.Errorf("backupState needs backupEligibility")
	}
	return &webauthn.Credential{
		CredentialID:         id,
		IsResidentCredential: resident,
		RpID:                 p.RPID,
		PrivateKey:           key,
		UserHandle:           handle,
		SignCount:            float64(p.SignCount),
		BackupEligibility:    p.BackupEligibility,
		BackupState:          p.BackupState,
		UserName:             p.UserName,
		UserDisplayName:      p.UserDisplayName,
	}, nil
}

// checkPrivateKey refuses a key the virtual authenticator cannot sign with:
// it takes an ECDSA P-256 key in PKCS#8 only.
func checkPrivateKey(der []byte) error {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return fmt.Errorf("privateKey is not a PKCS#8 key: %w", err)
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return fmt.Errorf("privateKey is a %T, want an ECDSA P-256 key", key)
	}
	return nil
}

// passkeyOf returns c as it is kept in the file.
func passkeyOf(c *webauthn.Credential) passkey {
	resident := c.IsResidentCredential
	return passkey{
		CredentialID:         base64.RawURLEncoding.EncodeToString(c.CredentialID),
		RPID:                 c.RpID,
		UserHandle:           base64.RawURLEncoding.EncodeToString(c.UserHandle),
		UserName:             c.UserName,
		UserDisplayName:      c.UserDisplayName,
		PrivateKey:           base64.RawURLEncoding.EncodeToString(c.PrivateKey),
		SignCount:            int64(c.SignCount),
		IsResidentCredential: &resident,
		BackupEligibility:    c.BackupEligibility,
		BackupState:          c.BackupState,
	}
}

// savePasskeys returns an action that reads the credentials the virtual
// authenticator *id names holds, and keeps them in dst. An authenticator
// that holds none fails it, as the page has not registered the passkey a
// later step would sign in with, such as when it is still on its way.
func savePasskeys(id *webauthn.AuthenticatorID, dst *passkeyFile) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		res, err := cdp.Call(ctx, t, webauthn.GetCredentials, webauthn.GetCredentialsParams{AuthenticatorID: *id})
		if err != nil {
			return chromedp.Void{}, fmt.Errorf("save_passkeys: reading the credentials: %w", err)
		}
		if len(res.Credentials) == 0 {
			return chromedp.Void{}, fmt.Errorf("save_passkeys: the authenticator holds no passkey: the page has registered none yet")
		}
		f := passkeyFile{Passkeys: make([]passkey, len(res.Credentials))}
		for i, c := range res.Credentials {
			f.Passkeys[i] = passkeyOf(c)
		}
		*dst = f
		return chromedp.Void{}, nil
	}
}

// writePasskeys writes f to path, or to a new file in the temporary
// directory when path is empty, and returns where it went. The private keys
// sign in as the user, so only the user who ran Probe can read it.
func writePasskeys(f *passkeyFile, path string) (string, error) {
	return writePrivateJSON(f, path, "probe-browser-passkeys-*.json")
}

// addCredential is the protocol's WebAuthn.addCredential, taking a
// credential without the fields of the cmtgKey extension. cdproto's
// Credential always sends generateCmtgKeyOnNextOperation, and the browser
// refuses it from an authenticator without the extension, which is every
// one virtual_authenticator starts.
var addCredential = cdp.Command[addCredentialParams, cdp.Empty]{Method: webauthn.CommandAddCredential}

type addCredentialParams struct {
	AuthenticatorID webauthn.AuthenticatorID `json:"authenticatorId"`
	Credential      credentialParams         `json:"credential"`
}

// credentialParams is the protocol's Credential, without the fields of the
// cmtgKey and largeBlob extensions. The bytes are sent in base64, as the
// protocol's binary values are.
type credentialParams struct {
	CredentialID         []byte  `json:"credentialId"`
	IsResidentCredential bool    `json:"isResidentCredential"`
	RpID                 string  `json:"rpId"`
	PrivateKey           []byte  `json:"privateKey"`
	UserHandle           []byte  `json:"userHandle,omitempty"`
	SignCount            float64 `json:"signCount"`
	BackupEligibility    bool    `json:"backupEligibility"`
	BackupState          bool    `json:"backupState"`
	UserName             string  `json:"userName,omitempty"`
	UserDisplayName      string  `json:"userDisplayName,omitempty"`
}

func newAddCredentialParams(id webauthn.AuthenticatorID, c *webauthn.Credential) addCredentialParams {
	return addCredentialParams{
		AuthenticatorID: id,
		Credential: credentialParams{
			CredentialID:         c.CredentialID,
			IsResidentCredential: c.IsResidentCredential,
			RpID:                 c.RpID,
			PrivateKey:           c.PrivateKey,
			UserHandle:           c.UserHandle,
			SignCount:            c.SignCount,
			BackupEligibility:    c.BackupEligibility,
			BackupState:          c.BackupState,
			UserName:             c.UserName,
			UserDisplayName:      c.UserDisplayName,
		},
	}
}
