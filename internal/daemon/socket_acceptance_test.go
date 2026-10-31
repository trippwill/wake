// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

type socketTestLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *socketTestLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.Write(p)
}

func (b *socketTestLog) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }
func (b *socketTestLog) Reset()         { b.mu.Lock(); defer b.mu.Unlock(); b.buffer.Reset() }

// This test executable runs the real daemon in its own process with controlled providers.
func TestSocketDaemonProcess(t *testing.T) {
	raw := os.Getenv("WAKE_SOCKET_DAEMON_CONFIG")
	if raw == "" {
		return
	}

	var cfg config.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}

	cert, err := base64.StdEncoding.DecodeString(os.Getenv("WAKE_SOCKET_DAEMON_CERT"))
	if err != nil {
		t.Fatal(err)
	}

	certificate, err := x509.ParseCertificate(cert)
	if err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(certificate)

	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport, Timeout: time.Second * 10}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()

	endpoint := os.Getenv("WAKE_SOCKET_DAEMON_UPSTREAM")
	if err = run(
		ctx,
		cfg,
		slog.New(slog.NewJSONHandler(os.Stderr, nil)),
		sourceOptions{
			IBKR:            ibkrflex.Options{HTTPClient: client, BaseURL: endpoint},
			ReferenceClient: client,
			ReferenceURL:    endpoint + "/mapping",
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestSocketConsumerAcceptance(t *testing.T) {
	binary := os.Getenv("WAKE_SOCKET_CONSUMER")
	if binary == "" {
		t.Skip("mise run test:socket supplies the independent acceptance binary")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	report, err := os.ReadFile("../../testdata/ibkrflex/confirmations/initial.xml")
	if err != nil {
		t.Fatal(err)
	}

	var reports atomic.Value
	reports.Store(report)

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SendRequest":
			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Success</Status><ReferenceCode>socket-fixture</ReferenceCode></FlexStatementResponse>`,
			)
		case "/GetStatement":
			_, _ = w.Write(reports.Load().([]byte))
		case "/mapping":
			var jobs []any
			if json.NewDecoder(r.Body).Decode(&jobs) != nil {
				http.Error(w, "invalid", http.StatusBadRequest)
				return
			}

			response := make([]map[string]string, len(jobs))
			for i := range response {
				response[i] = map[string]string{"warning": "No identifier found."}
			}

			_ = json.NewEncoder(w).Encode(response)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := shellConfig(t)
	cfg.Credentials.MasterKeyPath = filepath.Join(cfg.Paths.ConfigDir, "master.key")
	if err = credential.CreateMasterKey(cfg.Credentials.MasterKeyPath); err != nil {
		t.Fatal(err)
	}

	key, err := credential.LoadMasterKey(cfg.Credentials.MasterKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Clear()

	envelope, err := credential.NewEnvelope(key)
	if err != nil {
		t.Fatal(err)
	}
	defer envelope.Clear()

	profile := persistence.Profile{
		WarehousePath:   cfg.Storage.WarehousePath,
		ConnectionsPath: cfg.Storage.ConnectionsPath,
	}
	open := func() *persistence.Store {
		t.Helper()

		s, e := persistence.Open(ctx, profile, nil)
		if e != nil {
			t.Fatal(e)
		}

		return s
	}
	store := open()

	registration, err := ibkrflex.Registration(
		ibkrflex.Options{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
	)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()

	providerHost, err := host.New(ctx, store, envelope, nil, func() time.Time { return now }, registration)
	if err != nil {
		t.Fatal(err)
	}

	workflow := application.New(store, providerHost, nil, func() time.Time { return now }, nil)

	secret := credential.NewSecret([]byte("socket-synthetic-token"))
	defer secret.Clear()

	connection, err := workflow.CreateConnection(
		ctx,
		"Socket acceptance",
		"ibkr-flex",
		confirmationSettings("123"),
		map[string]*credential.Secret{"token": secret},
	)
	if err != nil {
		t.Fatal(err)
	}

	advance := func() {
		t.Helper()

		now = now.Add(15 * time.Second)
		if e := workflow.RunOnce(ctx, now); e != nil {
			t.Fatal(e)
		}
	}
	advance()
	advance()

	dashboard, err := workflow.Dashboard(ctx)
	if err != nil || len(dashboard.Connections[0].Candidates) != 1 {
		t.Fatal("discovery", err)
	}

	candidate := dashboard.Connections[0].Candidates[0]

	details, err := application.NewAccountDetails("Socket acceptance account", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = workflow.Confirm(
		ctx,
		connection,
		candidate.Artifact,
		candidate.Candidate.Reference(),
		details,
	); err != nil {
		t.Fatal(err)
	}

	advance()
	advance()

	dashboard, err = workflow.Dashboard(ctx)
	if err != nil || len(dashboard.Executions) != 12 || len(dashboard.Lifecycles) != 3 ||
		len(dashboard.Conversions) != 2 {
		t.Fatal(
			"fixture processing",
			len(dashboard.Executions),
			len(dashboard.Lifecycles),
			len(dashboard.Conversions),
			err,
		)
	}

	if err = store.Schedule(
		ctx,
		host.WorkKey{Connection: connection, Stream: ibkrflex.Confirmations, Operation: "acquire"},
		false,
	); err != nil {
		t.Fatal(err)
	}

	providerHost.Close()

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	socket := filepath.Join(cfg.Paths.RuntimeDir, "wake.sock")

	socketTransport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer socketTransport.CloseIdleConnections()

	socketClient := &http.Client{Transport: socketTransport, Timeout: time.Second}

	var (
		daemon     *exec.Cmd
		daemonDone chan error
		logs       socketTestLog
	)

	stop := func() {
		t.Helper()

		if daemon == nil {
			return
		}

		_ = daemon.Process.Signal(syscall.SIGTERM)
		select {
		case e := <-daemonDone:
			if e != nil {
				t.Fatalf("daemon: %v\n%s", e, logs.String())
			}
		case <-time.After(15 * time.Second):
			_ = daemon.Process.Kill()

			<-daemonDone
			t.Fatal("daemon did not join")
		}

		daemon = nil

		socketTransport.CloseIdleConnections()
	}
	defer stop()

	start := func() {
		t.Helper()
		logs.Reset()

		data, e := json.Marshal(cfg)
		if e != nil {
			t.Fatal(e)
		}

		//nolint:gosec // Re-execute this test binary as the controlled daemon process.
		daemon = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSocketDaemonProcess$")

		daemon.Env = append(
			os.Environ(),
			"WAKE_SOCKET_DAEMON_CONFIG="+string(data),
			"WAKE_SOCKET_DAEMON_UPSTREAM="+upstream.URL,
			"WAKE_SOCKET_DAEMON_CERT="+base64.StdEncoding.EncodeToString(upstream.Certificate().Raw),
		)
		daemon.Stdout = io.Discard

		daemon.Stderr = &logs
		if e = daemon.Start(); e != nil {
			t.Fatal(e)
		}

		daemonDone = make(chan error, 1)
		go func() { daemonDone <- daemon.Wait() }()

		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			response, e := socketClient.Get("http://wake/v1/checkpoint")
			if e == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return
				}
			}

			select {
			case e := <-daemonDone:
				daemon = nil

				t.Fatalf("daemon exited %v\n%s", e, logs.String())
			default:
			}

			time.Sleep(20 * time.Millisecond)
		}

		t.Fatalf("socket did not become ready; daemon logs:\n%s", logs.String())
	}
	statePath := filepath.Join(t.TempDir(), "mirror.json")
	consume := func(assert bool) map[string]json.RawMessage {
		t.Helper()

		args := []string{"--socket", socket, "--state", statePath, "--page-size", "1"}
		if assert {
			args = append(args, "--assert", "../../testdata/socket-consumer/fixtures/confirmations.json")
		}

		//nolint:gosec // The Mise task supplies the independently built acceptance binary; arguments use disposable paths.
		command := exec.CommandContext(ctx, binary, args...)
		// The consumer receives no store locations, private imports or upstream credentials.
		command.Env = []string{"PATH=" + os.Getenv("PATH")}

		var stdout, stderr bytes.Buffer

		command.Stdout = &stdout
		command.Stderr = &stderr
		if e := command.Run(); e != nil {
			t.Fatalf("consumer %v\n%s\n%s", e, stdout.String(), stderr.String())
		}

		if !strings.Contains(stderr.String(), "Connecting to Wake") {
			t.Fatal("no immediate consumer progress")
		}

		var result map[string]json.RawMessage
		if json.Unmarshal(stdout.Bytes(), &result) != nil {
			t.Fatal("consumer output", stdout.String())
		}

		return result
	}
	mirror := func() struct {
		Cursor    string
		Resources map[string]application.CanonicalResource
		History   []struct {
			Cursor   string
			Resource application.CanonicalResource
		}
	} {
		t.Helper()

		var s struct {
			Cursor    string
			Resources map[string]application.CanonicalResource
			History   []struct {
				Cursor   string
				Resource application.CanonicalResource
			}
		}

		//nolint:gosec // statePath belongs to this test's disposable directory.
		data, e := os.ReadFile(statePath)
		if e != nil || json.Unmarshal(data, &s) != nil {
			t.Fatal("mirror", e)
		}

		return s
	}

	start()
	consume(true)

	before := mirror()
	if len(before.Resources) < 25 {
		t.Fatal("incomplete independent mirror", len(before.Resources))
	}

	stop()
	start()
	consume(true)

	if mirror().Cursor != before.Cursor {
		t.Fatal("daemon restart lost cursor")
	}

	stop()
	// Publish a pending replacement then resolve its target through a repeated observation's new alias.
	// Both endpoints already exist; no new financial activity is created.
	store = open()

	providerHost, err = host.New(ctx, store, envelope, nil, func() time.Time { return now }, registration)
	if err != nil {
		t.Fatal(err)
	}

	workflow = application.New(store, providerHost, nil, func() time.Time { return now }, nil)

	id, replacementID := "", ""
	for _, execution := range dashboard.Executions {
		if execution.Terms().Fields().Event.Value() == "0000F001.00000001.01.01" {
			id = execution.ID()
		}

		if execution.Terms().Fields().Event.Value() == "0000F001.00000002.01.01" {
			replacementID = execution.ID()
		}
	}

	if id == "" || replacementID == "" {
		t.Fatal("fixture endpoints absent")
	}

	pattern := regexp.MustCompile(`<TradeConfirm [^>]*tradeID="900000002"[^>]*/>`)

	originalRow := pattern.FindString(string(report))
	if originalRow == "" {
		t.Fatal("replacement fixture row absent")
	}

	pendingRow := strings.Replace(originalRow, `origTradeID=""`, `origTradeID="late-original"`, 1)
	pendingRow = strings.Replace(pendingRow, `code="O;P"`, `code="Co"`, 1)
	pendingReport := strings.Replace(string(report), originalRow, pendingRow, 1)
	processReport := func(payload string) {
		t.Helper()
		reports.Store([]byte(payload))

		now = now.Add(24 * time.Hour)

		if e := workflow.Retry(ctx, connection); e != nil {
			t.Fatal(e)
		}

		advance()
		advance()

		if e := store.Schedule(
			ctx,
			host.WorkKey{Connection: connection, Stream: ibkrflex.Confirmations, Operation: "acquire"},
			false,
		); e != nil {
			t.Fatal(e)
		}
	}
	processReport(pendingReport)

	pending, err := store.Resource(ctx, "activity", replacementID)
	if err != nil || !pending.Activity.Conflict {
		t.Fatal("pending replacement did not raise conflict", err)
	}

	resolvedReport := strings.Replace(pendingReport, `tradeID="900000001"`, `tradeID="late-original"`, 1)
	processReport(resolvedReport)

	resolved, err := store.Resource(ctx, "activity", replacementID)
	if err != nil || resolved.Activity.Conflict {
		t.Fatal("resolved replacement did not clear conflict", err)
	}
	// Change authority twice while the consumer remains disconnected.
	for _, price := range []string{"401.00000000000100", "402.0000"} {
		form, e := workflow.CorrectionForm(ctx, "activity", id)
		if e != nil {
			t.Fatal(e)
		}

		decision, e := application.NewDecision(
			application.DecisionInput{
				TargetKind:       "activity",
				TargetID:         id,
				Scope:            "price",
				Text:             price,
				Currency:         "USD",
				Action:           "set",
				ExpectedRevision: form.Revision,
				Submission:       rand.Text(),
			},
		)
		if e != nil {
			t.Fatal(e)
		}

		if e = workflow.SaveDecision(ctx, decision, "synthetic/operator"); e != nil {
			t.Fatal(e)
		}
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	start()
	consume(false)

	after := mirror()
	prices := []string{}
	for _, entry := range after.History {
		if entry.Resource.ID == id && entry.Resource.Activity.Price.Amount != "400.123456789" {
			prices = append(prices, entry.Resource.Activity.Price.Amount)
		}
	}

	if len(prices) != 2 || prices[0] != "401.00000000000100" || prices[1] != "402.0000" {
		t.Fatal("disconnected transitions collapsed", prices)
	}

	consume(false)

	if len(mirror().History) != len(after.History) {
		t.Fatal("duplicate synchronization changed history")
	}

	stop()
	// Start rebuild, commit one bounded group, then resume in a new daemon process.
	store = open()

	job, e := store.StartRebuild(ctx, rand.Text(), "synthetic/operator", time.Now())
	if e != nil {
		t.Fatal(e)
	}

	if _, e = store.RunRecovery(ctx); e != nil {
		t.Fatal(e)
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	start()

	result := consume(false)
	if string(result["rebootstrap"]) != "true" {
		t.Fatal("generation invalidation not reported", job)
	}

	fresh := mirror()
	if fresh.Resources["activity/"+id].Activity.Price.Amount != "402.0000" || fresh.Cursor == after.Cursor {
		t.Fatal("rebootstrap lost accepted model")
	}

	stop()
	// Companion recreation keeps the warehouse generation and permits independent inspection.
	for _, path := range []string{profile.ConnectionsPath} {
		if err = os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	start()

	result = consume(false)
	if string(result["rebootstrap"]) != "false" || mirror().Cursor != fresh.Cursor {
		t.Fatal("companion recovery reset consumer")
	}

	// Follow stays visible while idle and exits cleanly on an ordinary interrupt.
	var followLogs socketTestLog
	//nolint:gosec // The task-built binary reads only the disposable socket and mirror.
	following := exec.CommandContext(
		ctx,
		binary,
		"--socket",
		socket,
		"--state",
		statePath,
		"--follow",
		"--interval",
		"30s",
	)

	following.Env = []string{"PATH=" + os.Getenv("PATH")}
	following.Stdout = io.Discard

	following.Stderr = &followLogs
	if err = following.Start(); err != nil {
		t.Fatal(err)
	}

	followingDone := make(chan error, 1)
	go func() { followingDone <- following.Wait() }()

	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(followLogs.String(), "Still synchronizing") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	_ = following.Process.Signal(os.Interrupt)
	select {
	case e := <-followingDone:
		if e != nil {
			t.Fatal("follow interrupt failed", e, followLogs.String())
		}
	case <-time.After(5 * time.Second):
		_ = following.Process.Kill()

		<-followingDone
		t.Fatal("follow did not stop")
	}

	if !strings.Contains(followLogs.String(), "Still synchronizing") {
		t.Fatal("follow lacks periodic progress")
	}

	stop()
	// Transport failures also exercise the standalone program's structured error and exit status.
	//nolint:gosec // The same task-built binary intentionally connects to the stopped disposable daemon.
	unavailable := exec.CommandContext(ctx, binary, "--socket", socket, "--state", statePath)
	unavailable.Env = []string{"PATH=" + os.Getenv("PATH")}

	var failureOut, failureProgress bytes.Buffer

	unavailable.Stdout = &failureOut
	unavailable.Stderr = &failureProgress
	if err = unavailable.Run(); err == nil || !strings.Contains(failureOut.String(), `"status":"failed"`) ||
		!strings.Contains(failureProgress.String(), "Socket consumer failed") {
		t.Fatal("standalone failure status", err, failureOut.String(), failureProgress.String())
	}
}
