// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/webauthn"
	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/clientaccess"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/internal/testbrowser"
	"go.trippwill.dev/wake/internal/transport/financialapi"
)

func TestBrowserPasskeyLifecycle(t *testing.T) {
	const (
		initialPassword  = "synthetic passkey setup password"
		browserPassword  = "synthetic password set by passkey"
		recoveryPassword = "synthetic passkey recovery password"
		settings         = "/system/settings"
		removeKey        = `[data-passkey-open="remove"]`
		logout           = `form[action="/logout"] button`
	)

	dir := privateAuthDir(t)
	if err := SetPassword(dir, []byte(initialPassword)); err != nil {
		t.Fatal(err)
	}

	var clockOffset atomic.Int64

	first, second, checkFinancialAPI := newPasskeyBrowserServers(t, dir, func() time.Time {
		return time.Now().Add(time.Duration(clockOffset.Load()))
	})
	checkFinancialAPI()

	ctx, cancel := context.WithTimeout(testbrowser.Start(t), 60*time.Second)
	defer cancel()

	check := func(step string, actions ...chromedp.Action[chromedp.Void]) {
		t.Helper()

		if err := chromedp.Do(ctx, actions...); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}
	passwordLogin := func(password string) []chromedp.Action[chromedp.Void] {
		return []chromedp.Action[chromedp.Void]{
			chromedp.WaitVisible(chromedp.CSS("#password")),
			chromedp.SendKeys(chromedp.CSS("#password"), password),
			testbrowser.NewDocument(chromedp.Click(chromedp.CSS(`form[action="/login"] button[type="submit"]`))),
			chromedp.WaitVisible(chromedp.CSS(logout)),
		}
	}
	passkeyLogin := testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#passkey-login")))
	beginEnrollment := func(label string) []chromedp.Action[chromedp.Void] {
		return []chromedp.Action[chromedp.Void]{
			chromedp.Click(chromedp.CSS("#passkey-add")),
			chromedp.SendKeys(chromedp.CSS("#passkey-label"), label),
			chromedp.SendKeys(chromedp.CSS("#passkey-current"), initialPassword),
			chromedp.Click(chromedp.CSS("#passkey-submit")),
			chromedp.WaitVisible(chromedp.CSS("#passkey-create")),
		}
	}

	check("open setup origin",
		testbrowser.SetViewport(1280, 900),
		testbrowser.Command(webauthn.Enable, webauthn.EnableParams{}),
		testbrowser.NewDocument(chromedp.Navigate(first+settings)),
	)

	primary := addVirtualPasskeyDevice(t, ctx)

	check("initial password sign-in", passwordLogin(initialPassword)...)
	clockOffset.Add(int64(attemptInterval))
	check("open Settings", testbrowser.NewDocument(chromedp.Navigate(first+settings)))
	check("authorize enrollment before cancelling", beginEnrollment("Discarded key")...)
	check(
		"abort a pending browser passkey prompt",
		testbrowser.Command(
			webauthn.SetAutomaticPresenceSimulation,
			webauthn.SetAutomaticPresenceSimulationParams{
				AuthenticatorID: primary,
				Enabled:         false,
			},
		),
		chromedp.Click(chromedp.CSS("#passkey-create")),
		chromedp.Poll[chromedp.Void](`document.querySelector('#browser-access-dialog [data-passkey-status]')
			.textContent.includes('Follow your browser’s prompt to save the passkey.')`),
		chromedp.Click(chromedp.CSS("#passkey-cancel")),
		chromedp.Poll[chromedp.Void](`document.querySelector('#browser-access-dialog [data-passkey-status]')
			.textContent.includes('The passkey prompt was canceled or timed out. Try again.')`),
		chromedp.Click(chromedp.CSS("#passkey-cancel")),
		chromedp.WaitNotPresent(chromedp.CSS("#browser-access-dialog[open]")),
		chromedp.WaitNotPresent(chromedp.CSS(removeKey)),
		testbrowser.Command(
			webauthn.SetAutomaticPresenceSimulation,
			webauthn.SetAutomaticPresenceSimulationParams{
				AuthenticatorID: primary,
				Enabled:         true,
			},
		),
	)
	clockOffset.Add(int64(attemptInterval))
	check("authorize primary key enrollment with the password", beginEnrollment("Desktop browser")...)
	check("enroll the primary key",
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#passkey-create"))),
		chromedp.Poll[chromedp.Void](`document.querySelector('main').textContent.includes('Desktop browser')`),
		chromedp.WaitVisible(chromedp.CSS(removeKey)),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(logout))),
	)
	check("use the primary key on a different localhost port",
		testbrowser.NewDocument(chromedp.Navigate(second+settings)),
		passkeyLogin,
		chromedp.WaitVisible(chromedp.CSS(logout)),
		testbrowser.NewDocument(chromedp.Navigate(second+settings)),
	)
	check("authorize backup key enrollment with the primary key",
		chromedp.Click(chromedp.CSS("#passkey-add")),
		chromedp.Poll[chromedp.Void](`document.querySelector('#passkey-method').value === 'passkey'`),
		chromedp.Click(chromedp.CSS("#passkey-submit")),
		chromedp.WaitVisible(chromedp.CSS("#passkey-create")),
		testbrowser.Command(webauthn.RemoveVirtualAuthenticator, webauthn.RemoveVirtualAuthenticatorParams{
			AuthenticatorID: primary,
		}),
	)
	addVirtualPasskeyDevice(t, ctx)
	check("enroll a separate backup device without a label",
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#passkey-create"))),
		chromedp.Poll[chromedp.Void](`document.querySelectorAll('[data-passkey-open="remove"]').length === 2`),
		chromedp.WaitVisible(chromedp.CSS(`[aria-label^="Rename Passkey ending in "]`)),
	)
	check("rename the backup key with a passkey",
		chromedp.Click(chromedp.CSS(`[data-passkey-open="rename"][data-passkey-label=""]`)),
		chromedp.SetValue(chromedp.CSS("#passkey-label"), "Backup security key"),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#passkey-submit"))),
		chromedp.WaitVisible(chromedp.CSS(`[aria-label="Remove Backup security key"]`)),
	)

	prior := passkeyBrowserCookie(t, ctx)

	check("disable password sign-in",
		chromedp.Click(chromedp.CSS("#password-disable")),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#passkey-submit"))),
		chromedp.WaitNotPresent(chromedp.CSS("#password-disable")),
		chromedp.WaitVisible(chromedp.CSS("#password-passkey")),
	)
	assertPasskeyBrowserSessionRejected(t, ctx, second, prior)
	checkFinancialAPI()
	check(
		"remove the primary key and protect the last sign-in method",
		chromedp.Click(chromedp.CSS(`[aria-label="Remove Desktop browser"]`)),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#passkey-submit"))),
		chromedp.WaitNotPresent(chromedp.CSS(removeKey)),
		chromedp.WaitVisible(
			chromedp.Search(
				`//li//*[text()='Set a password or add another passkey before removing this passkey.']`,
			),
		),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(logout))),
		chromedp.WaitNotPresent(chromedp.CSS("#password")),
		passkeyLogin,
		chromedp.WaitVisible(chromedp.CSS(logout)),
	)
	check("set a password using the backup passkey",
		testbrowser.NewDocument(chromedp.Navigate(second+settings)),
		chromedp.Click(chromedp.CSS("#password-passkey")),
		chromedp.SendKeys(chromedp.CSS("#passkey-new-password"), browserPassword),
		chromedp.SendKeys(chromedp.CSS("#passkey-confirm-password"), browserPassword),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#passkey-submit"))),
		chromedp.WaitVisible(chromedp.CSS("#password-disable")),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(logout))),
	)
	check("sign in with the password set by the backup passkey", passwordLogin(browserPassword)...)
	check("disable password sign-in before host recovery",
		testbrowser.NewDocument(chromedp.Navigate(second+settings)),
		chromedp.Click(chromedp.CSS("#password-disable")),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#passkey-submit"))),
		chromedp.WaitNotPresent(chromedp.CSS("#password-disable")),
	)

	// The host command invokes SetPassword against the same auth state while Wake is running.
	if err := SetPassword(dir, []byte(recoveryPassword)); err != nil {
		t.Fatal(err)
	}

	clockOffset.Add(int64(attemptInterval))
	check("host recovery invalidates the previous session",
		testbrowser.NewDocument(chromedp.Navigate(second+settings)),
		chromedp.WaitVisible(chromedp.CSS("#password")),
	)
	check("sign in with the recovered password", passwordLogin(recoveryPassword)...)
	check("verify live password recovery preserves the backup key",
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(logout))),
		passkeyLogin,
		chromedp.WaitVisible(chromedp.CSS(logout)),
	)
	checkFinancialAPI()
}

