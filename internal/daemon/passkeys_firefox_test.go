// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build firefox

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestFirefoxPasskeyLifecycle(t *testing.T) {
	const (
		password = "synthetic Firefox passkey password"
		label    = "Firefox fixture"
		settings = "/system/settings"
		logout   = `body[data-signed-in] form[action="/logout"] button`
		enrolled = `[aria-label="Remove Firefox fixture"]`
	)

	cfg := shellConfig(t)
	origin := cfg.Admin.Origin()
	want := "http://" + net.JoinHostPort("wake.localhost", strconv.Itoa(int(cfg.Admin.Port)))
	if origin != want {
		t.Fatalf("default admin origin = %q, want %q", origin, want)
	}

	if err := admin.SetPassword(cfg.Paths.ConfigDir, []byte(password)); err != nil {
		t.Fatal(err)
	}

	daemonCtx, stop := context.WithCancel(t.Context())
	ready := make(chan struct{})
	done := make(chan struct{})

	var runErr error

	go func() {
		defer close(done)

		runErr = Run(daemonCtx, cfg, slog.Default(), func(phase string) {
			if phase == "ready" {
				close(ready)
			}
		})
	}()

	t.Cleanup(func() {
		stop()

		select {
		case <-done:
			if runErr != nil {
				t.Errorf("daemon stopped: %v", runErr)
			}
		case <-time.After(10 * time.Second):
			t.Error("daemon shutdown stalled")
		}
	})

	select {
	case <-ready:
	case <-done:
		t.Fatal("daemon stopped before startup")
	case <-time.After(10 * time.Second):
		t.Fatal("daemon startup stalled")
	}

	browser := startFirefox(t)
	browser.post("/url", firefoxObject{"url": origin + settings}, nil)
	browser.enter("#password", password)
	browser.click(`form[action="/login"] button[type="submit"]`)
	browser.visible(logout)

	browser.post("/url", firefoxObject{"url": origin + settings}, nil)
	browser.click("#passkey-add")
	browser.enter("#passkey-label", label)
	browser.enter("#passkey-current", password)

	// Password sign-in and enrollment share the daemon's one-second attempt limit.
	time.Sleep(time.Second)

	browser.click("#passkey-submit")
	browser.click("#passkey-create")
	browser.visible(enrolled)

	var rpID string

	browser.post("/execute/sync", firefoxObject{
		"script": `return document.querySelector(".wake-passkey-list .wake-help")?.textContent.trim();`,
		"args":   []string{},
	}, &rpID)

	if rpID != "wake.localhost" {
		t.Fatalf("enrolled passkey RP ID = %q, want wake.localhost", rpID)
	}

	browser.click(logout)
	browser.click("body:not([data-signed-in]) #passkey-login")
	browser.visible(logout)
	browser.post("/url", firefoxObject{"url": origin + settings}, nil)
	browser.visible(enrolled)
}

type firefoxObject map[string]any

type firefoxSession struct {
	t        *testing.T
	client   *http.Client
	endpoint string
}

