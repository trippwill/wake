// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"go.trippwill.dev/wake/internal/application"
)

type publication struct {
	BaselineID string `json:"baseline_id"`
	Version    string `json:"version"`
}

type publishedView struct {
	publication
	raw  json.RawMessage
	path string
}

type acceptanceState struct {
	history   publishedView
	positions publishedView
	followers map[string]*follower
}

func (s *acceptanceState) close() {
	for _, follow := range s.followers {
		follow.close()
	}
}

func readPositions(
	ctx context.Context,
	client *http.Client,
	origin, path string,
	headers http.Header,
) (publishedView, error) {
	view := publishedView{path: path}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+path, nil)
	if err != nil {
		return view, err
	}

	req.Header = headers.Clone()

	res, err := client.Do(req)
	if err != nil {
		return view, err
	}

	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return view, fmt.Errorf("positions probe: HTTP %d", res.StatusCode)
	}

	if err = json.NewDecoder(res.Body).Decode(&view.raw); err != nil {
		return view, err
	}

	err = json.Unmarshal(view.raw, &view.publication)

	return view, err
}

func acceptance(
	ctx context.Context, consumer, profiles, dir string, ready map[string]string, body io.Reader,
	client *http.Client, headers http.Header, stage string, suite *acceptanceState,
) error {
	history := publishedView{path: "/v1/snapshot"}
	if err := json.NewDecoder(body).Decode(&history.raw); err != nil {
		return err
	}

	var full application.FinancialSnapshot
	if err := json.Unmarshal(history.raw, &full); err != nil {
		return err
	}

	history.publication = publication{BaselineID: full.BaselineID, Version: full.Version}
	if stage != "initial" {
		switch stage {
		case "restart", "access-recovery":
			if suite.history.publication != history.publication {
				return errors.New("restart or access recovery changed publication")
			}
		case "financial-restore", "replacement", "empty-warehouse":
			if suite.history.BaselineID == history.BaselineID {
				return errors.New("replacement retained obsolete baseline")
			}
		default:
			if suite.history.BaselineID != history.BaselineID || suite.history.Version == history.Version {
				return errors.New("fixture update did not advance publication")
			}
		}
	}

	target, err := url.Parse(ready["endpoint"])
	if err != nil {
		return err
	}

	for _, profile := range strings.Split(profiles, ",") {
		view, previous := history, &suite.history
		account, faultStage, followStage := "", "acquisition", "restart"
		switch profile {
		case "history":
		case "positions":
			for _, r := range full.Resources {
				if r.Kind == "accounts" {
					account = r.ID
					break
				}
			}

			if account == "" {
				return errors.New("positions fixture has no account")
			}

			view, err = readPositions(
				ctx,
				client,
				target.String(),
				"/v1/accounts/"+url.PathEscape(account)+"/positions",
				headers,
			)
			if err != nil {
				return err
			}

			previous, faultStage, followStage = &suite.positions, "replacement", "financial-restore"
		default:
			return errors.New("unknown profile")
		}

		stateDir := filepath.Join(dir, profile)
		// #nosec G703 -- The profile name is allowlisted and the workspace is locked.
		if err = os.Mkdir(stateDir, 0o700); err != nil && (stage == "initial" || !errors.Is(err, os.ErrExist)) {
			return err
		}

		adapter := adapterClient{
			consumer:  consumer,
			stateDir:  stateDir,
			profile:   profile,
			account:   account,
			tokenFile: ready["token_file"],
		}
		if stage == faultStage {
			if previous.publication == view.publication {
				return fmt.Errorf("%s fault target did not change publication", profile)
			}

			if err = checkInterrupted(
				ctx,
				adapter,
				target,
				client.Transport,
				*previous,
				view.path,
				false,
			); err != nil {
				return err
			}

			if profile == "history" {
				if err = checkInterrupted(
					ctx,
					adapter,
					target,
					client.Transport,
					*previous,
					view.path,
					true,
				); err != nil {
					return err
				}
			}
		}

		if err = adapter.run(ctx, "sync", target.String(), view, false); err != nil {
			return err
		}

		if err = adapter.run(ctx, "export", "", view, false); err != nil {
			return err
		}

		switch stage {
		case followStage:
			adapter.stateDir = filepath.Join(dir, profile+"-follower")
			// #nosec G703 -- The profile name is allowlisted and the workspace is locked.
			if err = os.Mkdir(adapter.stateDir, 0o700); err != nil {
				return err
			}

			suite.followers[profile], err = startFollower(ctx, adapter, target, client.Transport, view)
			if err != nil {
				return err
			}
		case faultStage:
			follow := suite.followers[profile]
			if err = follow.reconnect(ctx, target, view); err != nil {
				return err
			}

			delete(suite.followers, profile)
		}

		*previous = view
	}

	return nil
}

