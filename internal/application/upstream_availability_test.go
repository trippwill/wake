// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"testing"

	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestProviderCapabilitiesRemainVisibleWithoutUpstream(t *testing.T) {
	r, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	w := New(nil, []provider.Registration{r}, nil, nil, nil, nil)
	c := ConnectionView{Provider: r.Descriptor().ID(), SettingsStored: true, CredentialKeys: []string{"token"}}
	w.decorateConnection(&c)

	if !c.CanAcquire || !c.CanDiscover || !c.CanNormalize || !c.UpstreamUnavailable || c.MissingAccess {
		t.Fatal(c)
	}

	c.CredentialKeys = nil
	w.decorateConnection(&c)

	if !c.MissingAccess || !c.UpstreamUnavailable {
		t.Fatal(c)
	}
}
