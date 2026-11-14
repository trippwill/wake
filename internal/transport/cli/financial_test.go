// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serveFinancialFixture(t *testing.T, dir string, handler http.HandlerFunc) string {
	t.Helper()

	tokenPath := filepath.Join(dir, "client.token")
	token := strings.Repeat("A", 22) + "." + strings.Repeat("A", 43)
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WAKE_TOKEN_FILE", tokenPath)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing client credential")
		}

		handler(w, r)
	}), ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}

		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})

	return "http://" + listener.Addr().String()
}

func financialCLI(t *testing.T, want int, args ...string) []byte {
	t.Helper()

	var stdout, stderr bytes.Buffer
	if code := run(append([]string{"--json"}, args...), &stdout, &stderr); code != want {
		t.Fatalf("%v exited %d, want %d\nstdout: %s\nstderr: %s", args, code, want, &stdout, &stderr)
	}

	if want == exitOK {
		if stderr.Len() != 0 || !json.Valid(stdout.Bytes()) {
			t.Fatalf("unexpected output\nstdout: %s\nstderr: %s", &stdout, &stderr)
		}

		return stdout.Bytes()
	}

	var event struct {
		Event  string `json:"event"`
		Code   string `json:"code"`
		Status int    `json:"exit_status"`
	}
	if stdout.Len() != 0 || json.Unmarshal(stderr.Bytes(), &event) != nil ||
		event.Event != "error" || event.Code == "" || event.Status != want {
		t.Fatalf("unexpected error output\nstdout: %s\nstderr: %s", &stdout, &stderr)
	}

	return stderr.Bytes()
}

