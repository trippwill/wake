// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/internal/transport/admin"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestAuthenticatedAccountWorkflowHTTP(t *testing.T) {
	ctx := t.Context()
	store, _ := testStore(t)
	envelope := testEnvelope(t)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	var reports atomic.Int32

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mapping":
			_, _ = fmt.Fprint(w, `[{"warning":"No identifier found."}]`)
		case strings.HasSuffix(r.URL.Path, "SendRequest"):
			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Success</Status><ReferenceCode>synthetic</ReferenceCode></FlexStatementResponse>`,
			)
		default:
			if reports.Add(1) > 1 {
				w.Header().Set("Retry-After", "120")
			}

			_, _ = fmt.Fprint(w, workflowReport(equityRow("U1", "http-fill", "401.000000001")))
		}
	}))
	defer upstream.Close()

	registration, err := ibkrflex.Registration(
		ibkrflex.Options{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
	)
	if err != nil {
		t.Fatal(err)
	}

	h, err := host.New(ctx, store, envelope, nil, func() time.Time { return now }, nil, registration)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	refs, err := reference.New(
		store,
		envelope,
		upstream.Client(),
		upstream.URL+"/mapping",
		func() time.Time { return now },
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	workflow := application.New(store, h, refs, func() time.Time { return now }, nil)

	dir := t.TempDir()
	if err = admin.SetUnlock(dir, []byte("synthetic-http-unlock")); err != nil {
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

		if strings.Contains(w.Body.String(), "synthetic-token") {
			t.Fatal("credential exposed in response")
		}

		return w
	}
	login := request(
		http.MethodPost,
		"/login",
		url.Values{"unlock": {"synthetic-http-unlock"}},
		http.StatusSeeOther,
	)
	cookie = login.Result().Cookies()[0]
	page := request(http.MethodGet, "/connections/new", nil, http.StatusOK).Body.String()

	_, rest, found := strings.Cut(page, `name="csrf" value="`)
	if !found {
		t.Fatal("missing CSRF form")
	}

	csrf, _, found = strings.Cut(rest, `"`)
	if !found || csrf == "" {
		t.Fatal("invalid CSRF form")
	}

	request(
		http.MethodPost,
		"/connections",
		url.Values{
			"provider":         {"ibkr-flex"},
			"connection_name":  {"Synthetic"},
			"setting.query_id": {"invalid"},
			"secret.token":     {"synthetic-token"},
		},
		http.StatusBadRequest,
	)
	request(
		http.MethodPost,
		"/connections",
		url.Values{
			"provider":         {"ibkr-flex"},
			"connection_name":  {"Synthetic"},
			"setting.query_id": {"123"},
			"secret.token":     {"synthetic-token"},
		},
		http.StatusSeeOther,
	)

	view, err := inspectWorkflow(ctx, store, workflow)
	if err != nil || len(view.Connections) != 1 {
		t.Fatal(view, err)
	}

	id := view.Connections[0].ID
	request(
		http.MethodPost,
		"/connections/"+id+"/settings",
		url.Values{
			"provider":         {"ibkr-flex"},
			"connection_name":  {"Updated synthetic"},
			"setting.query_id": {"123"},
		},
		http.StatusSeeOther,
	)
	request(
		http.MethodPost,
		"/connections/"+id+"/secrets",
		url.Values{"secret_key": {"token"}, "secret.token": {"synthetic-token"}},
		http.StatusSeeOther,
	)
	request(http.MethodPost, "/connections/"+id+"/retry", nil, http.StatusSeeOther)
	request(http.MethodPost, "/reference/key", url.Values{"key": {"synthetic-reference"}}, http.StatusSeeOther)

	run := func() {
		t.Helper()

		if err = workflow.RunOnce(ctx, now); err != nil {
			t.Fatal(err)
		}

		now = now.Add(10 * time.Second)
	}
	run()
	run()

	view, err = inspectWorkflow(ctx, store, workflow)
	if err != nil || len(view.Connections[0].Candidates) != 2 {
		t.Fatal(view, err)
	}

	candidate := view.Connections[0].Candidates[0]
	ref := candidate.Candidate.Reference()
	path := "/connections/" + id + "/confirm"
	request(
		http.MethodGet,
		path+"?"+url.Values{
			"artifact":  {candidate.Artifact},
			"account":   {ref.Value()},
			"authority": {ref.Authority()},
			"kind":      {ref.Kind()},
			"scope":     {ref.Scope()},
		}.Encode(),
		nil,
		http.StatusOK,
	)
	values := url.Values{
		"artifact":  {candidate.Artifact},
		"authority": {ref.Authority()},
		"kind":      {ref.Kind()},
		"scope":     {ref.Scope()},
		"account":   {ref.Value()},
		"name":      {"Synthetic account"},
		"currency":  {"USD"},
	}
	request(http.MethodPost, path, values, http.StatusSeeOther)

	if view, err = inspectWorkflow(
		ctx,
		store,
		workflow,
	); err != nil || view.Account.ID() == "" ||
		len(view.Executions) != 0 {
		t.Fatal("confirmation did not retain authority boundary", view, err)
	}

	run()
	run()

	view, err = inspectWorkflow(ctx, store, workflow)
	if err != nil || len(view.Executions) != 1 || len(view.Instruments) != 1 {
		t.Fatal(view, err)
	}

	overview := request(http.MethodGet, "/", nil, http.StatusOK).Body.String()
	for _, want := range []string{
		"Last successful retrieval",
		"Next event",
		"Refresh now",
		`datetime="` + now.Add(-10*time.Second).Format(time.RFC3339Nano) + `"`,
	} {
		if !strings.Contains(overview, want) {
			t.Fatalf("Overview missing %q", want)
		}
	}

	key := host.WorkKey{Connection: id, Stream: ibkrflex.Confirmations, Operation: "acquire"}

	before, err := store.Checkpoint(ctx, key)
	if err != nil || !before.Next.Equal(now.Add(5*time.Minute-10*time.Second)) {
		t.Fatal("unexpected routine poll", before.Next, err)
	}

	pollingPath := "/connections/" + id + "/polling"
	for _, values := range []url.Values{
		{"stream": {key.Stream}},
		{"stream": {"unknown"}, "interval": {"3m"}},
		{"stream": {key.Stream}, "interval": {"30s"}},
	} {
		rejected := request(http.MethodPost, pollingPath, values, http.StatusBadRequest)
		if !strings.Contains(rejected.Body.String(), "Enter an interval within the displayed range") {
			t.Fatal("invalid polling input did not explain how to correct it")
		}

		unchanged, err := store.Checkpoint(ctx, key)
		if err != nil || !unchanged.Next.Equal(before.Next) {
			t.Fatal("invalid polling input moved the deadline", unchanged.Next, err)
		}
	}

	accessPath := "/connections/" + id + "?view=access"
	for _, edit := range []struct {
		value, shown string
		delay        time.Duration
	}{
		{"3m", "3m", 3 * time.Minute},
		{"1m", "1m", 2 * time.Minute},
		{"8m", "8m", 8 * time.Minute},
		{"2h", "2h", 2 * time.Hour},
		{"150s", "2m30s", 150 * time.Second},
		{"", "", 5 * time.Minute},
	} {
		request(http.MethodPost, pollingPath, url.Values{
			"stream": {key.Stream}, "interval": {edit.value},
		}, http.StatusSeeOther)

		updated, err := store.Checkpoint(ctx, key)
		if err != nil || !updated.Next.Equal(now.Add(edit.delay-10*time.Second)) ||
			updated.Revision != before.Revision || !bytes.Equal(updated.State.Payload(), before.State.Payload()) {
			t.Fatalf("edit %q produced unexpected checkpoint: deadline %v, %v", edit.value, updated.Next, err)
		}

		access := request(http.MethodGet, accessPath, nil, http.StatusOK).Body.String()
		if !strings.Contains(access, `id="poll-interval-confirmations" name="interval" value="`+edit.shown+`"`) {
			t.Fatalf("access page lost the saved interval %q", edit.shown)
		}
	}

	reconcile := func(interval time.Duration) {
		t.Helper()

		defaults := map[string]time.Duration{"ibkr-flex:confirmations": interval}

		restarted, err := host.New(
			ctx,
			store,
			envelope,
			nil,
			func() time.Time { return now },
			defaults,
			registration,
		)
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()

		if err := store.ReconcilePolls(ctx, restarted.PollSettings); err != nil {
			t.Fatal(err)
		}
	}

	for _, launch := range []struct{ interval, delay time.Duration }{
		{3 * time.Minute, 3 * time.Minute},
		{time.Minute, 2 * time.Minute},
		{5 * time.Minute, 5 * time.Minute},
	} {
		reconcile(launch.interval)

		updated, err := store.Checkpoint(ctx, key)
		if err != nil || !updated.Next.Equal(now.Add(launch.delay-10*time.Second)) {
			t.Fatalf("launch interval %v produced unexpected deadline %v: %v", launch.interval, updated.Next, err)
		}
	}

	refresh := request(http.MethodPost, "/refresh", nil, http.StatusSeeOther)
	if refresh.Header().Get("Location") != "/?refresh=requested" {
		t.Fatal("refresh did not bring forward the routine poll")
	}

	// Saving the same effective interval keeps the explicitly advanced poll.
	request(http.MethodPost, pollingPath, url.Values{
		"stream": {key.Stream}, "interval": {"5m"},
	}, http.StatusSeeOther)

	// An override keeps the same effective interval when the launch default changes.
	reconcile(8 * time.Minute)

	after, err := store.Checkpoint(ctx, key)
	if err != nil || !after.Next.Equal(now.Add(110*time.Second)) || before.Revision != after.Revision ||
		before.State.Version() != after.State.Version() || !bytes.Equal(before.State.Payload(), after.State.Payload()) {
		t.Fatal("refresh moved the provider deadline or changed its continuation", before.Next, after.Next, err)
	}

	refreshed, err := workflow.Overview(ctx)
	if err != nil || len(refreshed.Status.Connections) != 1 {
		t.Fatal("refresh status unavailable", err)
	}

	work := refreshed.Status.Connections[0].Work
	index := slices.IndexFunc(work, func(work application.WorkStatus) bool {
		return work.Stream == key.Stream && work.Operation == key.Operation
	})
	if index < 0 || !work[index].Next.Equal(after.Next) ||
		!work[index].LastRetrieved.Equal(now.Add(-10*time.Second)) {
		t.Fatal("Overview timing differs from the refreshed checkpoint", work, after.Next)
	}

	repeat := request(http.MethodPost, "/refresh", nil, http.StatusSeeOther)
	if repeat.Header().Get("Location") != "/?refresh=idle" {
		t.Fatal("repeated refresh moved a poll past the cooldown")
	}

	pending, err := store.PendingWork(ctx, after.Next.Add(-time.Second))
	if err != nil || len(pending) != 0 {
		t.Fatal("poll became runnable before the cooldown", pending, err)
	}

	pending, err = store.PendingWork(ctx, after.Next)
	if err != nil || len(pending) != 1 || pending[0] != key {
		t.Fatal("poll did not become runnable at the cooldown", pending, err)
	}

	for _, path = range []string{"/connections", "/connections/" + id, "/activity", "/executions/" + view.Executions[0].ID(), "/instruments/" + view.Instruments[0].ID(), "/evidence/" + candidate.Artifact} {
		request(http.MethodGet, path, nil, http.StatusOK)
	}

	for _, path = range []string{"/connections/missing/confirm", "/executions/missing", "/instruments/missing", "/evidence/missing", "/reference-evidence/missing"} {
		request(http.MethodGet, path, nil, http.StatusNotFound)
	}

	request(http.MethodPost, "/connections/"+id+"/retry", nil, http.StatusSeeOther)

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	failedRefresh := request(http.MethodPost, "/refresh", nil, http.StatusServiceUnavailable)
	if !strings.Contains(failedRefresh.Body.String(), "Refresh unavailable") {
		t.Fatal("failed refresh did not explain the outcome")
	}

	for _, path = range []string{"/connections", "/connections/" + id, "/activity", "/connections/" + id + "/confirm?" + url.Values{"artifact": {candidate.Artifact}, "account": {ref.Value()}, "authority": {ref.Authority()}, "kind": {ref.Kind()}, "scope": {ref.Scope()}}.Encode()} {
		request(http.MethodGet, path, nil, http.StatusServiceUnavailable)
	}
}
