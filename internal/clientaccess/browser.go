// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package clientaccess

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	ErrInvalidPasskey      Error = "invalid passkey details"
	ErrPasskeyLimit        Error = "passkey storage limit reached"
	ErrLastBrowserMethod   Error = "at least one browser sign-in method is required"
	MaxPasskeys                  = 16
	MaxPasskeyLabelBytes         = 100
	BrowserUserHandleBytes       = 32
	maxCredentialIDBytes         = 1023
	// Reserve room for every financial grant even when its name needs JSON escaping.
	maxPasskeyBytes = 48 << 10
)

type Passkey struct {
	RPID       string              `json:"rp_id"`
	Label      string              `json:"label,omitempty"`
	Credential webauthn.Credential `json:"credential"`
}

func ParsePasskeyLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if len(label) > MaxPasskeyLabelBytes || !utf8.ValidString(label) ||
		strings.ContainsFunc(label, unicode.IsControl) {
		return "", ErrInvalidPasskey
	}

	return label, nil
}

// BrowserAccess holds the browser authority and serves as the expected state for updates.
type BrowserAccess struct {
	Password   string
	Key        [32]byte
	UserHandle []byte
	Passkeys   []Passkey
}

func (s FileStore) BrowserAccess() (BrowserAccess, error) {
	record, err := s.read()
	defer record.clear()

	return record.browserAccess(), err
}

func (s FileStore) Browser() (string, [32]byte, error) {
	state, err := s.BrowserAccess()

	return state.Password, state.Key, err
}

// UpdateBrowserAccess applies a change only while its browser snapshot is current.
// The store owns the session key and establishes the user handle with the first credential.
func (s FileStore) UpdateBrowserAccess(
	ctx context.Context,
	expected BrowserAccess,
	change func(*BrowserAccess) error,
	rotate bool,
) (BrowserAccess, error) {
	var updated BrowserAccess

	err := s.update(ctx, func(record *authRecord) error {
		current := record.browserAccess()
		if !current.matches(expected) {
			return ErrBrowserChanged
		}

		current.UserHandle = bytes.Clone(current.UserHandle)
		if err := change(&current); err != nil {
			return err
		}

		if current.Password == "" && len(current.Passkeys) == 0 {
			return ErrLastBrowserMethod
		}

		switch {
		case len(record.UserHandle) != 0:
			if !bytes.Equal(record.UserHandle, current.UserHandle) {
				return ErrInvalidPasskey
			}
		case len(current.Passkeys) != 0:
			if len(current.UserHandle) != BrowserUserHandleBytes {
				return ErrInvalidPasskey
			}

			record.Version = passkeyAuthVersion
		case len(current.UserHandle) != 0:
			return ErrInvalidPasskey
		}

		if err := validatePasskeys(current.Passkeys); err != nil {
			return err
		}

		encoded, err := json.Marshal(current.Passkeys)
		if err != nil || len(encoded) > maxPasskeyBytes {
			return ErrPasskeyLimit
		}

		rotateKey := rotate || record.Unlock != current.Password || len(record.SessionKey) == 0
		record.Unlock = current.Password
		record.UserHandle = current.UserHandle
		record.Passkeys = current.Passkeys

		if rotateKey {
			record.rotate()
		}

		updated = record.browserAccess()

		return nil
	})
	if err != nil {
		return BrowserAccess{}, err
	}

	return updated, nil
}

func (r authRecord) browserAccess() BrowserAccess {
	state := BrowserAccess{Password: r.Unlock, UserHandle: r.UserHandle, Passkeys: r.Passkeys}
	copy(state.Key[:], r.SessionKey)

	return state
}

func (b BrowserAccess) matches(other BrowserAccess) bool {
	if b.Password != other.Password || subtle.ConstantTimeCompare(b.Key[:], other.Key[:]) != 1 ||
		!bytes.Equal(b.UserHandle, other.UserHandle) || len(b.Passkeys) != len(other.Passkeys) {
		return false
	}

	if len(b.Passkeys) == 0 {
		return true
	}

	// Compare durable fields. The library also holds decoded flags in private fields.
	current, err := json.Marshal(b.Passkeys)
	expected, expectedErr := json.Marshal(other.Passkeys)

	return err == nil && expectedErr == nil && bytes.Equal(current, expected)
}

func validatePasskeys(passkeys []Passkey) error {
	if len(passkeys) > MaxPasskeys {
		return ErrPasskeyLimit
	}

	seen := make(map[string]bool, len(passkeys))
	for _, passkey := range passkeys {
		id := string(passkey.Credential.ID)
		label, err := ParsePasskeyLabel(passkey.Label)
		if err != nil || label != passkey.Label || len(id) == 0 || len(id) > maxCredentialIDBytes || seen[id] ||
			passkey.RPID != strings.ToLower(passkey.RPID) || protocol.ValidateRPID(passkey.RPID) != nil {
			return ErrInvalidPasskey
		}

		if _, err := webauthncose.ParsePublicKey(passkey.Credential.PublicKey); err != nil {
			return ErrInvalidPasskey
		}

		seen[id] = true
	}

	return nil
}
