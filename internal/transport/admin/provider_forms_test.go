// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
)

type formProvider struct{}

func (formProvider) Acquire(context.Context, provider.Request, provider.Access) (provider.Step, error) {
	return provider.Step{}, provider.ErrUnavailable
}

type formStore struct {
	host.Store
	connection host.Connection
	lookups    int
}

func (s *formStore) Connection(context.Context, string) (host.Connection, error) {
	s.lookups++
	return s.connection, nil
}

func (s *formStore) Configure(_ context.Context, c host.Connection, _ map[string][]byte) error {
	s.connection = c
	return nil
}

func formWorkflow(t *testing.T) (*application.Workflow, *formStore, provider.Descriptor) {
	t.Helper()

	fields := []provider.Field{}
	for index := range 64 {
		key := "field" + strconv.Itoa(index)

		field, err := provider.NewField(key, "Public "+key, provider.TextField, true)
		if err != nil {
			t.Fatal(err)
		}

		fields = append(fields, field)
	}

	stream, _ := provider.NewStream("records", "synthetic")

	descriptor, err := provider.NewDescriptor("synthetic", "Synthetic", []provider.Stream{stream}, fields, nil)
	if err != nil {
		t.Fatal(err)
	}

	registration, err := provider.NewRegistration(descriptor, "v1", formProvider{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	store := &formStore{connection: host.Connection{ID: "one", Provider: "synthetic", Name: "Synthetic"}}

	h, err := host.New(t.Context(), store, nil, nil, nil, registration)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(h.Close)
	// A nil financial store makes an accidental dashboard read fail this focused proof.
	return application.New(nil, h, nil, nil, nil), store, descriptor
}

func TestConnectionDescriptorNeedsNoFinancialHistory(t *testing.T) {
	workflow, store, descriptor := formWorkflow(t)
	request := httptest.NewRequest(http.MethodPost, "/connections/one/settings", nil)
	request.SetPathValue("id", "one")

	request.PostForm = url.Values{"provider": {"untrusted-other-provider"}}
	if got := requestDescriptor(request, workflow); got.ID() != descriptor.ID() || store.lookups != 1 {
		t.Fatal("descriptor lookup did not use the connection", got, store.lookups)
	}
}

func TestConnectionPostAcceptsValidSDKFieldSizes(t *testing.T) {
	workflow, store, descriptor := formWorkflow(t)
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-provider-form-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	login := httptest.NewRecorder()
	csrf := auth.issue(login)

	handler, err := NewHandler(auth, Dependencies{Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}

	values := url.Values{
		"csrf":            {csrf},
		"provider":        {descriptor.ID()},
		"connection_name": {"Large synthetic settings"},
	}
	value := strings.Repeat("a&+", 1365) + "x"
	for _, field := range descriptor.Fields() {
		values.Set("setting."+field.Key(), value)
	}

	body := values.Encode()
	if len(body) <= 16<<10 {
		t.Fatal("regression did not exceed previous request limit")
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1:8787/connections",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://127.0.0.1:8787")
	request.AddCookie(login.Result().Cookies()[0])

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatal(response.Code, response.Body.String())
	}

	for _, field := range descriptor.Fields() {
		if got, _ := store.connection.Settings.Get(field.Key()); got != value {
			t.Fatal("valid provider setting was truncated", field.Key())
		}
	}

	oversized := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1:8787/connections",
		strings.NewReader("padding="+strings.Repeat("a", 1<<20)),
	)
	oversized.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	oversized.Header.Set("Origin", "http://127.0.0.1:8787")
	oversized.AddCookie(login.Result().Cookies()[0])

	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, oversized)

	if rejected.Code != http.StatusBadRequest {
		t.Fatal("connection request budget became unbounded", rejected.Code)
	}
}
