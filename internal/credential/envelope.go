// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

const (
	envelopeVersion = 1
	nonceSize       = 12
	dataKeySize     = 32
	tagSize         = 16
	wrappedKeySize  = dataKeySize + tagSize
	minimumSize     = 1 + nonceSize + wrappedKeySize + nonceSize + tagSize
)

const (
	wrappingLabel = "wake/credential/envelope/v1/wrapping\x00"
	payloadLabel  = "wake/credential/envelope/v1/payload\x00"
)

type envelopeState struct {
	mutex     sync.RWMutex
	masterKey [dataKeySize]byte
	alive     bool
}

// Envelope encrypts secrets with random data keys wrapped by an AES-256 master
// key. Copies share the same private state, so clearing any copy clears all
// aliases.
type Envelope struct {
	state *envelopeState
}

// NewEnvelope copies key into an AES-256 envelope.
func NewEnvelope(key *MasterKey) (*Envelope, error) {
	masterKey, err := key.copy()
	if err != nil {
		return nil, err
	}
	defer clear(masterKey[:])

	return &Envelope{state: &envelopeState{masterKey: masterKey, alive: true}}, nil
}

// Clear overwrites the copied master key and makes the envelope unusable.
func (value *Envelope) Clear() {
	if value == nil || value.state == nil {
		return
	}

	value.state.mutex.Lock()
	defer value.state.mutex.Unlock()

	clear(value.state.masterKey[:])
	value.state.alive = false
}

func (value *Envelope) copyMasterKey() ([dataKeySize]byte, error) {
	if value == nil || value.state == nil {
		return [dataKeySize]byte{}, ErrDestroyedEnvelope
	}

	value.state.mutex.RLock()
	defer value.state.mutex.RUnlock()

	if !value.state.alive {
		return [dataKeySize]byte{}, ErrDestroyedEnvelope
	}

	return value.state.masterKey, nil
}

// EncryptedSecret is a versioned binary envelope that does not expose its bytes.
type EncryptedSecret struct {
	value []byte
}

// Encrypt creates a new envelope bound to non-empty associated data.
func (value *Envelope) Encrypt(secret *Secret, aad []byte) (EncryptedSecret, error) {
	if len(aad) == 0 {
		return EncryptedSecret{}, ErrInvalidAAD
	}

	masterKey, err := value.copyMasterKey()
	if err != nil {
		return EncryptedSecret{}, err
	}
	defer clear(masterKey[:])

	plaintext, err := secret.Copy()
	if err != nil {
		return EncryptedSecret{}, err
	}
	defer clear(plaintext)

	dataKey := make([]byte, dataKeySize)
	if _, err := rand.Read(dataKey); err != nil {
		return EncryptedSecret{}, fmt.Errorf("generate data key: %w", err)
	}
	defer clear(dataKey)

	wrappedNonce, err := randomNonce()
	if err != nil {
		return EncryptedSecret{}, err
	}

	payloadNonce, err := randomNonce()
	if err != nil {
		return EncryptedSecret{}, err
	}

	wrappingAAD := domainSeparatedAAD(wrappingLabel, aad)
	defer clear(wrappingAAD)

	payloadAAD := domainSeparatedAAD(payloadLabel, aad)
	defer clear(payloadAAD)

	masterGCM, err := gcm(masterKey[:])
	if err != nil {
		return EncryptedSecret{}, err
	}

	wrappedKey := masterGCM.Seal(nil, wrappedNonce, dataKey, wrappingAAD)

	dataGCM, err := gcm(dataKey)
	if err != nil {
		return EncryptedSecret{}, err
	}

	payload := dataGCM.Seal(nil, payloadNonce, plaintext, payloadAAD)
	result := make([]byte, 0, 1+len(wrappedNonce)+len(wrappedKey)+len(payloadNonce)+len(payload))
	result = append(result, envelopeVersion)
	result = append(result, wrappedNonce...)
	result = append(result, wrappedKey...)
	result = append(result, payloadNonce...)
	result = append(result, payload...)

	return EncryptedSecret{value: result}, nil
}