func startFirefox(t *testing.T) firefoxSession {
	t.Helper()

	firefox, geckodriver := os.Getenv("WAKE_FIREFOX"), os.Getenv("WAKE_GECKODRIVER")
	if firefox == "" || geckodriver == "" {
		t.Fatal("set WAKE_FIREFOX (Firefox 152+) and WAKE_GECKODRIVER (geckodriver 0.37+)")
	}

	port := strconv.Itoa(int(freeTCPPort(t)))
	address := net.JoinHostPort("127.0.0.1", port)
	driverCtx, stopDriver := context.WithCancel(context.Background())
	t.Cleanup(stopDriver)

	// #nosec G204 G702 -- The test runner supplies the local WebDriver executable.
	cmd := exec.CommandContext(driverCtx, geckodriver,
		"--host", "127.0.0.1", "--port", port, "--profile-root", t.TempDir())
	cmd.WaitDelay = 5 * time.Second

	var logs bytes.Buffer

	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		stopDriver()

		_ = cmd.Wait()

		if t.Failed() {
			t.Logf("geckodriver output:\n%s", logs.String())
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp4", address, time.Second)
		if err == nil {
			_ = conn.Close()
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("geckodriver startup: %v", err)
		}

		time.Sleep(50 * time.Millisecond)
	}

	browser := firefoxSession{
		t:        t,
		client:   &http.Client{Transport: &http.Transport{}, Timeout: 20 * time.Second},
		endpoint: "http://" + address + "/session",
	}

	var session struct {
		ID string `json:"sessionId"`
	}

	browser.post("", firefoxObject{"capabilities": firefoxObject{"alwaysMatch": firefoxObject{
		"browserName": "firefox",
		"moz:firefoxOptions": firefoxObject{
			"binary": firefox,
			"args":   []string{"-headless"},
			// Select Firefox's virtual authenticator backend for this temporary profile.
			"prefs": firefoxObject{
				"security.webauth.webauthn_enable_softtoken": true,
				"security.webauth.webauthn_enable_usbtoken":  false,
			},
		},
	}}}, &session)

	if session.ID == "" {
		t.Fatal("geckodriver omitted the session ID")
	}

	browser.endpoint += "/" + url.PathEscape(session.ID)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := browser.request(ctx, http.MethodDelete, "", nil, nil); err != nil {
			t.Errorf("close Firefox session: %v", err)
		}

		browser.client.CloseIdleConnections()
	})

	browser.post("/window/rect", firefoxObject{"width": 1280, "height": 900}, nil)
	browser.post("/webauthn/authenticator", firefoxObject{
		"protocol":            "ctap2",
		"transport":           "usb",
		"hasResidentKey":      true,
		"hasUserVerification": true,
		"isUserConsenting":    true,
		"isUserVerified":      true,
	}, nil)

	return browser
}

func (s firefoxSession) request(ctx context.Context, method, path string, data, result any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, s.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()

	var reply struct {
		Value json.RawMessage `json:"value"`
	}

	if err := json.NewDecoder(res.Body).Decode(&reply); err != nil {
		return fmt.Errorf("HTTP %s: %w", res.Status, err)
	}

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s: %s", res.Status, reply.Value)
	}

	if result != nil {
		return json.Unmarshal(reply.Value, result)
	}

	return nil
}

func (s firefoxSession) post(path string, data, result any) {
	s.t.Helper()

	if err := s.request(s.t.Context(), http.MethodPost, path, data, result); err != nil {
		s.t.Fatalf("Firefox POST %s: %v", path, err)
	}
}

func (s firefoxSession) visible(selector string) {
	s.t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for {
		var status struct {
			Visible bool   `json:"visible"`
			Error   string `json:"error"`
			Address string `json:"address"`
		}

		s.post("/execute/sync", firefoxObject{
			"script": `
				return {
					visible: document.querySelector(arguments[0])?.checkVisibility() ?? false,
					error: document.querySelector(".wake-error[role=alert], [data-passkey-status].wake-error")?.textContent ?? "",
					address: location.href
				};
			`,
			"args": []string{selector},
		}, &status)

		if status.Visible {
			return
		}

		if status.Error != "" || time.Now().After(deadline) {
			s.t.Fatalf("Firefox waiting for %s at %s (Wake error %q)", selector, status.Address, status.Error)
		}

		time.Sleep(50 * time.Millisecond)
	}
}

func (s firefoxSession) element(selector string) string {
	s.t.Helper()
	s.visible(selector)

	var target struct {
		ID string `json:"element-6066-11e4-a52e-4f735466cecf"`
	}

	s.post("/element", firefoxObject{"using": "css selector", "value": selector}, &target)

	if target.ID == "" {
		s.t.Fatalf("Firefox omitted the element ID for %s", selector)
	}

	return "/element/" + url.PathEscape(target.ID)
}

func (s firefoxSession) click(selector string) {
	s.t.Helper()
	s.post(s.element(selector)+"/click", firefoxObject{}, nil)
}

func (s firefoxSession) enter(selector, text string) {
	s.t.Helper()
	s.post(s.element(selector)+"/value", firefoxObject{"text": text}, nil)
}
