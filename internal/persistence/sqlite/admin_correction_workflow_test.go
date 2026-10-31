// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestAuthenticatedCorrectionsAndRecoveryHTTP(t *testing.T) {
	ctx := t.Context()
	store := coverageStore(t)
	coverageReport(t, store, "synthetic-option", syntheticOption)

	view, err := store.Dashboard(ctx)
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	id := view.Executions[0].ID()
	instrument := view.Executions[0].Instrument()
	workflow := application.New(store, nil, nil, nil, nil)

	dir := t.TempDir()
	if err = admin.SetUnlock(dir, []byte("synthetic-correction-unlock")); err != nil {
		t.Fatal(err)
	}

	const origin = "http://127.0.0.1:8787"

	auth, enabled, err := admin.Open(dir, "local", origin)
	if err != nil || !enabled {
		t.Fatal(enabled, err)
	}
	defer auth.Close()

	handler, err := admin.NewHandler(auth, admin.Dependencies{Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}

	var cookie *http.Cookie

	csrf := ""
	request := func(method, path string, values url.Values, want int) *httptest.ResponseRecorder {
		t.Helper()

		if values == nil {
			values = url.Values{}
		}

		if csrf != "" {
			values.Set("csrf", csrf)
		}

		r := httptest.NewRequest(method, origin+path, strings.NewReader(values.Encode())).WithContext(ctx)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)

		if cookie != nil {
			r.AddCookie(cookie)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code != want {
			t.Fatalf("%s %s: %d: %s", method, path, w.Code, w.Body.String())
		}

		return w
	}
	login := request(
		http.MethodPost,
		"/login",
		url.Values{"unlock": {"synthetic-correction-unlock"}},
		http.StatusSeeOther,
	)
	cookie = login.Result().Cookies()[0]
	path := "/activity/" + id + "/correct"
	page := request(http.MethodGet, path, nil, http.StatusOK).Body.String()

	_, tail, ok := strings.Cut(page, `name="csrf" value="`)
	if !ok {
		t.Fatal("missing CSRF form")
	}

	csrf, _, ok = strings.Cut(tail, `"`)
	if !ok || csrf == "" {
		t.Fatal("invalid CSRF form")
	}

	form, err := store.CorrectionForm(ctx, "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	values := url.Values{
		"scope":      {"quantity"},
		"value":      {"3.0001"},
		"revision":   {strconv.FormatInt(form.Revision, 10)},
		"action":     {"set"},
		"submission": {rand.Text()},
		"rationale":  {"Synthetic operator review"},
		"actor":      {"forged-actor"},
	}
	request(http.MethodPost, path, values, http.StatusSeeOther)
	request(http.MethodPost, path, values, http.StatusSeeOther)

	form, err = store.CorrectionForm(ctx, "activity", id)
	if err != nil || len(form.Decisions) != 1 || !strings.HasPrefix(form.Decisions[0].Actor, "admin/local/") ||
		form.Decisions[0].Actor == "forged-actor" {
		t.Fatal("submission/attribution boundary", form, err)
	}

	detail, err := store.ExecutionDetail(ctx, id)
	if err != nil || detail.Execution.Terms().Fields().Quantity.Value() != "3.0001" {
		t.Fatal("correction not projected", detail, err)
	}

	values.Set("submission", rand.Text())
	values.Set("value", "4")
	request(http.MethodPost, path, values, http.StatusConflict)
	values.Set("revision", strconv.FormatInt(form.Revision, 10))
	values.Set("value", "-1")
	request(http.MethodPost, path, values, http.StatusBadRequest)
	values.Set("revision", "invalid")
	request(http.MethodPost, path, values, http.StatusBadRequest)
	// Instrument naming is independently scoped and leaves financial quantities intact.
	instrumentPath := "/instruments/" + instrument + "/correct"
	request(http.MethodGet, instrumentPath, nil, http.StatusOK)

	instrumentForm, err := store.CorrectionForm(ctx, "instrument", instrument)
	if err != nil {
		t.Fatal(err)
	}

	request(
		http.MethodPost,
		instrumentPath,
		url.Values{
			"scope":      {"name"},
			"value":      {"Reviewed synthetic option"},
			"revision":   {strconv.FormatInt(instrumentForm.Revision, 10)},
			"action":     {"set"},
			"submission": {rand.Text()},
		},
		http.StatusSeeOther,
	)

	named, err := store.InstrumentDetail(ctx, instrument)
	if err != nil || named.Instrument.Name() != "Reviewed synthetic option" {
		t.Fatal(named, err)
	}

	request(http.MethodGet, "/activity/missing/correct", nil, http.StatusNotFound)
	request(http.MethodGet, "/instruments/missing/correct", nil, http.StatusNotFound)
	request(http.MethodPost, "/processing/missing/retry", nil, http.StatusBadRequest)
	request(http.MethodPost, "/processing/synthetic-option/retry", nil, http.StatusSeeOther)
	request(http.MethodPost, "/recovery/resume", url.Values{"revision": {"invalid"}}, http.StatusBadRequest)
	request(http.MethodPost, "/recovery/resume", url.Values{"revision": {"999999"}}, http.StatusBadRequest)
	request(http.MethodPost, "/recovery/rebuild", url.Values{"submission": {"short"}}, http.StatusBadRequest)

	submission := rand.Text()
	request(http.MethodPost, "/recovery/rebuild", url.Values{"submission": {submission}}, http.StatusSeeOther)
	// Fail a committed-group acknowledgement, then recover through the real HTTP retry.
	if _, err = store.db.Exec(
		`CREATE TRIGGER fail_recovery_group BEFORE UPDATE ON recovery_items BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`,
	); err != nil {
		t.Fatal(err)
	}

	if _, err = store.RunRecovery(ctx); err == nil {
		t.Fatal("rebuild failure not retained")
	}

	older := submission
	submission = rand.Text()
	request(http.MethodPost, "/recovery/rebuild", url.Values{"submission": {submission}}, http.StatusSeeOther)

	if _, err = store.RunRecovery(ctx); err == nil {
		t.Fatal("new rebuild failure not retained")
	}

	rejected := request(http.MethodPost, "/recovery/"+older+"/retry", nil, http.StatusBadRequest)
	if !strings.Contains(rejected.Body.String(), "no longer eligible") ||
		strings.Contains(rejected.Body.String(), "account display name") {
		t.Fatal("ineligible rebuild returned unrelated setup guidance")
	}

	if _, err = store.db.Exec(`DROP TRIGGER fail_recovery_group`); err != nil {
		t.Fatal(err)
	}

	request(http.MethodPost, "/recovery/"+submission+"/retry", nil, http.StatusSeeOther)

	for i := 0; i < 10; i++ {
		changed, e := store.RunRecovery(ctx)
		if e != nil {
			t.Fatal(e)
		}

		if !changed {
			break
		}

		if i == 9 {
			t.Fatal("rebuild failed to finish")
		}
	}

	detail, err = store.ExecutionDetail(ctx, id)
	named, nameErr := store.InstrumentDetail(ctx, instrument)
	if err != nil || nameErr != nil || detail.Execution.Terms().Fields().Quantity.Value() != "3.0001" ||
		named.Instrument.Name() != "Reviewed synthetic option" {
		t.Fatal("rebuild lost retained decisions", err, nameErr)
	}

	form, err = store.CorrectionForm(ctx, "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	request(
		http.MethodPost,
		path,
		url.Values{
			"scope":      {"quantity"},
			"revision":   {fmt.Sprint(form.Revision)},
			"action":     {"release"},
			"submission": {rand.Text()},
		},
		http.StatusSeeOther,
	)

	detail, err = store.ExecutionDetail(ctx, id)
	if err != nil || detail.Execution.Terms().Fields().Quantity.Value() != "2" {
		t.Fatal("release did not restore reported quantity", detail, err)
	}

	request(http.MethodPost, "/recovery/missing/retry", nil, http.StatusBadRequest)
}