func financialResourceIDs(t *testing.T, body []byte, want ...string) {
	t.Helper()

	var result struct {
		WarehouseID string                `json:"warehouse_id"`
		Version     string                `json:"version"`
		Resources   []struct{ ID string } `json:"resources"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	ids := make([]string, 0, len(result.Resources))
	for _, resource := range result.Resources {
		ids = append(ids, resource.ID)
	}

	if result.WarehouseID != "warehouse" || result.Version != "version" ||
		result.Resources == nil || !slices.Equal(ids, want) {
		t.Fatalf("unexpected result: %s; want IDs %v", body, want)
	}
}

func TestFinancialCLIReads(t *testing.T) {
	const account = `{"id":"acct","kind":"accounts","revision":3,"account":{"name":"Account","currency":null}}`

	activity := func(id, account, date, family, state string) string {
		return fmt.Sprintf(`{"id":%q,"kind":"activity","revision":1,"activity":{"account_id":%q,"date":%q,
			"family":%q,"state":%q,"at":null,"price":{"amount":"1.23000000000000000100","currency":"USD"}}}`,
			id, account, date, family, state,
		)
	}
	snapshot := `{"warehouse_id":"warehouse","version":"version","resources":[` + strings.Join([]string{
		`{"id":"other","kind":"accounts","revision":1,"account":{"name":"Other","currency":"GBP"}}`,
		activity("b", "acct", "2026-10-03", "trade", "accepted"),
		activity("c", "acct", "2026-10-02", "trade", "accepted"),
		activity("d", "acct", "2026-10-03", "trade", "cancelled"),
		activity("e", "acct", "2026-10-03", "cash", "accepted"),
		activity("f", "other", "2026-10-03", "trade", "accepted"),
		activity("g", "acct", "2026-10-04", "trade", "replaced"),
		activity("a", "acct", "2026-10-03", "trade", "accepted"),
		account,
	}, ",") + `]}`

	endpoint := serveFinancialFixture(t, t.TempDir(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}

		w.Header().Set("Content-Type", "application/json")

		switch r.URL.RequestURI() {
		case snapshotRoute:
			if r.Header.Get("If-None-Match") == "*" {
				w.WriteHeader(http.StatusNotModified)
				return
			}

			_, _ = fmt.Fprint(w, snapshot)
		case "/v1/accounts/acct":
			_, _ = fmt.Fprint(w, account)
		case "/v1/accounts/acct/explanation?revision=3":
			_, _ = fmt.Fprint(w, `{"id":"acct","revision":3}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"message":"private diagnostic text"}`)
		}
	})

	call := func(args ...string) []byte {
		return financialCLI(t, exitOK, append(args, "--endpoint", endpoint)...)
	}
	financialResourceIDs(t, call("accounts", "list"), "acct", "other")

	if got := financialCLI(
		t,
		exitOK,
		"accounts",
		"get",
		"--endpoint",
		endpoint,
		"acct",
	); !bytes.Contains(
		got,
		[]byte(`"currency":null`),
	) {
		t.Fatal(string(got))
	}

	query := []string{
		"activity",
		"query",
		"--account",
		"acct",
		"--family",
		"trade",
		"--from",
		"2026-10-02",
		"--to",
		"2026-10-03",
	}
	page := call(append(slices.Clone(query), "--limit", "2")...)
	financialResourceIDs(t, page, "a", "b")

	if !bytes.Contains(page, []byte(`"next_offset":2`)) ||
		!bytes.Contains(page, []byte(`"1.23000000000000000100"`)) || !bytes.Contains(page, []byte(`"at":null`)) {
		t.Fatal(string(page))
	}

	last := call(append(slices.Clone(query), "--offset", "2")...)
	financialResourceIDs(t, last, "c")

	if !bytes.Contains(last, []byte(`"next_offset":null`)) {
		t.Fatal(string(last))
	}

	financialResourceIDs(t, call(append(slices.Clone(query), "--state", "all")...), "a", "b", "d", "c")
	financialResourceIDs(t, call("activity", "query", "--state", "replaced"), "g")
	financialResourceIDs(t, call("activity", "query", "--state", "cancelled"), "d")
	financialResourceIDs(t, call("activity", "query", "--account", "missing"))
	financialResourceIDs(t, call("activity", "query", "--offset", "1000"))

	doctor := call("doctor")
	if !bytes.Contains(doctor, []byte(`"auth":"client_token"`)) ||
		!bytes.Contains(doctor, []byte(`"reachable":true`)) {
		t.Fatal(string(doctor))
	}

	raw := financialCLI(
		t,
		exitOK,
		"request",
		"get",
		"--endpoint",
		endpoint,
		"/v1/accounts/acct/explanation?revision=3",
	)
	if string(bytes.TrimSpace(raw)) != `{"id":"acct","revision":3}` {
		t.Fatal(string(raw))
	}

	stderr := financialCLI(t, exitFailure, "accounts", "get", "--endpoint", endpoint, "missing")
	if !bytes.Contains(stderr, []byte("404")) || bytes.Contains(stderr, []byte("private diagnostic text")) {
		t.Fatal(string(stderr))
	}
}

func TestFinancialCLIPositions(t *testing.T) {
	views := map[string]string{
		"open": `"status":"available","as_of":{"date":"2026-10-03","at":"2026-10-03T12:00:00.123400Z"},
			"reason":null,"positions":[{"instrument_id":"instrument","side":"short",
			"quantity":{"value":"-1.0000000000000000001","unit":"shares"},"option":null,"debt":null}]`,
		"empty":       `"status":"available","as_of":{"date":"2026-10-03","at":null},"reason":null,"positions":[]`,
		"unavailable": `"status":"unavailable","as_of":null,"reason":"not_reported","positions":null`,
	}
	responses := make(map[string]string, len(views))
	for id, view := range views {
		responses["/v1/accounts/"+id+"/positions"] = fmt.Sprintf(
			`{"warehouse_id":"warehouse","account_id":%q,"version":"positions-version",%s}`,
			id,
			view,
		)
	}

	endpoint := serveFinancialFixture(t, t.TempDir(), func(w http.ResponseWriter, r *http.Request) {
		view, ok := responses[r.URL.Path]
		if !ok {
			t.Errorf("unexpected positions request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)

			return
		}

		_, _ = fmt.Fprint(w, view)
	})
	for id, fragment := range map[string]string{
		"open": `"value":"-1.0000000000000000001"`, "empty": `"positions":[]`, "unavailable": `"positions":null`,
	} {
		body := financialCLI(t, exitOK, "positions", "get", "--endpoint", endpoint, id)
		if !bytes.Contains(body, []byte(fragment)) ||
			id == "open" && !bytes.Contains(body, []byte("12:00:00.123400Z")) {
			t.Fatalf("unexpected %s view: %s", id, body)
		}
	}
}

func TestFinancialCLIRejectsInvalidRequestsAndResponses(t *testing.T) {
	for _, args := range [][]string{
		{"activity", "query", "--limit", "0"},
		{"activity", "query", "--limit", "1001"},
		{"activity", "query", "--offset", "-1"},
		{"activity", "query", "--from", "2026-02-29"},
		{"activity", "query", "--from", "2026-10-03", "--to", "2026-10-02"},
		{"activity", "query", "--family", "unknown"},
		{"activity", "query", "--state", "unknown"},
		{"accounts", "get", "a/b"},
		{"positions", "get"},
		{"doctor", "--endpoint", "relative.sock"},
		{"doctor", "--config", "/profile.toml", "--endpoint", "/socket"},
		{"request", "get", "https://example.invalid/v1/snapshot"},
		{"request", "get", "/v1/events?extra=1"},
		{"request", "get", "/v1/%65vents"},
		{"request", "get", "/v1/a/../events"},
		{"request", "post", "/v1/snapshot"},
	} {
		financialCLI(t, exitUsage, args...)
	}

	endpoint := serveFinancialFixture(t, t.TempDir(), func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case snapshotRoute:
			_, _ = fmt.Fprint(w, `{"warehouse_id":"warehouse","version":"version","resources":[]}`)
		case "/v1/accounts/bad":
			_, _ = fmt.Fprint(w, `{"private":"private diagnostic text"}{`)
		case "/v1/accounts/redirect":
			w.Header().Set("Location", "https://example.invalid/v1/snapshot")
			w.WriteHeader(http.StatusTemporaryRedirect)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	})
	financialResourceIDs(t, financialCLI(t, exitOK, "accounts", "list", "--endpoint", endpoint))

	errOut := financialCLI(t, exitFailure, "accounts", "get", "--endpoint", endpoint, "bad")
	if bytes.Contains(errOut, []byte("private diagnostic text")) {
		t.Fatal(string(errOut))
	}

	financialCLI(t, exitFailure, "request", "get", "--endpoint", endpoint, "/v1/accounts/redirect")
	financialCLI(t, exitFailure, "doctor", "--endpoint", "http://127.0.0.1:1")
}

func TestFinancialCLIPreservesValidatedErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{"missing", http.StatusNotFound, `{"code":"not_found","message":"private diagnostic text"}`, "not_found"},
		{"stale", http.StatusConflict, `{"code":"stale_revision","message":"private diagnostic text"}`, "stale_revision"},
		{"invalid request", http.StatusBadRequest, `{"code":"invalid_request","message":"Invalid parameters"}`, "invalid_request"},
		{"method", http.StatusMethodNotAllowed, `{"code":"method_not_allowed","message":"Only GET is supported"}`, "method_not_allowed"},
		{"history update", http.StatusServiceUnavailable, `{"code":"history_updating","message":"Wake is updating retained financial history"}`, "history_updating"},
		{"internal", http.StatusInternalServerError, `{"code":"internal_error","message":"Financial read failed"}`, "internal_error"},
		{"mismatched", http.StatusNotFound, `{"code":"stale_revision","message":"private diagnostic text"}`, "operation_failed"},
		{"unknown", http.StatusNotFound, `{"code":"other","message":"private diagnostic text"}`, "operation_failed"},
		{"incomplete", http.StatusConflict, `{"code":"stale_revision"`, "operation_failed"},
		{"oversized", http.StatusNotFound, `{"code":"not_found","message":"` + strings.Repeat("a", maxFinancialError) + `"}`, "operation_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := serveFinancialFixture(t, t.TempDir(), func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			})
			out := financialCLI(t, exitFailure, "request", "get", "--endpoint", endpoint,
				"/v1/accounts/acct/explanation?revision=3")
			if !bytes.Contains(out, []byte(`"code":"`+tc.code+`"`)) ||
				bytes.Contains(out, []byte("private diagnostic text")) {
				t.Fatal(string(out))
			}
		})
	}
}

func TestFinancialCLIConfinesIdentityRedirects(t *testing.T) {
	const prefix = "/v1/accounts/"

	five := []string{prefix + "a", prefix + "b", prefix + "c", prefix + "d", prefix + "acct"}
	for _, tc := range []struct {
		name      string
		start     string
		locations []string
		followed  int32
		success   bool
	}{
		{"five hops", prefix + "retired", five, 5, true},
		{"six hops", prefix + "retired", append(slices.Clone(five), prefix+"new"), 5, false},
		{"encoded identity", prefix + "retired", []string{prefix + "%61cct"}, 1, true},
		{"loop", prefix + "retired", []string{prefix + "a", prefix + "%72etired"}, 1, false},
		{"external", prefix + "retired", []string{"http://outside.invalid" + prefix + "acct"}, 0, false},
		{"absolute same host", prefix + "retired", []string{"http://wake" + prefix + "acct"}, 0, false},
		{"cross kind", prefix + "retired", []string{"/v1/instruments/acct"}, 0, false},
		{"unsupported kind", prefix + "retired", []string{"/v1/custom/acct"}, 0, false},
		{"cross endpoint", prefix + "retired", []string{prefix + "acct/positions"}, 0, false},
		{"malformed", prefix + "retired", []string{prefix + "%zz"}, 0, false},
		{"query", prefix + "retired", []string{prefix + "acct?"}, 0, false},
		{"explanation", prefix + "retired/explanation?revision=3", []string{prefix + "acct/explanation?revision=3"}, 0, false},
		{"positions", prefix + "retired/positions", []string{prefix + "acct/positions"}, 0, false},
		{"snapshot", snapshotRoute, []string{prefix + "acct"}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32

			endpoint := serveFinancialFixture(t, t.TempDir(), func(w http.ResponseWriter, r *http.Request) {
				i := int(requests.Add(1) - 1)
				if i > 0 && (i > len(tc.locations) || r.URL.EscapedPath() != tc.locations[i-1]) {
					t.Errorf("unexpected redirect request %s", r.URL)
				}

				if i < len(tc.locations) {
					w.Header().Set("Location", tc.locations[i])
					w.WriteHeader(http.StatusPermanentRedirect)

					return
				}

				_, _ = fmt.Fprint(
					w,
					`{"id":"acct","kind":"accounts","revision":3,"account":{"name":"Account","currency":null}}`,
				)
			})
			if tc.success {
				out := financialCLI(t, exitOK, "accounts", "get", "--endpoint", endpoint, "retired")
				if !bytes.Contains(out, []byte(`"id":"acct"`)) {
					t.Fatal(string(out))
				}
			} else {
				financialCLI(t, exitFailure, "request", "get", "--endpoint", endpoint, tc.start)
			}

			if got := requests.Load(); got != tc.followed+1 {
				t.Fatalf("made %d requests, want %d", got, tc.followed+1)
			}
		})
	}
}

func TestFinancialCLIHelp(t *testing.T) {
	for _, command := range []string{"accounts list", "accounts get", "activity query", "positions get", "request get", "doctor"} {
		body := financialCLI(t, exitOK, append([]string{"help"}, strings.Fields(command)...)...)
		if !bytes.Contains(body, []byte(`"name":"wake `+command+`"`)) ||
			!bytes.Contains(body, []byte(`"names":["endpoint"]`)) {
			t.Fatal(string(body))
		}
	}
}

func TestFinancialCLIEndpointResolution(t *testing.T) {
	probe := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotModified) }
	first := serveFinancialFixture(t, t.TempDir(), probe)
	second := serveFinancialFixture(t, t.TempDir(), probe)
	t.Setenv("WAKE_ENDPOINT", first)

	for _, test := range []struct {
		args []string
		want string
	}{{nil, first}, {[]string{"--endpoint", second}, second}} {
		body := financialCLI(t, exitOK, append([]string{"doctor"}, test.args...)...)

		var result struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.Unmarshal(body, &result); err != nil || result.Endpoint != test.want {
			t.Fatal(string(body), err)
		}
	}
}

func TestFinancialCLIProtectsTransportCredentials(t *testing.T) {
	var received atomic.Int32

	target := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { received.Add(1); w.WriteHeader(http.StatusOK) },
		),
	)
	defer target.Close()

	endpoint := serveFinancialFixture(t, t.TempDir(), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/v1/accounts/target")
		w.WriteHeader(http.StatusPermanentRedirect)
	})
	financialCLI(t, exitFailure, "accounts", "get", "--endpoint", endpoint, "redirect")

	if received.Load() != 0 {
		t.Fatal("cross-origin request leaked credential")
	}

	tlsServer := httptest.NewTLSServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { received.Add(1); w.WriteHeader(http.StatusOK) },
		),
	)
	defer tlsServer.Close()

	financialCLI(t, exitFailure, "doctor", "--endpoint", tlsServer.URL)

	if received.Load() != 0 {
		t.Fatal("untrusted TLS peer received credential")
	}
}
