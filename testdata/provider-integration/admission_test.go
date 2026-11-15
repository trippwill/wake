// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package providerproof_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
)

func TestIndependentCallbackContextsRetainHostAuthorization(t *testing.T) {
	for _, scenario := range []string{"revision changed", "grant revoked"} {
		t.Run(scenario, func(t *testing.T) {
			base := registration(t)
			var f *fixtureHost
			acquire := acquireFunc(func(_ context.Context, _ provider.Request, a provider.Access) (provider.Step, error) {
				callback, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := a.WithSecret(callback, "api_secret", func([]byte) error { return nil }); err != nil {
					t.Fatalf("independent callback before access change: %v", err)
				}
				var err error
				if scenario == "grant revoked" {
					err = f.store.PauseForCredentialKey(t.Context())
				} else {
					err = f.store.SetReferenceKey(t.Context(), nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = a.WithSecret(callback, "api_secret", func([]byte) error {
					t.Error("obsolete capability opened credential bytes")
					return nil
				}); !errors.Is(err, provider.ErrUnauthorized) {
					t.Errorf("obsolete credential read: %v", err)
				}
				progress, err := provider.NewProgress(provider.ProgressRetained, 1, 1)
				if err != nil {
					t.Fatal(err)
				}
				if err = a.Report(callback, progress); !errors.Is(err, host.ErrStorage) {
					t.Errorf("obsolete progress write: %v", err)
				}
				scope, err := provider.NewPacingScope([]byte("synthetic admission proof"))
				if err != nil {
					t.Fatal(err)
				}
				if reservation, e := a.ReservePacing(callback, "requests", scope); !errors.Is(e, host.ErrStorage) || reservation.Granted() {
					t.Errorf("obsolete pacing reservation: %v %v", reservation, e)
				}
				if err = a.DelayPacing(callback, "requests", scope, f.now.Add(time.Hour)); !errors.Is(err, host.ErrStorage) {
					t.Errorf("obsolete pacing delay: %v", err)
				}
				return provider.Step{}, provider.ErrUnauthorized
			})
			registration, err := provider.NewRegistration(base.Descriptor(), base.Version(), acquire, base.Normalizer(), base.Discoverer(), nil)
			if err != nil {
				t.Fatal(err)
			}
			f = newFixture(t, registration)
			if _, err = f.host.Acquire(t.Context(), f.id, "statements"); err == nil {
				t.Fatal("obsolete operation was accepted")
			}
			cp := f.checkpoint(t, "acquire")
			if cp.Revision != 0 || cp.Observed.Phase() != "" || f.count(t, "pacing") != 0 {
				t.Fatal("obsolete callbacks changed durable state", cp)
			}
		})
	}
}
