// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package daemon owns process resources and joined shutdown.
package daemon

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/logging"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/internal/transport/admin"
	socketapi "go.trippwill.dev/wake/internal/transport/socket"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

type shell struct{ status admin.Status }

func (s shell) Status(context.Context) (admin.Status, error) { return s.status, nil }

func Run(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	progress ...func(string),
) (result error) {
	return run(ctx, cfg, logger, sourceOptions{}, progress...)
}

// Source endpoints are composed here, never read from broker settings. Tests use
// controlled TLS servers through the same daemon/host/store/admin composition.
type sourceOptions struct {
	IBKR            ibkrflex.Options
	ReferenceClient *http.Client
	ReferenceURL    string
	Now             func() time.Time
}

func run(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	options sourceOptions,
	progress ...func(string),
) (result error) {
	logger, flushLogs := logging.Coalesce(logger)
	defer flushLogs()

	stage := "starting"
	started := false
	defer func() {
		if result != nil {
			logger.ErrorContext(
				context.Background(),
				"daemon failed",
				"event",
				"daemon_failed",
				"stage",
				stage,
				"code",
				logging.Code(result),
			)
		}

		if started {
			logger.InfoContext(
				context.Background(),
				"daemon stopped",
				"event",
				"daemon_stopped",
				"outcome",
				"joined",
			)
		}
	}()

	phase := func(value string) {
		stage = value
		for _, report := range progress {
			report(value)
		}
	}

	if ctx.Err() != nil {
		return nil
	}

	phase("claiming private process paths")

	owner, err := acquireSocketOwnership(cfg.Paths.RuntimeDir)
	if err != nil {
		return err
	}
	defer owner.close()

	socket := filepath.Join(cfg.Paths.RuntimeDir, "wake.sock")
	if err = prepareSocket(socket); err != nil {
		return err
	}

	phase("opening private storage")

	store, err := persistence.Open(
		ctx,
		persistence.Profile{
			WarehousePath:   cfg.Storage.WarehousePath,
			ConnectionsPath: cfg.Storage.ConnectionsPath,
		},
		logger,
	)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, store.Close()) }()

	phase("checking credential protection")

	protected := false

	var envelope *credential.Envelope
	if cfg.Credentials.MasterKeyPath != "" {
		key, e := credential.LoadMasterKey(cfg.Credentials.MasterKeyPath)
		if e != nil {
			return e
		}
		defer key.Clear()

		envelope, e = credential.NewEnvelope(key)
		if e != nil {
			return e
		}
		defer envelope.Clear()

		protected = true
	}

	registration, err := ibkrflex.Registration(options.IBKR)
	if err != nil {
		return err
	}

	providerHost, err := host.New(ctx, store, envelope, logger, options.Now, registration)
	if err != nil {
		return err
	}
	defer providerHost.Close()

	referenceService, err := reference.New(
		store,
		envelope,
		options.ReferenceClient,
		options.ReferenceURL,
		options.Now, logger,
	)
	if err != nil {
		return err
	}

	workflow := application.New(store, providerHost, referenceService, options.Now, logger)

	workContext, cancelWork := context.WithCancel(ctx)

	var background sync.WaitGroup
	background.Go(func() {
		workflow.Run(workContext, nil)
	})

	defer func() { cancelWork(); background.Wait() }()

	requestsContext, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	requests := &requestGroup{}
	stopping := make(chan struct{})

	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}

	defer func() { _ = listener.Close(); _ = os.Remove(socket) }()

	if err = os.Chmod(socket, 0o600); err != nil {
		return err
	}

	server := newHTTPServer(requests.handler(socketapi.NewHandler(store, logger)), logger)
	server.BaseContext = func(net.Listener) context.Context { return requestsContext }
	servers := []*http.Server{server}
	listeners := []net.Listener{
		sameUIDListener{Listener: listener, logger: logging.Component(logger, "socket")},
	}
	logger.DebugContext(ctx, "private socket listening", "event", "socket_listening")

	auth, enabled, err := admin.Open(cfg.Paths.ConfigDir, cfg.Admin.AuthMode, cfg.Admin.Origin())
	if err != nil {
		return err
	}

	var (
		adminBind string
		adminPort string
	)
	if enabled {
		defer auth.Close()

		handler, e := admin.NewHandler(
			auth,
			admin.Dependencies{
				Queries: shell{
					admin.Status{CredentialProtection: protected, StartedAt: time.Now().UTC().Format(time.RFC3339)},
				},
				Config:   cfg,
				Workflow: workflow,
				Stopping: stopping,
				Logger:   logger,
			},
		)
		if e != nil {
			return e
		}

		adminListener, e := net.Listen("tcp", cfg.Admin.Address())
		if e != nil {
			bind, port, _ := net.SplitHostPort(cfg.Admin.Address())
			logger.ErrorContext(
				ctx,
				"admin listener failed",
				"event",
				"admin_bind_failed",
				"code",
				"listen_failed",
				"admin_bind",
				bind,
				"admin_port",
				port,
			)

			return e
		}

		adminBind, adminPort, _ = net.SplitHostPort(adminListener.Addr().String())

		defer func() { _ = adminListener.Close() }()

		adminServer := newHTTPServer(requests.handler(handler), logger)
		adminServer.BaseContext = server.BaseContext
		servers = append(servers, adminServer)
		listeners = append(listeners, adminListener)
	}

	var serving sync.WaitGroup

	failures := make(chan error, len(servers))
	for i, s := range servers {
		serving.Add(1)
		go func(s *http.Server, l net.Listener) {
			defer serving.Done()

			if e := s.Serve(l); !errors.Is(e, http.ErrServerClosed) {
				failures <- e
			}
		}(s, listeners[i])
	}

	started = true

	logger.InfoContext(
		ctx,
		"daemon ready",
		"event",
		"daemon_started",
		"admin_enabled",
		enabled,
		"admin_bind",
		adminBind,
		"admin_port",
		adminPort,
	)
	phase("ready")

	stage = "serving"

	select {
	case <-ctx.Done():
	case err = <-failures:
		result = err

		logger.ErrorContext(ctx, "HTTP server failed", "event", "server_failed", "code", "serve_failed")
	}

	phase("joining shutdown")
	close(stopping)
	shutdownHTTPServers(servers...)
	cancelRequests()
	requests.stop()
	serving.Wait()

	return result
}

func shutdownHTTPServers(servers ...*http.Server) {
	//nolint:gosec // Shutdown has an independent bounded lifetime after process cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Go(func() {
			if err := s.Shutdown(ctx); err != nil {
				_ = s.Close()
			}
		})
	}

	wg.Wait()
}

func newHTTPServer(handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           boundedReads(handler),
		ErrorLog:          log.New(serverDiagnostics{logging.Component(logger, "http")}, "", 0),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
}

// net/http diagnostics can contain panic values, addresses and request details.
type serverDiagnostics struct{ logger *slog.Logger }

func (w serverDiagnostics) Write(value []byte) (int, error) {
	w.logger.Error("HTTP server diagnostic", "event", "http_server_failure", "code", "http_transport_failed")
	return len(value), nil
}