func checkInterrupted(
	ctx context.Context,
	adapter adapterClient,
	target *url.URL,
	transport http.RoundTripper,
	previous publishedView,
	path string,
	forceFull bool,
) error {
	var observed atomic.Bool

	reverse := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(proxy *httputil.ProxyRequest) {
			proxy.SetURL(target)

			if forceFull && proxy.Out.URL.Path == path {
				proxy.Out.URL.RawQuery = ""
			}
		},
		ModifyResponse: func(r *http.Response) error {
			if r.Request.Method == http.MethodGet && r.Request.URL.Path == path && r.StatusCode == http.StatusOK {
				observed.Store(true)

				r.Body = &truncated{Reader: io.LimitReader(r.Body, 1), Closer: r.Body}
				r.ContentLength = 4096
				r.Header.Set("Content-Length", "4096")
			}

			return nil
		},
	}
	proxy := httptest.NewServer(reverse)
	defer proxy.Close()

	if err := adapter.run(ctx, "sync", proxy.URL, publishedView{}, true); err != nil {
		return err
	}

	if !observed.Load() {
		return fmt.Errorf("adapter did not read interrupted %s view", adapter.profile)
	}

	return adapter.run(ctx, "export", "", previous, false)
}

type adapterClient struct {
	consumer, stateDir, profile, account, tokenFile string
}

type adapterEvent struct {
	Protocol int
	Event    string
	Phase    string
	publication
	View json.RawMessage
}

func (a adapterClient) start(ctx context.Context, op, endpoint string) (*supervisedProcess, error) {
	req := map[string]any{"protocol": 1, "operation": op, "profile": a.profile, "state_dir": a.stateDir}
	if op != "export" {
		req["endpoint"], req["token_file"] = endpoint, a.tokenFile
	}

	if a.profile == "positions" {
		req["account_id"] = a.account
	}

	input, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	// #nosec G204 -- The caller selects an adapter executable, invoked without a shell.
	cmd := exec.Command(a.consumer)
	cmd.Dir = a.stateDir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stderr = os.Stderr

	return startProcess(ctx, cmd, 0)
}

func (a adapterClient) run(ctx context.Context, op, endpoint string, want publishedView, fault bool) error {
	proc, err := a.start(ctx, op, endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = proc.stop() }()

	err = readAdapter(proc.stdout, op, fault, func(event adapterEvent) error {
		if event.Event == "committed" && event.publication != want.publication {
			return errors.New("adapter reported incorrect committed publication")
		}

		if event.Event == "complete" && op == "export" {
			equal := jsonExportsEqual
			if a.profile == "history" {
				equal = historyExportsEqual
			}

			if !equal(event.View, want.raw) {
				return fmt.Errorf("inaccurate %s export", a.profile)
			}
		}

		return nil
	})

	if e := ctx.Err(); e != nil {
		return e
	}

	if err != nil {
		return err
	}

	err = proc.wait()

	if e := ctx.Err(); e != nil {
		return e
	}

	if fault {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() <= 0 {
			return errors.New("interrupted synchronization did not exit with a failure")
		}

		return nil
	}

	return err
}

func readAdapter(stdout io.Reader, op string, fault bool, receive func(adapterEvent) error) error {
	const maxOutput = 16 << 20

	input := &io.LimitedReader{R: stdout, N: maxOutput + 1}

	dec := json.NewDecoder(input)
	done := false
	for {
		var event adapterEvent

		err := dec.Decode(&event)
		if err == io.EOF {
			break
		}

		if err != nil || event.Protocol != 1 || done {
			return errors.New("invalid adapter output")
		}

		switch event.Event {
		case "progress":
			switch event.Phase {
			case "connecting", "retrying", "following", "idle":
			default:
				return errors.New("invalid progress phase")
			}
		case "committed":
			if event.BaselineID == "" || event.Version == "" || op == "export" || fault {
				return errors.New("invalid commit notification")
			}
		case "failed", "complete":
			if (event.Event == "failed") != fault {
				return fmt.Errorf("unexpected adapter terminal event %q", event.Event)
			}

			done = true
		default:
			return errors.New("unknown adapter event")
		}

		if err = receive(event); err != nil {
			return err
		}
	}

	if input.N == 0 {
		return errors.New("invalid adapter output size")
	}

	if !done {
		return errors.New("missing adapter completion")
	}

	return nil
}

type truncated struct {
	io.Reader
	io.Closer
}