func passkeyBrowserCookie(t *testing.T, ctx context.Context) string {
	t.Helper()

	var value string

	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		result, err := cdp.Call(ctx, target, network.GetCookies, network.GetCookiesParams{})
		for _, cookie := range result.Cookies {
			if cookie.Name == sessionCookie {
				value = cookie.Value
				break
			}
		}

		return err
	}))
	if err != nil || value == "" {
		t.Fatalf("read the existing browser session: %v", err)
	}

	return value
}

func assertPasskeyBrowserSessionRejected(t *testing.T, ctx context.Context, origin, value string) {
	t.Helper()

	r, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/system/contact", nil)
	if err != nil {
		t.Fatal(err)
	}

	r.Header.Set("Cookie", sessionCookie+"="+value)

	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}

	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("previous browser session status = %d", res.StatusCode)
	}
}

func newPasskeyBrowserServers(t *testing.T, dir string, now func() time.Time) (string, string, func()) {
	t.Helper()

	primary, secondary := httptest.NewUnstartedServer(nil), httptest.NewUnstartedServer(nil)
	t.Cleanup(primary.Close)
	t.Cleanup(secondary.Close)

	origin := func(server *httptest.Server) string {
		t.Helper()

		_, port, err := net.SplitHostPort(server.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}

		return "http://" + net.JoinHostPort("localhost", port)
	}
	first, second := origin(primary), origin(secondary)

	auth, enabled, err := Open(dir, first, second)
	if err != nil || !enabled {
		t.Fatalf("open admin authentication: enabled=%t, error=%v", enabled, err)
	}

	auth.now = now

	clients := clientaccess.New(clientaccess.NewFileStore(dir))

	primary.Config.Handler, err = NewHandler(auth, Dependencies{Clients: clients})
	if err != nil {
		t.Fatal(err)
	}

	secondary.Config.Handler = primary.Config.Handler
	primary.Start()
	secondary.Start()

	store, err := persistence.Open(t.Context(), persistence.Profile{
		WarehousePath:   filepath.Join(dir, "warehouse.db"),
		ConnectionsPath: filepath.Join(dir, "access.db"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	api := httptest.NewServer(financialapi.Authenticated(clients, financialapi.NewHandler(store, nil, nil)))
	t.Cleanup(api.Close)

	_, secret, err := clients.Create(t.Context(), "Synthetic passkey test client")
	if err != nil {
		t.Fatal(err)
	}

	defer secret.Clear()

	token, err := secret.Copy()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { clear(token) })

	return first, second, func() {
		t.Helper()

		r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL+"/v1/snapshot", nil)
		if err != nil {
			t.Fatal(err)
		}

		r.Header.Set("Authorization", "Bearer "+string(token))

		res, err := api.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}

		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("existing financial client snapshot status = %d", res.StatusCode)
		}
	}
}

func addVirtualPasskeyDevice(t *testing.T, ctx context.Context) webauthn.AuthenticatorID {
	t.Helper()

	var id webauthn.AuthenticatorID

	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		result, err := cdp.Call(
			ctx,
			target,
			webauthn.AddVirtualAuthenticator,
			webauthn.AddVirtualAuthenticatorParams{
				Options: &webauthn.VirtualAuthenticatorOptions{
					Protocol:                    webauthn.AuthenticatorProtocolCtap2,
					Transport:                   webauthn.AuthenticatorTransportUsb,
					HasResidentKey:              true,
					HasUserVerification:         true,
					AutomaticPresenceSimulation: true,
					IsUserVerified:              true,
				},
			},
		)
		id = result.AuthenticatorID

		return err
	}))
	if err != nil {
		t.Fatal(err)
	}

	return id
}
