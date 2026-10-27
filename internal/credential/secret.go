// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package credential protects Wake's upstream credential material.
package credential

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Error is a classifiable credential-protection error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrDestroyedSecret is returned for a zero or cleared secret.
	ErrDestroyedSecret Error = "destroyed secret"
	// ErrInvalidMasterKey is returned for a master key that is not AES-256 sized.
	ErrInvalidMasterKey Error = "invalid master key"
	// ErrInvalidMasterKeyPath is returned when a master-key file path is not absolute.
	ErrInvalidMasterKeyPath Error = "invalid master key path"
	// ErrInsecureMasterKeyFile is returned when a master-key file is not private
	// regular-file key material owned by root or the effective user.
	ErrInsecureMasterKeyFile Error = "insecure master key file"
	// ErrMasterKeyExists is returned when setup would overwrite existing key
	// material. Operators must rotate keys explicitly rather than accidentally.
	ErrMasterKeyExists Error = "master key file already exists"
	// ErrDestroyedMasterKey is returned for a zero or cleared master key.
	ErrDestroyedMasterKey Error = "destroyed master key"
	// ErrDestroyedEnvelope is returned for a zero or cleared envelope.
	ErrDestroyedEnvelope Error = "destroyed envelope"
	// ErrInvalidAAD is returned when associated data is empty.
	ErrInvalidAAD Error = "invalid associated data"
	// ErrInvalidEncryptedSecret is returned for a malformed encrypted payload.
	ErrInvalidEncryptedSecret Error = "invalid encrypted secret" // #nosec G101 -- error text, not credential material
	// ErrUnsupportedVersion is returned for an unsupported encrypted payload version.
	ErrUnsupportedVersion Error = "unsupported encrypted secret version"
	// ErrAuthentication is returned when an encrypted payload cannot be authenticated.
	ErrAuthentication Error = "encrypted secret authentication failed"
	// ErrSecretSerialization is returned when secret material is serialized as JSON.
	ErrSecretSerialization Error = "secret JSON serialization is not supported"
)

type secretState struct {
	mutex sync.RWMutex
	value []byte
	alive bool
}

// Secret keeps mutable credential bytes out of string-based APIs. Copies share
// the same private state, so clearing any copy clears all aliases.
type Secret struct {
	state *secretState
}

// NewSecret returns a defensive copy of value. An empty secret is valid.
func NewSecret(value []byte) *Secret {
	return &Secret{state: &secretState{value: append([]byte(nil), value...), alive: true}}
}

// Copy returns a mutable copy that callers must clear when no longer needed.
func (value *Secret) Copy() ([]byte, error) {
	if value == nil || value.state == nil {
		return nil, ErrDestroyedSecret
	}

	value.state.mutex.RLock()
	defer value.state.mutex.RUnlock()

	if !value.state.alive {
		return nil, ErrDestroyedSecret
	}

	return append([]byte(nil), value.state.value...), nil
}

// Clear overwrites the secret's owned bytes and makes it unusable.
func (value *Secret) Clear() {
	if value == nil || value.state == nil {
		return
	}

	value.state.mutex.Lock()
	defer value.state.mutex.Unlock()

	clear(value.state.value)
	value.state.value = nil
	value.state.alive = false
}

// String redacts secret material.
func (value Secret) String() string { return "[REDACTED]" }

// GoString redacts secret material.
func (value Secret) GoString() string { return "credential.Secret([REDACTED])" }

// Format redacts secret material for every fmt verb.
func (value Secret) Format(state fmt.State, verb rune) { _, _ = io.WriteString(state, value.String()) }

// MarshalJSON rejects accidental JSON serialization.
func (value Secret) MarshalJSON() ([]byte, error) { return nil, ErrSecretSerialization }

// UnmarshalJSON rejects accidental JSON serialization.
func (value *Secret) UnmarshalJSON(data []byte) error { return ErrSecretSerialization }

type masterKeyState struct {
	mutex sync.RWMutex
	value [32]byte
	alive bool
}

// MasterKey is an AES-256 key held in mutable memory. Copies share the same
// private state, so clearing any copy clears all aliases.
type MasterKey struct {
	state *masterKeyState
}

// NewMasterKey validates and copies an AES-256 key.
func NewMasterKey(value []byte) (*MasterKey, error) {
	if len(value) != len(masterKeyState{}.value) {
		return nil, ErrInvalidMasterKey
	}

	result := &MasterKey{state: &masterKeyState{alive: true}}
	copy(result.state.value[:], value)

	return result, nil
}

// Clear overwrites the key and makes it unusable.
func (value *MasterKey) Clear() {
	if value == nil || value.state == nil {
		return
	}

	value.state.mutex.Lock()
	defer value.state.mutex.Unlock()

	clear(value.state.value[:])
	value.state.alive = false
}

func (value *MasterKey) copy() ([32]byte, error) {
	if value == nil || value.state == nil {
		return [32]byte{}, ErrDestroyedMasterKey
	}

	value.state.mutex.RLock()
	defer value.state.mutex.RUnlock()

	if !value.state.alive {
		return [32]byte{}, ErrDestroyedMasterKey
	}

	return value.state.value, nil
}

// String redacts key material.
func (value MasterKey) String() string { return "[REDACTED]" }

// GoString redacts key material.
func (value MasterKey) GoString() string { return "credential.MasterKey([REDACTED])" }

// Format redacts key material for every fmt verb.
func (value MasterKey) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, value.String())
}

// MarshalJSON rejects accidental JSON serialization.
func (value MasterKey) MarshalJSON() ([]byte, error) { return nil, ErrSecretSerialization }

// UnmarshalJSON rejects accidental JSON serialization.
func (value *MasterKey) UnmarshalJSON(data []byte) error { return ErrSecretSerialization }

// String redacts the envelope's copied master key.
func (value Envelope) String() string { return "[REDACTED]" }

// GoString redacts the envelope's copied master key.
func (value Envelope) GoString() string { return "credential.Envelope([REDACTED])" }

// Format redacts the envelope's copied master key for every fmt verb.
func (value Envelope) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, value.String())
}

// MarshalJSON rejects accidental JSON serialization.
func (value Envelope) MarshalJSON() ([]byte, error) { return nil, ErrSecretSerialization }

// UnmarshalJSON rejects accidental JSON serialization.
func (value *Envelope) UnmarshalJSON(data []byte) error { return ErrSecretSerialization }

var (
	_ fmt.Formatter    = Secret{}
	_ fmt.Stringer     = Secret{}
	_ fmt.GoStringer   = Secret{}
	_ json.Marshaler   = Secret{}
	_ json.Unmarshaler = (*Secret)(nil)
	_ fmt.Formatter    = MasterKey{}
	_ fmt.Stringer     = MasterKey{}
	_ fmt.GoStringer   = MasterKey{}
	_ json.Marshaler   = MasterKey{}
	_ json.Unmarshaler = (*MasterKey)(nil)
	_ fmt.Formatter    = Envelope{}
	_ fmt.Stringer     = Envelope{}
	_ fmt.GoStringer   = Envelope{}
	_ json.Marshaler   = Envelope{}
	_ json.Unmarshaler = (*Envelope)(nil)
)
