// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package credential

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	keyBytes := bytes.Repeat([]byte{1}, dataKeySize)

	key, err := NewMasterKey(keyBytes)
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := NewEnvelope(key)
	if err != nil {
		t.Fatal(err)
	}

	key.Clear()

	for _, plaintext := range [][]byte{nil, []byte("flex-token")} {
		secret := NewSecret(plaintext)

		first, err := envelope.Encrypt(secret, []byte("credential/ibkr-flex"))
		if err != nil {
			t.Fatal(err)
		}

		second, err := envelope.Encrypt(secret, []byte("credential/ibkr-flex"))
		if err != nil {
			t.Fatal(err)
		}

		firstBytes, err := first.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}

		secondBytes, err := second.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}

		if bytes.Equal(firstBytes, secondBytes) {
			t.Fatal("two encryptions produced the same envelope")
		}

		parsed, err := ParseEncryptedSecret(firstBytes)
		if err != nil {
			t.Fatal(err)
		}

		clear(firstBytes)

		opened, err := envelope.Decrypt(parsed, []byte("credential/ibkr-flex"))
		if err != nil {
			t.Fatal(err)
		}

		got, err := opened.Copy()
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(got, plaintext) {
			t.Fatalf("plaintext = %q, want %q", got, plaintext)
		}

		clear(got)
		opened.Clear()
		secret.Clear()
	}
}

