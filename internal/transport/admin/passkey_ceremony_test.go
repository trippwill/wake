// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/webauthn"
	"go.trippwill.dev/wake/internal/clientaccess"
)

func TestPasskeyRelyingPartyEligibility(t *testing.T) {
	for origin, want := range map[string]string{
		"http://localhost:18787": "localhost", "http://localhost:28787": "localhost",
		"http://wake.localhost:8787": "wake.localhost",
		"https://wake.internal":      "wake.internal", "http://wake.internal": "", "http://127.0.0.1:8787": "",
		"https://[::1]": "", "https://co.uk": "",
	} {
		rp := passkeyParty(origin)
		if rp == nil {
			if want != "" {
				t.Errorf("rejected %q", origin)
			}

			continue
		}

		if want == "" || rp.Config.RPID != want || len(rp.Config.RPOrigins) != 1 ||
			rp.Config.RPOrigins[0] != origin {
			t.Errorf("unexpected relying party for %q: %q, %v", origin, rp.Config.RPID, rp.Config.RPOrigins)
		}
	}
}

func TestPasskeyChallengeBindingAndConsumption(t *testing.T) {
	var pending pendingCeremonies

	now := time.Now()
	c := &ceremony{
		origin:  "http://localhost:18787",
		browser: "browser A",
		session: "session A",
		change:  passkeyChange{operation: "disable"},
	}

	id, err := pending.put(c, now)
	if err != nil {
		t.Fatal(err)
	}

	for _, pair := range [][2]string{{"http://localhost:28787", c.browser}, {c.origin, "browser B"}} {
		if _, err := pending.take(id, pair[0], pair[1], now); !errors.Is(err, errCeremonyExpired) {
			t.Fatalf("unexpected challenge admission: %v", err)
		}
	}

	got, err := pending.take(id, c.origin, c.browser, now)
	if err != nil || got != c {
		t.Fatal("lost bound operation", err)
	}

	if _, err := pending.take(id, c.origin, c.browser, now); !errors.Is(err, errCeremonyExpired) {
		t.Fatalf("accepted replay: %v", err)
	}

	id, err = pending.put(c, now)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pending.take(
		id,
		c.origin,
		c.browser,
		now.Add(ceremonyLifetime),
	); !errors.Is(
		err,
		errCeremonyExpired,
	) {
		t.Fatalf("accepted expired challenge: %v", err)
	}
}

