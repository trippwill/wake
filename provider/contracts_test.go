// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"bytes"
	"testing"
	"time"

	"go.trippwill.dev/wake/provider"
)

func TestImmutableContinuationEvidenceAndAccounting(t *testing.T) {
	payload := []byte(`{"page":1}`)

	s, err := provider.NewState("v1", payload)
	if err != nil {
		t.Fatal(err)
	}

	payload[0] = 'x'
	copy := s.Payload()
	copy[0] = 'y'

	if string(s.Payload()) != `{"page":1}` {
		t.Fatal("mutable continuation")
	}

	if _, err = provider.NewState("v1", bytes.Repeat([]byte("x"), 65537)); err != provider.ErrLimit {
		t.Fatal("unbounded continuation", err)
	}

	a, err := provider.NewArtifact("fixture", "text/plain", time.Now(), payload)
	if err != nil {
		t.Fatal(err)
	}

	hash := a.Hash()
	payload[0] = 'z'
	copy = a.Content()
	copy[0] = 'q'

	if a.Hash() != hash {
		t.Fatal("mutable evidence")
	}

	if _, err = provider.NewResult(provider.Counts{Rows: 1, Trades: 1, Rejected: 1}); err == nil {
		t.Fatal("inconsistent counts")
	}

	if _, err = provider.NewProgress("private error text", 0, 0); err == nil {
		t.Fatal("unsafe progress")
	}
}