// Decrypt authenticates value against aad and returns a new secret.
func (value *Envelope) Decrypt(encrypted EncryptedSecret, aad []byte) (*Secret, error) {
	if len(aad) == 0 {
		return nil, ErrInvalidAAD
	}

	masterKey, err := value.copyMasterKey()
	if err != nil {
		return nil, err
	}
	defer clear(masterKey[:])

	if err := validateEncryptedSecret(encrypted.value); err != nil {
		return nil, err
	}

	wrappedNonce := encrypted.value[1 : 1+nonceSize]
	wrappedKey := encrypted.value[1+nonceSize : 1+nonceSize+wrappedKeySize]
	payloadNonce := encrypted.value[1+nonceSize+wrappedKeySize : 1+2*nonceSize+wrappedKeySize]
	payload := encrypted.value[1+2*nonceSize+wrappedKeySize:]

	wrappingAAD := domainSeparatedAAD(wrappingLabel, aad)
	defer clear(wrappingAAD)

	payloadAAD := domainSeparatedAAD(payloadLabel, aad)
	defer clear(payloadAAD)

	masterGCM, err := gcm(masterKey[:])
	if err != nil {
		return nil, err
	}

	dataKey, err := masterGCM.Open(nil, wrappedNonce, wrappedKey, wrappingAAD)
	if err != nil {
		return nil, ErrAuthentication
	}
	defer clear(dataKey)

	dataGCM, err := gcm(dataKey)
	if err != nil {
		return nil, ErrAuthentication
	}

	plaintext, err := dataGCM.Open(nil, payloadNonce, payload, payloadAAD)
	if err != nil {
		return nil, ErrAuthentication
	}
	defer clear(plaintext)

	return NewSecret(plaintext), nil
}

// MarshalBinary returns a defensive copy of the stable envelope format.
func (value EncryptedSecret) MarshalBinary() ([]byte, error) {
	if err := validateEncryptedSecret(value.value); err != nil {
		return nil, err
	}

	return append([]byte(nil), value.value...), nil
}

// ParseEncryptedSecret validates and copies a stable envelope format.
func ParseEncryptedSecret(data []byte) (EncryptedSecret, error) {
	if err := validateEncryptedSecret(data); err != nil {
		return EncryptedSecret{}, err
	}

	return EncryptedSecret{value: append([]byte(nil), data...)}, nil
}

func validateEncryptedSecret(value []byte) error {
	if len(value) < minimumSize {
		return ErrInvalidEncryptedSecret
	}

	if value[0] != envelopeVersion {
		return ErrUnsupportedVersion
	}

	return nil
}

func randomNonce() ([]byte, error) {
	result := make([]byte, nonceSize)
	if _, err := rand.Read(result); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	return result, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return cipher.NewGCM(block)
}

func domainSeparatedAAD(label string, aad []byte) []byte {
	result := make([]byte, 0, len(label)+len(aad))
	result = append(result, label...)

	return append(result, aad...)
}

// String redacts encrypted credential material.
func (value EncryptedSecret) String() string { return "[REDACTED]" }

// GoString redacts encrypted credential material.
func (value EncryptedSecret) GoString() string { return "credential.EncryptedSecret([REDACTED])" }

// Format redacts encrypted credential material for every fmt verb.
func (value EncryptedSecret) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, value.String())
}

// MarshalJSON rejects accidental JSON serialization.
func (value EncryptedSecret) MarshalJSON() ([]byte, error) { return nil, ErrSecretSerialization }

// UnmarshalJSON rejects accidental JSON serialization.
func (value *EncryptedSecret) UnmarshalJSON(data []byte) error { return ErrSecretSerialization }

var (
	_ fmt.Formatter    = EncryptedSecret{}
	_ fmt.Stringer     = EncryptedSecret{}
	_ fmt.GoStringer   = EncryptedSecret{}
	_ json.Marshaler   = EncryptedSecret{}
	_ json.Unmarshaler = (*EncryptedSecret)(nil)
)
