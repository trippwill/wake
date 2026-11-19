// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package clientaccess

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/webauthn"
)

const testPassword = "v1.AAAAAAAAAAAAAAAAAAAAAA.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestBrowserAccessLegacyRecoveryAndReset(t *testing.T) {
	store := privateTestStore(t)
	path := filepath.Join(store.dir, FileName)
	if state := getBrowserAccess(t, store); !state.matches(BrowserAccess{}) {
		t.Fatal("an unprovisioned store returned browser authority")
	}

	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("loading browser access created a file", err)
	}

	const legacy = `{"version":1,"unlock_verifier":"` + testPassword +
		`","session_key":"AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","grants":null}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	before := getBrowserAccess(t, store)
	if before.Password != testPassword || before.Key != ([32]byte{1}) || len(before.UserHandle) != 0 {
		t.Fatal("legacy browser authority changed on read")
	}

	manager := New(store)

	_, secret, err := manager.Create(t.Context(), "Synthetic client")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Clear()

	token, err := secret.Copy()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(token)

	passkey := testPasskey(t, "a")
	enrolled, err := store.UpdateBrowserAccess(t.Context(), before, func(draft *BrowserAccess) error {
		draft.UserHandle = bytes.Repeat([]byte{2}, BrowserUserHandleBytes)
		draft.Passkeys = []Passkey{passkey}

		return nil
	}, true)
	if err != nil || enrolled.Key == before.Key || !enrolled.matches(getBrowserAccess(t, store)) {
		t.Fatal("enrollment did not preserve and rotate current browser authority", err)
	}

	dropPassword := func(draft *BrowserAccess) error {
		draft.Password = ""
		return nil
	}

	removed, err := store.UpdateBrowserAccess(t.Context(), enrolled, dropPassword, true)
	if err != nil || removed.Password != "" || removed.Key == enrolled.Key || len(removed.Passkeys) != 1 {
		t.Fatal("removing the password did not preserve passkey authority", err)
	}

	dropPasskeys := func(draft *BrowserAccess) error {
		draft.Passkeys = nil
		return nil
	}
	if _, err := store.UpdateBrowserAccess(
		t.Context(),
		removed,
		dropPasskeys,
		true,
	); !errors.Is(
		err,
		ErrLastBrowserMethod,
	) {
		t.Fatal("store allowed removal of the sole sign-in method", err)
	}

	if err := store.SetBrowser(testPassword); err != nil {
		t.Fatal(err)
	}

	recovered := getBrowserAccess(t, store)
	if recovered.Password != testPassword || removed.Key == recovered.Key || len(recovered.Passkeys) != 1 ||
		!bytes.Equal(recovered.UserHandle, enrolled.UserHandle) {
		t.Fatal("password recovery did not retain enrollment and rotate sessions")
	}

	_, release, err := manager.Admit(t.Context(), string(token))
	if err != nil {
		t.Fatal("browser changes affected the financial grant", err)
	}

	release()

	if err := store.RevokeBrowserSessions(t.Context()); err != nil {
		t.Fatal(err)
	}

	revoked := getBrowserAccess(t, store)
	if revoked.Key == recovered.Key {
		t.Fatal("revoking sessions did not rotate the key")
	}

	recovered.Key = revoked.Key
	if !recovered.matches(revoked) {
		t.Fatal("revoking sessions changed browser sign-in methods")
	}

	if err := store.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}

	reset := getBrowserAccess(t, store)
	if revoked.Key == reset.Key {
		t.Fatal("resetting access did not rotate the key")
	}

	revoked.Key = reset.Key
	if !revoked.matches(reset) {
		t.Fatal("resetting access changed browser sign-in methods")
	}

	if _, _, err := manager.Admit(t.Context(), string(token)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("resetting access kept the grant", err)
	}

	passwordOnly, err := store.UpdateBrowserAccess(t.Context(), reset, dropPasskeys, true)
	if err != nil || !bytes.Equal(passwordOnly.UserHandle, enrolled.UserHandle) ||
		len(passwordOnly.Passkeys) != 0 {
		t.Fatal("removing all passkeys did not preserve the install handle", err)
	}

	removedPassword, err := store.UpdateBrowserAccess(t.Context(), passwordOnly, dropPassword, true)
	if !errors.Is(err, ErrLastBrowserMethod) || removedPassword.Key != ([32]byte{}) ||
		!passwordOnly.matches(getBrowserAccess(t, store)) {
		t.Fatal("a browser session removed the remaining password", err)
	}

	if err := store.SetBrowser(""); !errors.Is(err, ErrLastBrowserMethod) {
		t.Fatal("store allowed removal of the remaining password", err)
	}
}

func TestBrowserAccessRejectsLosingCredentialUpdates(t *testing.T) {
	store := privateTestStore(t)
	if err := store.SetBrowser(testPassword); err != nil {
		t.Fatal(err)
	}

	before := getBrowserAccess(t, store)
	passkey := testPasskey(t, "a")

	enrolled, err := store.UpdateBrowserAccess(t.Context(), before, func(draft *BrowserAccess) error {
		draft.UserHandle = bytes.Repeat([]byte{3}, BrowserUserHandleBytes)
		draft.Passkeys = []Passkey{passkey}

		return nil
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for count := range uint32(2) {
		go func() {
			<-start

			_, err := store.UpdateBrowserAccess(t.Context(), enrolled, func(draft *BrowserAccess) error {
				draft.Passkeys[0].Credential.Authenticator.SignCount = count + 1
				return nil
			}, false)
			results <- err
		}()
	}

	close(start)

	first, second := <-results, <-results
	if first == nil {
		first, second = second, first
	}

	if !errors.Is(first, ErrBrowserChanged) || second != nil {
		t.Fatal("concurrent updates did not serialize the sign count", first, second)
	}

	after := getBrowserAccess(t, store)
	if after.Key != enrolled.Key || after.Passkeys[0].Credential.Authenticator.SignCount == 0 {
		t.Fatal("updating the sign count rotated sessions or lost the update")
	}

	if err := store.RevokeBrowserSessions(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := store.UpdateBrowserAccess(t.Context(), after, func(*BrowserAccess) error {
		t.Fatal("revoked session key admitted a browser access update")
		return nil
	}, false); !errors.Is(err, ErrBrowserChanged) {
		t.Fatal("session revocation did not invalidate stale snapshots", err)
	}
}

func TestBrowserAccessRejectsInvalidEnrollment(t *testing.T) {
	store := privateTestStore(t)
	if err := store.SetBrowser(testPassword); err != nil {
		t.Fatal(err)
	}

	before := getBrowserAccess(t, store)
	passkey := testPasskey(t, "a")

	for _, tc := range []struct {
		name   string
		change func(*BrowserAccess)
		want   error
	}{
		{"missing handle", func(d *BrowserAccess) { d.UserHandle = nil }, ErrInvalidPasskey},
		{"handle without a passkey", func(d *BrowserAccess) { d.Passkeys = nil }, ErrInvalidPasskey},
		{"uncanonical label", func(d *BrowserAccess) { d.Passkeys[0].Label = " key " }, ErrInvalidPasskey},
		{"control character", func(d *BrowserAccess) { d.Passkeys[0].Label = "key\x00" }, ErrInvalidPasskey},
		{"overlong label", func(d *BrowserAccess) {
			d.Passkeys[0].Label = strings.Repeat("a", MaxPasskeyLabelBytes+1)
		}, ErrInvalidPasskey},
		{"bad public key", func(d *BrowserAccess) {
			d.Passkeys[0].Credential.PublicKey = []byte("invalid")
		}, ErrInvalidPasskey},
		{"duplicate id across relying parties", func(d *BrowserAccess) {
			duplicate := passkey
			duplicate.RPID = "wake.example"
			d.Passkeys = append(d.Passkeys, duplicate)
		}, ErrInvalidPasskey},
		{"passkey count", func(d *BrowserAccess) { d.Passkeys = make([]Passkey, MaxPasskeys+1) }, ErrPasskeyLimit},
		{"passkey bytes", func(d *BrowserAccess) {
			d.Passkeys[0].Credential.Attestation.Object = make([]byte, maxPasskeyBytes)
		}, ErrPasskeyLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.UpdateBrowserAccess(t.Context(), before, func(draft *BrowserAccess) error {
				draft.UserHandle = bytes.Repeat([]byte{4}, BrowserUserHandleBytes)
				draft.Passkeys = []Passkey{passkey}
				tc.change(draft)

				return nil
			}, true)
			if !errors.Is(err, tc.want) || !before.matches(getBrowserAccess(t, store)) {
				t.Fatal("invalid passkey changed stored browser authority or returned the wrong error", err)
			}
		})
	}
}

func TestBrowserAccessRejectsInvalidAndStaleChanges(t *testing.T) {
	store := privateTestStore(t)
	passkey := testPasskey(t, "a")

	enrolled, err := store.UpdateBrowserAccess(
		t.Context(),
		getBrowserAccess(t, store),
		func(draft *BrowserAccess) error {
			draft.UserHandle = bytes.Repeat([]byte{5}, BrowserUserHandleBytes)
			draft.Passkeys = []Passkey{passkey}

			return nil
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.UpdateBrowserAccess(t.Context(), enrolled, func(draft *BrowserAccess) error {
		draft.UserHandle[0]++
		return nil
	}, true); !errors.Is(err, ErrInvalidPasskey) || !enrolled.matches(getBrowserAccess(t, store)) {
		t.Fatal("editing the established user handle changed persisted authority", err)
	}

	const rejected Error = "passkey is unavailable to this browser origin"
	if _, err := store.UpdateBrowserAccess(t.Context(), enrolled, func(draft *BrowserAccess) error {
		draft.Passkeys[0].Credential.Authenticator.SignCount++
		return rejected
	}, false); !errors.Is(err, rejected) || !enrolled.matches(getBrowserAccess(t, store)) {
		t.Fatal("a rejected callback changed persisted authority", err)
	}

	// Host recovery invalidates a passkey mutation prepared by the old browser session.
	if err := store.SetBrowser(testPassword); err != nil {
		t.Fatal(err)
	}

	if _, err := store.UpdateBrowserAccess(t.Context(), enrolled, func(*BrowserAccess) error {
		t.Fatal("host recovery admitted a stale passkey update")
		return nil
	}, true); !errors.Is(err, ErrBrowserChanged) {
		t.Fatal("host recovery did not invalidate the passkey snapshot", err)
	}

	recovered := getBrowserAccess(t, store)
	if recovered.Password != testPassword || recovered.Key == enrolled.Key {
		t.Fatal("host recovery did not restore the password and rotate sessions")
	}

	enrolled.Password, enrolled.Key = recovered.Password, recovered.Key
	if !enrolled.matches(recovered) {
		t.Fatal("host recovery changed the enrolled passkey or user handle")
	}
}

func TestBrowserAccessRejectsInconsistentRecords(t *testing.T) {
	passkey := testPasskey(t, "a")
	for _, tc := range []struct {
		name   string
		change func(*authRecord)
	}{
		{"legacy record with a passkey", func(r *authRecord) { r.Version = passwordAuthVersion }},
		{"passkey record without a handle", func(r *authRecord) { r.UserHandle = nil }},
		{"passkey record without a sign-in method", func(r *authRecord) { r.Passkeys = nil }},
		{"passkey record without a session key", func(r *authRecord) { r.SessionKey = nil }},
		{"passkey record with duplicate credentials", func(r *authRecord) { r.Passkeys = append(r.Passkeys, passkey) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := privateTestStore(t)
			path := filepath.Join(store.dir, FileName)
			record := authRecord{
				Version:    passkeyAuthVersion,
				SessionKey: bytes.Repeat([]byte{1}, 32),
				UserHandle: bytes.Repeat([]byte{2}, BrowserUserHandleBytes),
				Passkeys:   []Passkey{passkey},
			}
			tc.change(&record)

			// #nosec G117 -- The record contains a synthetic key for a disposable test store.
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}

			if state, err := store.BrowserAccess(); !errors.Is(err, ErrInvalidSecretFile) ||
				!state.matches(BrowserAccess{}) {
				t.Fatal("inconsistent record returned browser authority", err)
			}

			if err := store.SetBrowser(testPassword); !errors.Is(err, ErrInvalidSecretFile) {
				t.Fatal("host recovery accepted inconsistent persisted authority", err)
			}

			persisted, err := store.BackupEvidence()
			defer clear(persisted)

			if err != nil || !bytes.Equal(persisted, data) {
				t.Fatal("host recovery replaced the inconsistent record", err)
			}
		})
	}
}

func TestParsePasskeyLabel(t *testing.T) {
	for input, want := range map[string]string{" ": "", " Backup ": "Backup", "🔑": "🔑"} {
		if label, err := ParsePasskeyLabel(input); err != nil || label != want {
			t.Fatalf("parse label %q = %q, %v", input, label, err)
		}
	}

	for _, input := range []string{"two\nlines", "\xff", strings.Repeat("🔑", MaxPasskeyLabelBytes/4+1)} {
		if _, err := ParsePasskeyLabel(input); !errors.Is(err, ErrInvalidPasskey) {
			t.Fatalf("parse invalid label %q returned %v", input, err)
		}
	}
}

func getBrowserAccess(t *testing.T, store FileStore) BrowserAccess {
	t.Helper()

	state, err := store.BrowserAccess()
	if err != nil {
		t.Fatal(err)
	}

	return state
}

func testPasskey(t *testing.T, id string) Passkey {
	t.Helper()

	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	// COSE OKP / EdDSA / Ed25519.
	encoded, err := webauthncbor.Marshal(map[int]any{1: 1, 3: -8, -1: 6, -2: []byte(key)})
	if err != nil {
		t.Fatal(err)
	}

	return Passkey{
		RPID: "localhost",
		Credential: webauthn.Credential{
			ID:        []byte(id),
			PublicKey: encoded,
			Flags:     webauthn.NewCredentialFlags(protocol.FlagUserVerified | protocol.FlagUserPresent),
		},
	}
}
