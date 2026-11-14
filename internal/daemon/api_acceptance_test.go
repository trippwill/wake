// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"reflect"
	"regexp"
	"strconv"
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
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

type apiTestLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *apiTestLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.Write(p)
}

func (b *apiTestLog) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }
func (b *apiTestLog) Reset()         { b.mu.Lock(); defer b.mu.Unlock(); b.buffer.Reset() }

// This test executable runs the real daemon in its own process with controlled providers.
func TestAPIDaemonProcess(t *testing.T) {
	raw := os.Getenv("WAKE_API_DAEMON_CONFIG")
	if raw == "" {
		return
	}

	var cfg config.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}

	cert, err := base64.StdEncoding.DecodeString(os.Getenv("WAKE_API_DAEMON_CERT"))
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

	seed, err := time.Parse(time.RFC3339Nano, os.Getenv("WAKE_API_DAEMON_NOW"))
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	clockNow := func() time.Time { return seed.Add(time.Since(started)) }
	endpoint := os.Getenv("WAKE_API_DAEMON_UPSTREAM")
	if err = run(
		ctx,
		cfg,
		slog.New(slog.NewJSONHandler(os.Stderr, nil)),
		sourceOptions{
			IBKR:            ibkrflex.Options{HTTPClient: client, BaseURL: endpoint},
			Now:             clockNow,
			ReferenceClient: client,
			ReferenceURL:    endpoint + "/mapping",
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestAPIConsumerAcceptance(t *testing.T) {
	binary := os.Getenv("WAKE_API_CONSUMER")
	if binary == "" {
		t.Skip("mise run test:api supplies the independent acceptance binary")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	report, err := os.ReadFile("../../testdata/ibkrflex/confirmations/initial.xml")
	if err != nil {
		t.Fatal(err)
	}

	realizedReport, err := os.ReadFile("../../testdata/ibkrflex/activity/realized.xml")
	if err != nil {
		t.Fatal(err)
	}

	var reports atomic.Value
	reports.Store(report)

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SendRequest":
			if r.URL.Query().Get("q") == "456" {
				_, _ = fmt.Fprint(
					w,
					`<FlexStatementResponse><Status>Success</Status><ReferenceCode>api-result-fixture</ReferenceCode></FlexStatementResponse>`,
				)

				return
			}

			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Success</Status><ReferenceCode>api-fixture</ReferenceCode></FlexStatementResponse>`,
			)
		case "/GetStatement":
			if r.URL.Query().Get("q") == "api-result-fixture" {
				_, _ = w.Write(realizedReport)
				return
			}

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
	cfg.Credentials.KeyPath = filepath.Join(cfg.Paths.ConfigDir, "master.key")
	if err = credential.CreateMasterKey(cfg.Credentials.KeyPath); err != nil {
		t.Fatal(err)
	}

	key, err := credential.LoadMasterKey(cfg.Credentials.KeyPath)
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

	providerHost, err := host.New(ctx, store, envelope, nil, func() time.Time { return now }, nil, registration)
	if err != nil {
		t.Fatal(err)
	}

	workflow := application.New(store, providerHost, nil, func() time.Time { return now }, nil)

	secret := credential.NewSecret([]byte("api-synthetic-token"))
	defer secret.Clear()

	connection, err := workflow.CreateConnection(
		ctx,
		"API acceptance",
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

	query, _ := application.NewListQuery(application.ListOptions{}, application.SourceScope)

	sourceDetail, err := workflow.SourceDetail(ctx, connection, query)
	dashboard := struct {
		Connections []application.ConnectionView
		Executions  []application.Execution
		Lifecycles  []application.Lifecycle
		Conversions []application.Conversion
	}{Connections: []application.ConnectionView{sourceDetail.Connection}}
	if err != nil || len(dashboard.Connections[0].Candidates) != 1 {
		t.Fatal("discovery", err)
	}

	candidate := dashboard.Connections[0].Candidates[0]

	details, err := application.NewAccountDetails("API acceptance account", "")
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

	activityQuery, _ := application.NewListQuery(application.ListOptions{}, application.ActivityScope)

	activities, err := workflow.ActivityList(ctx, activityQuery)
	for _, item := range activities.Items {
		switch item.Resource.Activity.Family {
		case "trade":
			detail, e := workflow.ExecutionDetail(ctx, item.Resource.ID)
			if e != nil {
				t.Fatal(e)
			}

			dashboard.Executions = append(dashboard.Executions, detail.Execution)
		case "lifecycle":
			detail, e := workflow.LifecycleDetail(ctx, item.Resource.ID)
			if e != nil {
				t.Fatal(e)
			}

			dashboard.Lifecycles = append(dashboard.Lifecycles, detail.Lifecycle)
		case "conversion":
			detail, e := workflow.ConversionDetail(ctx, item.Resource.ID)
			if e != nil {
				t.Fatal(e)
			}

			dashboard.Conversions = append(dashboard.Conversions, detail.Conversion)
		}
	}

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

	// Keep reference work quiescent for the restart-only identity assertion.
	instrumentQuery, _ := application.NewListQuery(application.ListOptions{}, application.InstrumentScope)

	instruments, e := store.InstrumentList(ctx, instrumentQuery)
	if e != nil {
		t.Fatal(e)
	}

	for _, item := range instruments.Items {
		if e = store.ApplyReference(
			ctx,
			item.Resource.ID,
			reference.Result{Status: "no-match", Next: now.Add(365 * 24 * time.Hour)},
		); e != nil {
			t.Fatal(e)
		}
	}

	if err = store.Schedule(
		ctx,
		host.WorkKey{Connection: connection, Stream: ibkrflex.Confirmations, Operation: "acquire"},
		false,
	); err != nil {
		t.Fatal(err)
	}

	realizedSettings, _ := provider.NewSettings(
		map[string]string{"query_id": "123", "activity_query_id": "456"},
	)
	if err = workflow.UpdateConnection(ctx, connection, "API acceptance", realizedSettings); err != nil {
		t.Fatal(err)
	}

	advance()
	advance()

	if err = store.Schedule(
		ctx,
		host.WorkKey{Connection: connection, Stream: ibkrflex.Activity, Operation: "acquire"},
		false,
	); err != nil {
		t.Fatal(err)
	}

	providerHost.Close()

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	tokenFile := seedAPIClient(t, cfg)
	endpoint := "http://" + cfg.API.Listen
	apiTransport := financialTransport(t, cfg)
	apiClient := &http.Client{Transport: apiTransport, Timeout: time.Second}

	var (
		daemon     *exec.Cmd
		daemonDone chan error
		logs       apiTestLog
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

			t.Fatal("daemon did not join", <-daemonDone)
		}

		daemon = nil

		apiTransport.CloseIdleConnections()
	}
	defer stop()

	start := func(status ...int) {
		t.Helper()
		logs.Reset()

		data, e := json.Marshal(cfg)
		if e != nil {
			t.Fatal(e)
		}

		//nolint:gosec // Re-execute this test binary as the controlled daemon process.
		daemon = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAPIDaemonProcess$")

		daemon.Env = append(
			os.Environ(),
			"WAKE_API_DAEMON_CONFIG="+string(data),
			"WAKE_API_DAEMON_NOW="+now.UTC().Format(time.RFC3339Nano),
			"WAKE_API_DAEMON_UPSTREAM="+upstream.URL,
			"WAKE_API_DAEMON_CERT="+base64.StdEncoding.EncodeToString(upstream.Certificate().Raw),
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
			response, e := apiClient.Get("http://wake/v1/snapshot")
			if e == nil {
				_ = response.Body.Close()

				expected := http.StatusOK
				if len(status) > 0 {
					expected = status[0]
				}

				if response.StatusCode == expected {
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

		t.Fatalf("financial API did not become ready; daemon logs:\n%s", logs.String())
	}
	statePath := filepath.Join(t.TempDir(), "mirror.db")

	var eventOutput *apiTestLog

	consume := func(assert bool) map[string]json.RawMessage {
		t.Helper()

		args := []string{"--endpoint", endpoint, "--token-file", tokenFile, "--state", statePath}
		if assert {
			args = append(args, "--assert", "../../testdata/api-consumer/fixtures/confirmations.json")
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

		if eventOutput != nil {
			// The separate consumer backs off for up to 30 seconds while the daemon
			// is deliberately offline. Allow one bounded reconnect after restart.
			deadline := time.Now().Add(35 * time.Second)
			for {
				// The follower may legitimately skip the one-shot consumer's version.
				lines := strings.TrimSpace(eventOutput.String())

				var installed struct {
					Version string `json:"version"`
				}

				_ = json.Unmarshal([]byte(lines[strings.LastIndex(lines, "\n")+1:]), &installed)

				request, e := http.NewRequestWithContext(ctx, http.MethodGet, "http://wake/v1/snapshot", nil)
				if e != nil {
					t.Fatal(e)
				}

				request.Header.Set("If-None-Match", `"`+installed.Version+`"`)

				response, e := apiClient.Do(request)
				if e == nil {
					_ = response.Body.Close()
					if response.StatusCode == http.StatusNotModified {
						break
					}
				}

				if time.Now().After(deadline) {
					t.Fatal("SSE follower did not converge", eventOutput.String(), e)
				}

				time.Sleep(10 * time.Millisecond)
			}
		}

		return result
	}
	mirror := func() struct {
		WarehouseID string `json:"warehouse_id"`
		Version     string `json:"version"`
		Resources   map[string]application.CanonicalResource
	} {
		t.Helper()

		var current struct {
			WarehouseID string `json:"warehouse_id"`
			Version     string `json:"version"`
			Resources   map[string]application.CanonicalResource
		}

		db, e := sql.Open("sqlite", "file:"+statePath+"?mode=ro&_pragma=busy_timeout(5000)")
		if e != nil {
			t.Fatal(e)
		}

		defer func() { _ = db.Close() }()

		var data []byte
		if e = db.QueryRowContext(ctx, "SELECT payload FROM mirror WHERE singleton=1").
			Scan(&data); e != nil ||
			json.Unmarshal(data, &current) != nil {
			t.Fatal("mirror", e)
		}

		return current
	}

	start()
	consume(true)

	// A separate positions-only client receives no activity snapshot, private store paths,
	// provider protocol or credentials. Its first output follows the initial SSE hint.
	accountID := ""
	for _, resource := range mirror().Resources {
		if resource.Kind == "accounts" {
			accountID = resource.ID
		}
	}

	var positionLogs, positionOutput apiTestLog
	//nolint:gosec // Task-built independent consumer receives only the disposable API and public account ID.
	positionsClient := exec.CommandContext(
		ctx,
		binary,
		"--endpoint",
		endpoint, "--token-file", tokenFile,
		"--positions-account",
		accountID,
		"--follow",
	)

	positionsClient.Env = []string{"PATH=" + os.Getenv("PATH")}
	positionsClient.Stdout = &positionOutput

	positionsClient.Stderr = &positionLogs
	if e = positionsClient.Start(); e != nil {
		t.Fatal(e)
	}

	positionsDone := make(chan error, 1)
	go func() { positionsDone <- positionsClient.Wait() }()

	positionDeadline := time.Now().Add(10 * time.Second)
	for strings.TrimSpace(positionOutput.String()) == "" && time.Now().Before(positionDeadline) {
		time.Sleep(10 * time.Millisecond)
	}

	_ = positionsClient.Process.Signal(os.Interrupt)
	select {
	case e = <-positionsDone:
		if e != nil {
			t.Fatal(e, positionLogs.String())
		}
	case <-time.After(5 * time.Second):
		_ = positionsClient.Process.Kill()

		t.Fatal("positions client failed to stop")
	}

	var positions struct {
		Status string `json:"status"`
		AsOf   struct {
			Date string  `json:"date"`
			At   *string `json:"at"`
		} `json:"as_of"`
		Positions []struct {
			Quantity struct {
				Value string `json:"value"`
			} `json:"quantity"`
			UnitCostBasis struct {
				Amount string `json:"amount"`
			} `json:"unit_cost_basis"`
		} `json:"positions"`
	}
	if e = json.Unmarshal(
		[]byte(strings.TrimSpace(positionOutput.String())),
		&positions,
	); e != nil || positions.Status != "available" || positions.AsOf.Date != "2026-07-17" || positions.AsOf.At != nil || len(positions.Positions) != 1 || positions.Positions[0].Quantity.Value != "-2.00000000000000100" ||
		positions.Positions[0].UnitCostBasis.Amount != "0.0000" {
		t.Fatal("positions-only consumer", positionOutput.String(), positionLogs.String(), e)
	}

	// Follow corrections and restarts. Re-enrollment restarts the client with its new token.
	var (
		eventLogs     apiTestLog
		eventFollower *exec.Cmd
		eventDone     chan error
	)

	eventState := filepath.Join(t.TempDir(), "events.db")
	startFollower := func() {
		t.Helper()
		eventLogs.Reset()

		eventOutput = &apiTestLog{}
		//nolint:gosec // Task-built independent consumer receives only a disposable API and its own mirror.
		eventFollower = exec.CommandContext(
			ctx,
			binary,
			"--endpoint",
			endpoint, "--token-file", tokenFile,
			"--state",
			eventState,
			"--follow",
			"--interval",
			"20ms",
		)

		eventFollower.Env = []string{"PATH=" + os.Getenv("PATH")}
		eventFollower.Stdout = eventOutput

		eventFollower.Stderr = &eventLogs
		if err = eventFollower.Start(); err != nil {
			t.Fatal(err)
		}

		eventDone = make(chan error, 1)
		go func() { eventDone <- eventFollower.Wait() }()
	}
	stopFollower := func() {
		if eventFollower == nil {
			return
		}

		_ = eventFollower.Process.Signal(os.Interrupt)
		select {
		case e := <-eventDone:
			if e != nil {
				t.Error("SSE follow failed", e, eventLogs.String())
			}
		case <-time.After(5 * time.Second):
			_ = eventFollower.Process.Kill()

			t.Error("SSE follower did not stop", <-eventDone)
		}

		eventFollower = nil
	}

	startFollower()

	defer stopFollower()

	deadlineEvents := time.Now().Add(5 * time.Second)
	for !strings.Contains(eventLogs.String(), "Following accepted publication events") {
		if time.Now().After(deadlineEvents) {
			t.Fatal("SSE follower did not connect", eventLogs.String())
		}

		time.Sleep(10 * time.Millisecond)
	}

	before := mirror()
	if len(before.Resources) < 25 {
		t.Fatal("incomplete independent mirror", len(before.Resources))
	}

	movements := map[string]bool{}
	for _, r := range before.Resources {
		if r.Activity == nil {
			continue
		}

		if r.Activity.Transfer != nil {
			if r.Activity.Transfer.Direction != "out" || r.Activity.Quantity().Value != "2.00000000001" {
				t.Fatal("API transfer terms", r)
			}

			movements["security-transfer"] = true
		}

		if r.Activity.Cash != nil {
			if r.Activity.Cash.Amount.Amount != "7.89000000001" || r.Activity.Cash.Amount.Currency != "USD" {
				t.Fatal("API cash terms", r)
			}

			movements["cash"] = true
		}
	}

	if len(movements) != 2 {
		t.Fatal("independent API mirror omitted movements", movements)
	}

	nestedCharges := false
	for _, resource := range before.Resources {
		if resource.Activity == nil {
			continue
		}

		for _, charge := range resource.Activity.Charges() {
			if charge.Category != "commission" {
				continue
			}

			if charge.Total == nil || len(charge.Components) == 0 {
				t.Fatal("commission total or components missing from independent mirror", charge)
			}

			for _, component := range charge.Components {
				if component.Role == "thirdPartyRegulatoryCommission" && len(component.Components) == 3 {
					nestedCharges = true
				}
			}
		}
	}

	if !nestedCharges {
		t.Fatal("independent mirror omitted nested regulatory charge breakdown")
	}

	debtCount := 0
	for _, r := range before.Resources {
		if r.Activity == nil || r.Activity.Quantity() == nil || r.Activity.Quantity().Unit != "nominal" {
			continue
		}

		debtCount++

		if r.Activity.Trade.QuotationBasis != nil || r.Activity.Trade == nil || r.Activity.Trade.Price == nil ||
			r.Activity.Trade.AccruedInterest == nil {
			t.Fatal("independent mirror changed debt monetary roles or asserted a quotation basis", r)
		}
	}

	if debtCount < 2 {
		t.Fatal("independent mirror omitted debt samples", debtCount)
	}

	// Interrupt a real daemon snapshot in a separate TCP transport.
	cutAddress := "127.0.0.1:0"

	cutListener, e := net.Listen("tcp", cutAddress)
	if e != nil {
		t.Fatal(e)
	}

	cutServer := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			response, e := apiClient.Get("http://wake/v1/snapshot")
			if e != nil {
				http.Error(w, "upstream unavailable", http.StatusBadGateway)
				return
			}
			defer func() { _ = response.Body.Close() }()

			data, e := io.ReadAll(response.Body)
			if e != nil {
				http.Error(w, "upstream interrupted", http.StatusBadGateway)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", response.Header.Get("ETag"))
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data[:len(data)/2])
		}),
	}
	cutDone := make(chan error, 1)
	go func() { cutDone <- cutServer.Serve(cutListener) }()
	//nolint:gosec // The independent client reads the disposable interrupted API and its own mirror.
	interrupted := exec.CommandContext(
		ctx,
		binary,
		"--endpoint",
		"http://"+cutListener.Addr().String(),
		"--token-file",
		tokenFile,
		"--state",
		statePath,
	)
	interrupted.Env = []string{"PATH=" + os.Getenv("PATH")}

	var interruptedOutput bytes.Buffer

	interrupted.Stdout = &interruptedOutput
	interrupted.Stderr = &interruptedOutput
	interruptedError := interrupted.Run()
	_ = cutServer.Close()

	if e := <-cutDone; e != nil && !errors.Is(e, http.ErrServerClosed) {
		t.Fatal("interrupted test server shutdown", e)
	}

	if interruptedError == nil || !strings.Contains(interruptedOutput.String(), "interrupted response") ||
		mirror().Version != before.Version {
		t.Fatal("interrupted API download replaced baseline", interruptedError, interruptedOutput.String())
	}

	stop()
	start()
	consume(true)

	if mirror().Version != before.Version {
		t.Fatal("daemon restart changed snapshot version")
	}

	// Acquire another source event while an independent process repeatedly installs snapshots.
	stop()

	store = open()
	acquireKey := host.WorkKey{Connection: connection, Stream: ibkrflex.Confirmations, Operation: "acquire"}
	if err = store.Schedule(ctx, acquireKey, true); err != nil {
		t.Fatal(err)
	}

	if err = store.Retry(ctx, acquireKey); err != nil {
		t.Fatal(err)
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	concurrentRow := regexp.MustCompile(`<TradeConfirm [^>]*tradeID="900000001"[^>]*/>`).
		FindString(string(report))
	if concurrentRow == "" {
		t.Fatal("concurrent source fixture missing")
	}

	concurrentRow = strings.Replace(concurrentRow, `conid="99001001"`, `conid="99009999"`, 1)
	concurrentRow = strings.Replace(concurrentRow, `tradeID="900000001"`, `tradeID="concurrent-trade"`, 1)
	concurrentRow = strings.Replace(
		concurrentRow,
		`execID="0000F001.00000001.01.01"`,
		`execID="concurrent-fill"`,
		1,
	)
	reports.Store(
		[]byte(strings.Replace(string(report), "</TradeConfirms>", concurrentRow+"</TradeConfirms>", 1)),
	)
	start()
	//nolint:gosec // Separate test client has no Wake store paths or upstream credentials.
	concurrentConsumer := exec.CommandContext(
		ctx,
		binary,
		"--endpoint",
		endpoint, "--token-file", tokenFile,
		"--state",
		statePath,
		"--follow",
		"--interval",
		"20ms",
	)
	concurrentConsumer.Env = []string{"PATH=" + os.Getenv("PATH")}
	concurrentConsumer.Stdout = io.Discard

	var concurrentLogs apiTestLog

	concurrentConsumer.Stderr = &concurrentLogs
	if err = concurrentConsumer.Start(); err != nil {
		t.Fatal(err)
	}

	concurrentDone := make(chan error, 1)
	go func() { concurrentDone <- concurrentConsumer.Wait() }()

	deadlineConcurrent := time.Now().Add(45 * time.Second)
	installed := false
	for time.Now().Before(deadlineConcurrent) {
		count := 0
		for _, r := range mirror().Resources {
			if r.Kind == "activity" {
				count++
			}
		}

		if count == 20 {
			installed = true
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	_ = concurrentConsumer.Process.Signal(os.Interrupt)
	select {
	case e := <-concurrentDone:
		if e != nil {
			t.Fatal("concurrent snapshot client", e, concurrentLogs.String())
		}
	case <-time.After(5 * time.Second):
		_ = concurrentConsumer.Process.Kill()

		t.Fatal("concurrent snapshot client did not stop", <-concurrentDone)
	}

	if !installed {
		t.Fatal("concurrent accepted update was not installed", concurrentLogs.String(), logs.String())
	}

	stop()
	// Publish a pending replacement then resolve its target through a repeated observation's new alias.
	// Both endpoints already exist; no new financial activity is created.
	store = open()

	providerHost, err = host.New(ctx, store, envelope, nil, func() time.Time { return now }, nil, registration)
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

	// Converge a second qualified binding for the same equity while the client is
	// disconnected. Its previous mirror includes both roots and venue resources.
	original, err := store.Resource(ctx, "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	survivor := *original.Activity.InstrumentID()

	roots, err := store.InstrumentList(ctx, instrumentQuery)
	if err != nil {
		t.Fatal(err)
	}

	retired, retiredListing := "", ""
	for _, item := range roots.Items {
		detail, e := store.InstrumentDetail(ctx, item.Resource.ID)
		if e != nil {
			t.Fatal(e)
		}

		if detail.Instrument.Reported().Reference().Value() == "99009999" {
			retired = item.Resource.ID
			if len(detail.Listings) > 0 {
				retiredListing = detail.Listings[0].ID()
			}
		}
	}

	if retired == "" || retiredListing == "" {
		t.Fatal("second binding/listing absent")
	}

	identity := reference.Result{
		Status: "resolved",
		Records: []reference.Record{
			{
				FIGI:         "BBG000BPHFS9",
				Composite:    "BBG000BPH459",
				ShareClass:   "BBG001S5TD05",
				Market:       "Equity",
				SecurityType: "Common Stock",
			},
		},
		Next: now.Add(365 * 24 * time.Hour),
	}
	for _, root := range []string{survivor, retired} {
		if err = store.ApplyReference(ctx, root, identity); err != nil {
			t.Fatal(err)
		}
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	start()
	consume(false)

	after := mirror()
	if _, exists := after.Resources["instruments/"+retired]; exists {
		t.Fatal("mirror kept retired root")
	}

	if _, exists := after.Resources["listings/"+retiredListing]; exists {
		t.Fatal("mirror kept retired listing")
	}

	for _, resource := range after.Resources {
		if resource.Activity != nil && resource.Activity.InstrumentID() != nil &&
			*resource.Activity.InstrumentID() == retired {
			t.Fatal("mirror contains old root link")
		}
	}

	directClient := *apiClient
	directClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	for _, alias := range []struct{ kind, id, survivor string }{
		{"instruments", retired, survivor},
		{"listings", retiredListing, ""},
	} {
		oldPath := "/v1/" + alias.kind + "/" + alias.id

		response, e := directClient.Get("http://wake" + oldPath)
		if e != nil {
			t.Fatal(e)
		}

		_ = response.Body.Close()
		location := response.Header.Get("Location")
		current, ok := after.Resources[strings.TrimPrefix(location, "/v1/")]
		if response.StatusCode != http.StatusPermanentRedirect ||
			!strings.HasPrefix(location, "/v1/"+alias.kind+"/") || !ok ||
			current.Kind != alias.kind || alias.survivor != "" && current.ID != alias.survivor {
			t.Fatal(
				"retired resource did not redirect to a current mirror resource",
				oldPath,
				response.StatusCode,
				location,
			)
		}

		response, e = apiClient.Get("http://wake" + oldPath)
		if e != nil {
			t.Fatal(e)
		}

		var actual application.CanonicalResource

		e = json.NewDecoder(response.Body).Decode(&actual)
		_ = response.Body.Close()
		if e != nil || response.StatusCode != http.StatusOK ||
			response.Request.URL.Path != location || !reflect.DeepEqual(actual, current) {
			t.Fatal("redirected lookup differed from the independent mirror", oldPath, response.StatusCode, e)
		}
	}

	for _, tc := range []struct {
		path   string
		status int
		code   string
	}{
		{"/v1/instruments/" + retired + "/explanation?revision=1", http.StatusConflict, "stale_revision"},
		{"/v1/listings/" + retiredListing + "/explanation?revision=1", http.StatusConflict, "stale_revision"},
		{"/v1/instruments/nonexistent", http.StatusNotFound, "not_found"},
	} {
		response, e := directClient.Get("http://wake" + tc.path)
		if e != nil {
			t.Fatal(e)
		}

		var body struct{ Code, Message string }

		e = json.NewDecoder(response.Body).Decode(&body)
		_ = response.Body.Close()
		if e != nil || response.StatusCode != tc.status || body.Code != tc.code ||
			body.Message == "" || response.Header.Get("Location") != "" {
			t.Fatal("unexpected resource failure", tc.path, response.StatusCode, body.Code, e)
		}
	}

	if after.Resources["activity/"+id].Activity.Trade.Price.Amount != "402.0000" ||
		after.Version == before.Version {
		t.Fatal("snapshot lost latest disconnected correction", after.Version)
	}

	consume(false)

	if mirror().Version != after.Version {
		t.Fatal("unchanged synchronization advanced version")
	}

	stopFollower()
	stop()
	// Recreating access keeps the warehouse identity and accepted publication intact.
	if err = os.Remove(profile.ConnectionsPath); err != nil {
		t.Fatal(err)
	}

	start()
	startFollower()

	result := consume(false)
	if string(result["new_baseline"]) != "false" || mirror().Version != after.Version {
		t.Fatal("access recovery replaced financial baseline")
	}

	stop()
	// A different warehouse installs a complete, explicitly reported new baseline.
	if err = os.Remove(profile.WarehousePath); err != nil {
		t.Fatal(err)
	}

	start()

	result = consume(false)
	fresh := mirror()
	if string(result["new_baseline"]) != "true" || fresh.WarehouseID == after.WarehouseID ||
		fresh.Version == after.Version ||
		len(fresh.Resources) != 0 {
		t.Fatal("new warehouse did not atomically replace baseline", fresh)
	}

	// Follow stays visible while idle and exits cleanly on an ordinary interrupt.
	var followLogs apiTestLog
	//nolint:gosec // The task-built binary reads only the disposable API and mirror.
	following := exec.CommandContext(
		ctx,
		binary,
		"--endpoint",
		endpoint, "--token-file", tokenFile,
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

		t.Fatal("follow did not stop", <-followingDone)
	}

	if !strings.Contains(followLogs.String(), "Still synchronizing") {
		t.Fatal("follow lacks periodic progress")
	}

	stop()
	// Transport failures also exercise the standalone program's structured error and exit status.
	//nolint:gosec // The same task-built binary intentionally connects to the stopped disposable daemon.
	unavailable := exec.CommandContext(
		ctx,
		binary,
		"--endpoint",
		endpoint,
		"--token-file",
		tokenFile,
		"--state",
		statePath,
	)
	unavailable.Env = []string{"PATH=" + os.Getenv("PATH")}

	var failureOut, failureProgress bytes.Buffer

	unavailable.Stdout = &failureOut
	unavailable.Stderr = &failureProgress
	if err = unavailable.Run(); err == nil || !strings.Contains(failureOut.String(), `"status":"failed"`) ||
		!strings.Contains(failureProgress.String(), "API consumer failed") {
		t.Fatal("standalone failure status", err, failureOut.String(), failureProgress.String())
	}
}