func TestPasskeyAssertionChecksSignedContextAndVerification(t *testing.T) {
	const origin = "http://localhost:18787"

	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	key, err := webauthncbor.Marshal(map[int]any{1: 1, 3: -8, -1: 6, -2: []byte(public)})
	if err != nil {
		t.Fatal(err)
	}

	id := []byte("synthetic credential")
	c := ceremony{
		origin: origin,
		access: clientaccess.BrowserAccess{
			UserHandle: []byte("Wake installation A"),
			Passkeys: []clientaccess.Passkey{
				{RPID: "localhost", Credential: webauthn.Credential{ID: id, PublicKey: key}},
				{RPID: "other.test", Credential: webauthn.Credential{ID: []byte("unrelated key"), PublicKey: key}},
			},
		},
	}
	rp := passkeyParty(origin)

	_, err = c.prepare(rp, false)
	if err != nil {
		t.Fatal(err)
	}

	if len(c.data.AllowedCredentialIDs) != 1 {
		t.Fatalf("offered credentials from another RP: %d", len(c.data.AllowedCredentialIDs))
	}

	verified := protocol.FlagUserPresent | protocol.FlagUserVerified
	for _, tc := range []struct {
		name, origin, rpID, handle string
		flags                      protocol.AuthenticatorFlags
		previousCounter            uint32
		want                       bool
	}{
		{"synced counter drift", origin, "localhost", "Wake installation A", verified | protocol.FlagBackupEligible | protocol.FlagBackupState, 2, true},
		{"device counter drift", origin, "localhost", "Wake installation A", verified, 2, false},
		{"valid", origin, "localhost", "Wake installation A", verified, 0, true},
		{"user presence only", origin, "localhost", "Wake installation A", protocol.FlagUserPresent, 0, false},
		{"other port", "http://localhost:28787", "localhost", "Wake installation A", verified, 0, false},
		{"other RP", origin, "other.test", "Wake installation A", verified, 0, false},
		{"other installation", origin, "localhost", "Wake installation B", verified, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientData := marshalPasskeyTestJSON(
				t,
				map[string]string{"origin": tc.origin, "type": "webauthn.get", "challenge": c.data.Challenge},
			)
			hash := sha256.Sum256([]byte(tc.rpID))
			c.user.credentials[0].Authenticator.SignCount = tc.previousCounter
			c.user.credentials[0].Flags = webauthn.NewCredentialFlags(tc.flags &^ protocol.FlagBackupState)

			authData := append(hash[:], byte(tc.flags), 0, 0, 0, 1)
			clientHash := sha256.Sum256(clientData)
			signed := append(append([]byte{}, authData...), clientHash[:]...)
			encode := base64.RawURLEncoding.EncodeToString

			assertion := marshalPasskeyTestJSON(t, map[string]any{
				"type":  "public-key",
				"id":    encode(id),
				"rawId": encode(id),
				"response": map[string]string{
					"clientDataJSON":    encode(clientData),
					"authenticatorData": encode(authData),
					"userHandle":        encode([]byte(tc.handle)),
					"signature":         encode(ed25519.Sign(private, signed)),
				},
			})
			credential, err := c.verify(rp, assertion)
			if (err == nil) != tc.want {
				t.Fatalf("verification returned %v", err)
			}

			if err == nil &&
				(credential.Authenticator.SignCount < tc.previousCounter || credential.Flags.BackupState != tc.flags.HasBackupState()) {
				t.Fatal("lost counter or backup state")
			}
		})
	}
}

func marshalPasskeyTestJSON(t *testing.T, value any) []byte {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestPasskeyManagementPreflight(t *testing.T) {
	const origin = "http://localhost:18787"

	a, _, err := Open(privateAuthDir(t), origin)
	if err != nil {
		t.Fatal(err)
	}

	current := clientaccess.Passkey{RPID: "localhost", Credential: webauthn.Credential{ID: []byte("current")}}
	stale := clientaccess.Passkey{RPID: "old.test", Credential: webauthn.Credential{ID: []byte("old")}}

	c := ceremony{
		origin: origin,
		access: clientaccess.BrowserAccess{Passkeys: []clientaccess.Passkey{current, stale}},
	}
	view := a.accessView(httptest.NewRequest(http.MethodGet, origin, nil), c.access)
	if view.Passkeys[0].CanRemove || !view.Passkeys[1].CanRemove {
		t.Fatal("incorrect removal choices for retired RP")
	}

	in := beginPasskeyRequest{
		Operation: "remove",
		Method:    "passkey",
		Target:    base64.RawURLEncoding.EncodeToString(current.Credential.ID),
	}
	if err := a.validateChange(in, &c); !errors.Is(err, clientaccess.ErrLastBrowserMethod) {
		t.Fatalf("last usable key: %v", err)
	}

	in = beginPasskeyRequest{
		Operation: "add",
		Method:    "passkey",
		Label:     strings.Repeat("x", clientaccess.MaxPasskeyLabelBytes+1),
	}
	if err := a.validateChange(in, &c); !errors.Is(err, clientaccess.ErrInvalidPasskey) {
		t.Fatalf("label preflight: %v", err)
	}

	c.access.Passkeys = make([]clientaccess.Passkey, clientaccess.MaxPasskeys)
	if err := a.validateChange(in, &c); !errors.Is(err, clientaccess.ErrPasskeyLimit) {
		t.Fatalf("enrollment limit: %v", err)
	}
}

func TestEnrollmentChallengeRequiresInitiatingSession(t *testing.T) {
	const (
		origin   = "http://localhost:18787"
		password = "synthetic initial password"
	)

	dir := privateAuthDir(t)
	if err := SetPassword(dir, []byte(password)); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, origin)
	if err != nil {
		t.Fatal(err)
	}

	handler, err := NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	session := httptest.NewRecorder()
	csrf := issueTestSession(t, auth, session)

	post := func(path string, data any, token string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, origin+path, bytes.NewReader(marshalPasskeyTestJSON(t, data)))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Wake-CSRF", token)

		for _, c := range cookies {
			r.AddCookie(c)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		return w
	}
	begin := beginPasskeyRequest{Operation: "add", Method: "password", Password: password}
	cookie := session.Result().Cookies()[0]
	if w := post("/passkeys/begin", begin, "wrong", cookie); w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF rejection: %d", w.Code)
	}

	w := post("/passkeys/begin", begin, csrf, cookie)

	var prompt passkeyReply
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &prompt) != nil || prompt.Kind != "create" ||
		prompt.ID == "" {
		t.Fatalf("unexpected enrollment response: %d %s", w.Code, w.Body)
	}

	flow := w.Result().Cookies()[0]
	second := httptest.NewRecorder()
	otherCSRF := issueTestSession(t, auth, second)
	finish := finishPasskeyRequest{ID: prompt.ID, Credential: json.RawMessage("{}")}

	w = post("/passkeys/finish", finish, otherCSRF, second.Result().Cookies()[0], flow)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("other session: %d %s", w.Code, w.Body)
	}

	w = post("/passkeys/finish", finish, csrf, cookie, flow)
	if w.Code != http.StatusConflict {
		t.Fatalf("consumed challenge: %d %s", w.Code, w.Body)
	}
}

