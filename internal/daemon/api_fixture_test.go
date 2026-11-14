// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/config"
)

func freeTCPPort(t *testing.T) uint16 {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // An allocated TCP port is within the uint16 range.
	port := uint16(l.Addr().(*net.TCPAddr).Port)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	return port
}

func seedAPIClient(t *testing.T, cfg config.Config) string {
	t.Helper()

	_, secret, err := clientaccess.New(clientaccess.NewFileStore(cfg.Paths.ConfigDir)).
		Create(t.Context(), "Synthetic acceptance client")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Clear()

	value, err := secret.Copy()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(value)

	path := filepath.Join(cfg.Paths.ConfigDir, "test-client.token")
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

type apiTestTransport struct {
	*http.Transport
	token string
}

func (a *apiTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)

	return a.Transport.RoundTrip(r)
}

func financialTransport(t *testing.T, cfg config.Config) *apiTestTransport {
	t.Helper()

	value, err := os.ReadFile(filepath.Join(cfg.Paths.ConfigDir, "test-client.token"))
	if err != nil {
		t.Fatal(err)
	}

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", cfg.API.Address())
	}}
	t.Cleanup(transport.CloseIdleConnections)

	return &apiTestTransport{Transport: transport, token: string(value)}
}