func TestSecretValuesAreDefensiveAndRedacted(t *testing.T) {
	input := []byte("must-not-appear")
	secret := NewSecret(input)
	input[0] = 'X'

	copy, err := secret.Copy()
	if err != nil {
		t.Fatal(err)
	}

	if string(copy) != "must-not-appear" {
		t.Fatalf("secret copy = %q", copy)
	}

	copy[0] = 'Y'
	copy, err = secret.Copy()
	if err != nil || string(copy) != "must-not-appear" {
		t.Fatalf("defensive copy = %q, %v", copy, err)
	}

	for _, format := range []string{"%v", "%+v", "%#v", "%x", "%s"} {
		if output := fmt.Sprintf(format, secret); stringsContain(output, "must-not-appear") {
			t.Fatalf("format %q exposed secret: %q", format, output)
		}
	}

	if _, err := json.Marshal(secret); !errors.Is(err, ErrSecretSerialization) {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	owned := secret.state.value
	secret.Clear()

	if _, err := secret.Copy(); !errors.Is(err, ErrDestroyedSecret) {
		t.Fatalf("cleared secret error = %v", err)
	}

	if !bytes.Equal(owned, make([]byte, len(owned))) {
		t.Fatal("Clear() did not overwrite secret bytes")
	}
}

func TestCredentialValuesRejectSerializationAndRedact(t *testing.T) {
	keyBytes := []byte("12345678901234567890123456789012")

	key, err := NewMasterKey(keyBytes)
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := NewEnvelope(key)
	if err != nil {
		t.Fatal(err)
	}

	secret := NewSecret([]byte("must-not-appear"))

	encrypted, err := envelope.Encrypt(secret, []byte("credential/ibkr-flex"))
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []any{key, envelope, encrypted} {
		if output := fmt.Sprintf("%#v", value); stringsContain(output, "must-not-appear") ||
			stringsContain(output, string(keyBytes)) {
			t.Fatalf("format exposed credential material: %q", output)
		}

		if _, err := json.Marshal(value); !errors.Is(err, ErrSecretSerialization) {
			t.Fatalf("json.Marshal(%T) error = %v", value, err)
		}
	}
}

func TestCredentialCopiesShareClearedState(t *testing.T) {
	secret := NewSecret([]byte("token"))
	secretCopy := *secret
	secret.Clear()

	if _, err := secretCopy.Copy(); !errors.Is(err, ErrDestroyedSecret) {
		t.Fatalf("cleared secret copy error = %v", err)
	}

	key, err := NewMasterKey(bytes.Repeat([]byte{1}, dataKeySize))
	if err != nil {
		t.Fatal(err)
	}

	keyCopy := *key
	key.Clear()

	if _, err := NewEnvelope(&keyCopy); !errors.Is(err, ErrDestroyedMasterKey) {
		t.Fatalf("cleared key copy error = %v", err)
	}

	key, err = NewMasterKey(bytes.Repeat([]byte{1}, dataKeySize))
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := NewEnvelope(key)
	if err != nil {
		t.Fatal(err)
	}

	envelopeCopy := *envelope
	envelope.Clear()

	if _, err := envelopeCopy.Encrypt(
		NewSecret([]byte("token")),
		[]byte("credential/ibkr-flex"),
	); !errors.Is(
		err,
		ErrDestroyedEnvelope,
	) {
		t.Fatalf("cleared envelope copy error = %v", err)
	}
}

func TestCredentialLifecycleIsRaceFree(t *testing.T) {
	key, err := NewMasterKey(bytes.Repeat([]byte{1}, dataKeySize))
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := NewEnvelope(key)
	if err != nil {
		t.Fatal(err)
	}

	secret := NewSecret([]byte("token"))

	var group sync.WaitGroup
	group.Add(3)
	go func() {
		defer group.Done()

		for range 1000 {
			_, _ = secret.Copy()
		}
	}()
	go func() {
		defer group.Done()

		for range 1000 {
			_, _ = envelope.Encrypt(secret, []byte("credential/ibkr-flex"))
		}
	}()
	go func() {
		defer group.Done()

		secret.Clear()
		envelope.Clear()
	}()

	group.Wait()
}

func TestEnvelopeRejectsInvalidInputsAndTampering(t *testing.T) {
	key, err := NewMasterKey(bytes.Repeat([]byte{1}, dataKeySize))
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := NewEnvelope(key)
	if err != nil {
		t.Fatal(err)
	}

	secret := NewSecret([]byte("token"))
	if _, err := envelope.Encrypt(secret, nil); !errors.Is(err, ErrInvalidAAD) {
		t.Fatalf("empty AAD error = %v", err)
	}

	encrypted, err := envelope.Encrypt(secret, []byte("credential/ibkr-flex"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := envelope.Decrypt(encrypted, []byte("credential/other")); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong AAD error = %v", err)
	}

	wrongKey, err := NewMasterKey(bytes.Repeat([]byte{2}, dataKeySize))
	if err != nil {
		t.Fatal(err)
	}

	wrongEnvelope, err := NewEnvelope(wrongKey)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := wrongEnvelope.Decrypt(
		encrypted,
		[]byte("credential/ibkr-flex"),
	); !errors.Is(
		err,
		ErrAuthentication,
	) {
		t.Fatalf("wrong key error = %v", err)
	}

	encoded, err := encrypted.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	for _, index := range []int{0, 1, 1 + nonceSize, 1 + nonceSize + wrappedKeySize} {
		tampered := append([]byte(nil), encoded...)

		tampered[index] ^= 1
		parsed, err := ParseEncryptedSecret(tampered)
		if index == 0 {
			if !errors.Is(err, ErrUnsupportedVersion) {
				t.Fatalf("version tamper error = %v", err)
			}

			continue
		}

		if err != nil {
			t.Fatalf("ParseEncryptedSecret() error = %v", err)
		}

		if _, err := envelope.Decrypt(
			parsed,
			[]byte("credential/ibkr-flex"),
		); !errors.Is(
			err,
			ErrAuthentication,
		) {
			t.Fatalf("tamper at %d error = %v", index, err)
		}
	}

	for _, malformed := range [][]byte{nil, make([]byte, minimumSize-1), append([]byte{2}, make([]byte, minimumSize-1)...)} {
		_, err := ParseEncryptedSecret(malformed)
		if !errors.Is(err, ErrInvalidEncryptedSecret) && !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("ParseEncryptedSecret(%d bytes) error = %v", len(malformed), err)
		}
	}

	secret.Clear()
	envelope.Clear()

	if _, err := envelope.Encrypt(
		secret,
		[]byte("credential/ibkr-flex"),
	); !errors.Is(
		err,
		ErrDestroyedEnvelope,
	) {
		t.Fatalf("cleared envelope error = %v", err)
	}
}

func TestMasterKeyValidation(t *testing.T) {
	for _, length := range []int{0, dataKeySize - 1, dataKeySize + 1} {
		if _, err := NewMasterKey(make([]byte, length)); !errors.Is(err, ErrInvalidMasterKey) {
			t.Fatalf("NewMasterKey(%d bytes) error = %v", length, err)
		}
	}

	key, err := NewMasterKey(make([]byte, dataKeySize))
	if err != nil {
		t.Fatal(err)
	}

	owned := key.state.value
	key.Clear()

	if _, err := NewEnvelope(key); !errors.Is(err, ErrDestroyedMasterKey) {
		t.Fatalf("cleared key error = %v", err)
	}

	if !bytes.Equal(owned[:], make([]byte, len(owned))) {
		t.Fatal("Clear() did not overwrite master key bytes")
	}
}

func stringsContain(value, substring string) bool {
	return bytes.Contains([]byte(value), []byte(substring))
}