func TestPasskeyHTTPAdmission(t *testing.T) {
	const (
		origin     = "http://localhost:18787"
		other      = "http://localhost:28787"
		ineligible = "http://127.0.0.1:8787"
		login      = `{"operation":"login","method":"passkey"}`
	)

	dir := privateAuthDir(t)

	auth, _, err := Open(dir, origin, other, ineligible)
	if err != nil {
		t.Fatal(err)
	}

	handler, err := NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name, destination, body string
		origins                 []string
		mediaType               string
		status                  int
		message                 Error
	}{
		{"no Origin", origin, login, nil, "application/json", http.StatusForbidden, errPasskeyCSRF},
		{"other port", origin, login, []string{other}, "application/json", http.StatusForbidden, errPasskeyCSRF},
		{"duplicate Origin", origin, login, []string{origin, origin}, "application/json", http.StatusForbidden, errPasskeyCSRF},
		{"foreign media type", origin, login, []string{origin}, "text/plain", http.StatusBadRequest, errBadPasskeyRequest},
		{"second JSON value", origin, login + "{}", []string{origin}, "application/json", http.StatusBadRequest, errBadPasskeyRequest},
		{"no enrolled key", origin, login, []string{origin}, "application/json", http.StatusForbidden, errNoPasskey},
		{"ineligible origin", ineligible, login, []string{ineligible}, "application/json", http.StatusForbidden, errNoPasskey},
		{"no management session", origin, `{"operation":"disable","method":"passkey"}`, []string{origin}, "application/json", http.StatusUnauthorized, errPasskeySession},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tt.destination+"/passkeys/begin", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", tt.mediaType)

			for _, origin := range tt.origins {
				r.Header.Add("Origin", origin)
			}

			assertPasskeyAdmissionError(t, handler, r, tt.status, tt.message)
		})
	}

	if err := os.WriteFile(
		filepath.Join(dir, clientaccess.FileName),
		[]byte("broken state"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodPost, origin+"/passkeys/begin", strings.NewReader(login))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	assertPasskeyAdmissionError(
		t, handler, r, http.StatusServiceUnavailable, "Browser access is temporarily unavailable.",
	)
}

func assertPasskeyAdmissionError(
	t *testing.T,
	handler http.Handler,
	r *http.Request,
	status int,
	message Error,
) {
	t.Helper()

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	var result struct {
		Error string `json:"error"`
	}
	if w.Code != status || w.Header().Get("Content-Type") != "application/json" ||
		json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Error != message.Error() {
		t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body)
	}

	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == sessionCookie {
			t.Fatal("rejected passkey request issued a browser session")
		}
	}
}
