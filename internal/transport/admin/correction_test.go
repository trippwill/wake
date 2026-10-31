// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/provider"
)

type staleCorrectionStore struct{ application.Store }

func (staleCorrectionStore) SaveDecision(context.Context, application.Decision, string, time.Time) error {
	return application.ErrStale
}

func (staleCorrectionStore) CorrectionForm(
	context.Context,
	string,
	string,
) (application.CorrectionForm, error) {
	return application.CorrectionForm{
		Kind:     "activity",
		ID:       "activity",
		Label:    "Synthetic investment",
		Revision: 8,
		Fields:   []string{"price"},
		Values:   map[string]application.CorrectionValue{"price": {Value: "4.500000000000001", Currency: "USD"}},
	}, nil
}

func TestStaleCorrectionRetainsDraftAndShowsCurrentRevision(t *testing.T) {
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-correction-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	login := httptest.NewRecorder()

	csrf := auth.issue(login)

	workflow := application.New(staleCorrectionStore{}, nil, nil, nil, nil)

	handler, err := NewHandler(auth, Dependencies{Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}

	draft := url.Values{
		"csrf":       {csrf},
		"revision":   {"7"},
		"submission": {"original-submission-1234"},
		"scope":      {"price"},
		"value":      {"3.700000000000001"},
		"currency":   {"USD"},
		"rationale":  {"Keep <original> reasoning"},
		"action":     {"set"},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1:8787/activity/activity/correct",
		strings.NewReader(draft.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://127.0.0.1:8787")
	request.AddCookie(login.Result().Cookies()[0])

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusConflict {
		t.Fatal(response.Code, body)
	}

	for _, want := range []string{`value="3.700000000000001"`, `value="USD"`, `Keep &lt;original&gt; reasoning`, `name="revision" value="8"`, `4.500000000000001 USD`, `Save reviewed correction`} {
		if !strings.Contains(body, want) {
			t.Error("missing retained draft/current context", want)
		}
	}

	if strings.Contains(body, "original-submission-1234") {
		t.Fatal("stale submission identity reused")
	}
}

func TestContactFragmentRequiresAuthentication(t *testing.T) {
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-contact-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	handler, err := NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/status/contact", nil),
	)

	if response.Code != http.StatusUnauthorized {
		t.Fatal(response.Code)
	}
}

func TestSubmissionFailureRetainsPublicFieldsWithoutSecrets(t *testing.T) {
	var output bytes.Buffer

	tenant, _ := provider.NewField("tenant", "Tenant", provider.TextField, true)
	secret, _ := provider.NewField("api_secret", "API secret", provider.SecretField, true)
	stream, _ := provider.NewStream("statements", "synthetic")
	descriptor, _ := provider.NewDescriptor(
		"synthetic",
		"Synthetic",
		[]provider.Stream{stream},
		[]provider.Field{tenant, secret},
		nil,
	)

	draft := url.Values{
		"connection_name":   {"Retained account name"},
		"setting.tenant":    {"123456"},
		"secret.api_secret": {"secret-token-must-not-return"},
		"csrf":              {"untrusted-posted-csrf"},
	}
	if err := SubmissionFailure(
		"verified-session-csrf",
		"/connections",
		"Access needs repair",
		draft, descriptor,
	).Render(context.Background(), &output); err != nil {
		t.Fatal(err)
	}

	body := output.String()
	for _, value := range []string{"Retained account name", "123456", "verified-session-csrf"} {
		if !strings.Contains(body, value) {
			t.Error("lost public draft", value)
		}
	}

	for _, secret := range []string{"secret-token-must-not-return", "untrusted-posted-csrf"} {
		if strings.Contains(body, secret) {
			t.Error("replayed untrusted/private value")
		}
	}
}

type correctionCheckStore struct {
	application.Store
	calls int
}

func (s *correctionCheckStore) CheckCorrection(_ context.Context, d application.Decision) error {
	s.calls++

	if d.Fields().ExpectedRevision != 8 {
		return application.ErrStale
	}

	if d.Fields().Scope != "price" || d.Fields().Text != "12.34" {
		return application.ErrInvalid
	}

	return nil
}

func (*correctionCheckStore) CorrectionForm(
	ctx context.Context,
	kind, id string,
) (application.CorrectionForm, error) {
	return staleCorrectionStore{}.CorrectionForm(ctx, kind, id)
}

func TestCorrectionCheckIsAuthenticatedAndDoesNotSubmit(t *testing.T) {
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-live-check-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	login := httptest.NewRecorder()

	csrf := auth.issue(login)

	store := &correctionCheckStore{}

	handler, err := NewHandler(
		auth,
		Dependencies{Workflow: application.New(store, nil, nil, nil, nil)},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		token, origin, revision, value string
		status                         int
		valid, stale                   bool
	}{
		{csrf, "http://127.0.0.1:8787", "8", "12.34", 200, true, false},
		{csrf, "http://127.0.0.1:8787", "8", "bad", 200, false, false},
		{csrf, "http://127.0.0.1:8787", "7", "12.34", 200, false, true},
		{"bad", "http://127.0.0.1:8787", "8", "12.34", 403, false, false},
		{csrf, "http://outside.invalid", "8", "12.34", 403, false, false},
	} {
		body := url.Values{
			"csrf":       {test.token},
			"revision":   {test.revision},
			"value":      {test.value},
			"currency":   {"USD"},
			"scope":      {"price"},
			"action":     {"set"},
			"submission": {"synthetic-check-submission"},
		}
		req := httptest.NewRequest(
			http.MethodPost,
			"http://127.0.0.1:8787/activity/activity/correct/check",
			strings.NewReader(body.Encode()),
		)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", test.origin)
		req.AddCookie(login.Result().Cookies()[0])

		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)

		if result.Code != test.status {
			t.Fatal(result.Code, result.Body.String())
		}

		if result.Code == 200 {
			var response struct{ Valid, Stale bool }
			if err := json.Unmarshal(
				result.Body.Bytes(),
				&response,
			); err != nil || response.Valid != test.valid ||
				response.Stale != test.stale {
				t.Fatal(response, err, result.Body.String())
			}
		}
	}

	// Metadata rejection must not blame an otherwise valid financial value.
	for _, rationale := range []string{" leading space", "trailing space ", "embedded\tcontrol", strings.Repeat("é", 600)} {
		for _, path := range []string{"/activity/activity/correct/check", "/activity/activity/correct"} {
			body := url.Values{
				"csrf":       {csrf},
				"revision":   {"8"},
				"value":      {"12.34"},
				"currency":   {"USD"},
				"scope":      {"price"},
				"action":     {"set"},
				"submission": {"synthetic-rationale-check"},
				"rationale":  {rationale},
			}
			req := httptest.NewRequest(
				http.MethodPost,
				"http://127.0.0.1:8787"+path,
				strings.NewReader(body.Encode()),
			)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "http://127.0.0.1:8787")
			req.AddCookie(login.Result().Cookies()[0])

			result := httptest.NewRecorder()
			handler.ServeHTTP(result, req)

			want := http.StatusBadRequest
			if strings.HasSuffix(path, "/check") {
				want = http.StatusOK
			}

			if result.Code != want || !strings.Contains(result.Body.String(), "including the optional rationale") ||
				strings.Contains(result.Body.String(), "Price: Enter") {
				t.Errorf(
					"metadata error directs operator to wrong input: %d %s",
					result.Code,
					result.Body.String()[:min(250, result.Body.Len())],
				)
			}

			if !strings.HasSuffix(path, "/check") && !strings.Contains(result.Body.String(), `value="12.34"`) {
				t.Error("metadata rejection lost the valid financial draft")
			}
		}
	}

	if store.calls != 3 {
		t.Fatal("rejected request reached correction checker", store.calls)
	}
}
