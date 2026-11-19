// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"go.trippwill.dev/wake/internal/clientaccess"
)

func TestPasskeyHTTPBrowserAccessLifecycle(t *testing.T) {
	const (
		origin           = "http://localhost:18787"
		otherPort        = "http://localhost:28787"
		password         = "synthetic enrollment password"
		restoredPassword = "synthetic restored password"
	)

	dir := privateAuthDir(t)
	if err := SetPassword(dir, []byte(password)); err != nil {
		t.Fatal(err)
	}

	auth, enabled, err := Open(dir, origin, otherPort)
	if err != nil || !enabled {
		t.Fatal("password access unavailable", enabled, err)
	}

	handler, err := NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	auth.now = func() time.Time { return now }
	browser := passkeyHTTPBrowser{t: t, handler: handler, auth: auth, origin: origin}
	browser.signInWithPassword(password)

	now = now.Add(attemptInterval)

	primary := newPasskeyHTTPKey(t, "primary credential")
	add := beginPasskeyRequest{Operation: "add", Method: "password", Password: password, Label: "Desk key"}

	register := browser.begin(add, "create")
	if len(register.Options.User.ID) != clientaccess.BrowserUserHandleBytes ||
		register.Options.RP.ID != "localhost" ||
		register.Options.AuthenticatorSelection.UserVerification != protocol.VerificationRequired ||
		register.Options.AuthenticatorSelection.ResidentKey != protocol.ResidentKeyRequirementRequired {
		t.Fatalf("unexpected enrollment options: %+v", register.Options)
	}

	proof := primary.response(t, register, origin, nil)
	wrongPort := browser
	wrongPort.origin = otherPort
	wrongPort.finish(register.ID, proof, http.StatusConflict)
	readPasskeyReply(t, browser.finish(register.ID, proof, http.StatusOK), "done")
	browser.finish(register.ID, proof, http.StatusConflict)

	backup := newPasskeyHTTPKey(t, "backup credential")
	userHandle := register.Options.User.ID
	add = beginPasskeyRequest{Operation: "add", Method: "passkey", Label: "Backup key"}
	assertion := browser.begin(add, "get")

	register = readPasskeyReply(t, browser.finish(
		assertion.ID, primary.response(t, assertion, origin, userHandle), http.StatusOK,
	), "create")
	if !bytes.Equal(register.Options.User.ID, userHandle) || len(register.Options.Excluded) != 1 ||
		!bytes.Equal(register.Options.Excluded[0].CredentialID, primary.id) {
		t.Fatalf("second enrollment did not reuse the user or exclude the first key: %+v", register.Options)
	}

	readPasskeyReply(
		t,
		browser.finish(register.ID, backup.response(t, register, origin, nil), http.StatusOK),
		"done",
	)

	disable := beginPasskeyRequest{Operation: "disable", Method: "passkey"}
	assertion = browser.begin(disable, "get")
	rename := beginPasskeyRequest{
		Operation: "rename",
		Method:    "password",
		Password:  password,
		Target:    primary.id.String(),
		Label:     "  Travel key  ",
	}
	now = now.Add(attemptInterval)

	browser.begin(rename, "done")
	browser.finish(assertion.ID, backup.response(t, assertion, origin, userHandle), http.StatusConflict)

	settings := browser.request(http.MethodGet, "/system/settings", "", nil)
	if settings.Code != http.StatusOK {
		t.Fatalf("passkey settings returned %d", settings.Code)
	}

	for _, text := range []string{"Travel key", "Backup key", "Disable password sign-in"} {
		if !strings.Contains(settings.Body.String(), text) {
			t.Errorf("missing %q in passkey settings", text)
		}
	}

	assertion = browser.begin(disable, "get")
	previousSession := browser
	browser.finishWithSession(assertion.ID, backup.response(t, assertion, origin, userHandle))
	previousSession.expectAccess(http.StatusUnauthorized)
	browser.expectAccess(http.StatusOK)

	// Sign in on another configured port using the same hostname and registered key.
	browser.origin = otherPort
	browser.session, browser.csrf = nil, ""

	login := browser.request(http.MethodGet, "/login", "", nil)
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `id="passkey-login"`) ||
		strings.Contains(login.Body.String(), `name="password"`) {
		t.Fatalf("disabled password still offered or passkey login unavailable: %d %s", login.Code, login.Body)
	}

	signIn := beginPasskeyRequest{Operation: "login", Method: "passkey"}

	assertion = browser.begin(signIn, "get")
	if len(assertion.Options.Allowed) != 2 || assertion.Options.RPID != "localhost" ||
		assertion.Options.Verification != protocol.VerificationRequired {
		t.Fatalf("unexpected sign-in options: %+v", assertion.Options)
	}

	proof = backup.response(t, assertion, otherPort, userHandle)
	browser.finishWithSession(assertion.ID, proof)
	browser.expectAccess(http.StatusOK)
	browser.finish(assertion.ID, proof, http.StatusConflict)

	remove := beginPasskeyRequest{Operation: "remove", Method: "passkey", Target: primary.id.String()}
	assertion = browser.begin(remove, "get")
	previousSession = browser
	browser.finishWithSession(assertion.ID, backup.response(t, assertion, otherPort, userHandle))
	previousSession.expectAccess(http.StatusUnauthorized)
	browser.expectAccess(http.StatusOK)

	remove.Target = backup.id.String()
	browser.post("begin", remove, http.StatusBadRequest)

	setPassword := beginPasskeyRequest{
		Operation:   "set-password",
		Method:      "passkey",
		NewPassword: restoredPassword,
	}
	browser.post("begin", setPassword, http.StatusBadRequest)
	setPassword.Confirm = restoredPassword
	assertion = browser.begin(setPassword, "get")
	previousSession = browser
	browser.finishWithSession(assertion.ID, backup.response(t, assertion, otherPort, userHandle))
	previousSession.expectAccess(http.StatusUnauthorized)
	browser.expectAccess(http.StatusOK)

	access, err := auth.state.BrowserAccess()
	if err != nil || len(access.Passkeys) != 1 || !bytes.Equal(access.Passkeys[0].Credential.ID, backup.id) ||
		access.Passkeys[0].Credential.Authenticator.SignCount != backup.signCount {
		t.Fatal("remaining credential or its verified counter was lost", err)
	}

	remove.Method, remove.Password = "password", restoredPassword
	now = now.Add(attemptInterval)

	browser.begin(remove, "done")
	browser.expectAccess(http.StatusUnauthorized)

	now = now.Add(attemptInterval)

	browser.signInWithPassword(restoredPassword)
	browser.expectAccess(http.StatusOK)
}

type passkeyHTTPReply struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Options struct {
		Challenge              protocol.URLEncodedBase64
		RP                     protocol.RelyingPartyEntity
		User                   struct{ ID protocol.URLEncodedBase64 }
		AuthenticatorSelection protocol.AuthenticatorSelection
		RPID                   string                               `json:"rpId"`
		Verification           protocol.UserVerificationRequirement `json:"userVerification"`
		Allowed                []protocol.CredentialDescriptor      `json:"allowCredentials"`
		Excluded               []protocol.CredentialDescriptor      `json:"excludeCredentials"`
	} `json:"options"`
}

func readPasskeyReply(t *testing.T, w *httptest.ResponseRecorder, kind string) passkeyHTTPReply {
	t.Helper()

	var reply passkeyHTTPReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Kind != kind ||
		(kind != "done" && (reply.ID == "" || len(reply.Options.Challenge) == 0)) {
		t.Fatalf("expected %s response: %s (%v)", kind, w.Body, err)
	}

	return reply
}

type passkeyHTTPBrowser struct {
	t                *testing.T
	handler          http.Handler
	auth             *authenticator
	origin, csrf     string
	session, browser *http.Cookie
}

func (b passkeyHTTPBrowser) request(
	method, path, contentType string,
	data []byte,
) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(b.t.Context(), method, b.origin+path, bytes.NewReader(data))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Origin", b.origin)
	r.Header.Set("X-Wake-CSRF", b.csrf)

	if b.session != nil {
		r.AddCookie(b.session)
	}

	if b.browser != nil {
		r.AddCookie(b.browser)
	}

	w := httptest.NewRecorder()
	b.handler.ServeHTTP(w, r)

	return w
}

func (b *passkeyHTTPBrowser) begin(in beginPasskeyRequest, kind string) passkeyHTTPReply {
	b.t.Helper()

	w := b.post("begin", in, http.StatusOK)
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == ceremonyCookie {
			b.browser = cookie
		}
	}

	return readPasskeyReply(b.t, w, kind)
}

func (b passkeyHTTPBrowser) finish(
	id string,
	credential json.RawMessage,
	status int,
) *httptest.ResponseRecorder {
	b.t.Helper()

	return b.post("finish", finishPasskeyRequest{ID: id, Credential: credential}, status)
}

func (b *passkeyHTTPBrowser) finishWithSession(id string, credential json.RawMessage) {
	b.t.Helper()

	w := b.finish(id, credential, http.StatusOK)
	readPasskeyReply(b.t, w, "done")
	b.useSession(w)
}

func (b passkeyHTTPBrowser) post(route string, body any, status int) *httptest.ResponseRecorder {
	b.t.Helper()

	w := b.request(http.MethodPost, "/passkeys/"+route, "application/json", marshalPasskeyTestJSON(b.t, body))
	if w.Code != status || w.Header().Get("Content-Type") != "application/json" {
		b.t.Fatalf("passkey %s: status %d, want %d, body %s", route, w.Code, status, w.Body)
	}

	return w
}

func (b *passkeyHTTPBrowser) signInWithPassword(password string) {
	b.t.Helper()

	form := url.Values{"password": {password}}

	w := b.request(http.MethodPost, "/login", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if w.Code != http.StatusSeeOther {
		b.t.Fatalf("password sign-in failed: %d", w.Code)
	}

	b.useSession(w)
}

func (b *passkeyHTTPBrowser) useSession(w *httptest.ResponseRecorder) {
	b.t.Helper()

	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == sessionCookie {
			b.session = cookie
			r := httptest.NewRequestWithContext(b.t.Context(), http.MethodGet, b.origin, nil)
			r.AddCookie(cookie)

			var ok bool
			if b.csrf, ok = b.auth.session(r); !ok {
				b.t.Fatal("server issued an invalid session")
			}

			return
		}
	}

	b.t.Fatal("response omitted the session cookie")
}

func (b passkeyHTTPBrowser) expectAccess(status int) {
	b.t.Helper()

	w := b.request(http.MethodGet, "/system/contact", "", nil)
	if w.Code != status {
		b.t.Fatalf("protected route: status %d, want %d", w.Code, status)
	}
}

type passkeyHTTPKey struct {
	id        protocol.URLEncodedBase64
	public    ed25519.PublicKey
	private   ed25519.PrivateKey
	signCount uint32
}

func newPasskeyHTTPKey(t *testing.T, id string) *passkeyHTTPKey {
	t.Helper()

	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	return &passkeyHTTPKey{id: []byte(id), public: public, private: private}
}

func (k *passkeyHTTPKey) response(
	t *testing.T,
	prompt passkeyHTTPReply,
	origin string,
	userHandle []byte,
) json.RawMessage {
	t.Helper()

	creating := prompt.Kind == "create"
	rpID, ceremony := prompt.Options.RPID, protocol.AssertCeremony
	flags := protocol.FlagUserVerified | protocol.FlagUserPresent
	if creating {
		rpID, ceremony = prompt.Options.RP.ID, protocol.CreateCeremony
		flags |= protocol.FlagAttestedCredentialData
	} else {
		k.signCount++
	}

	clientData := marshalPasskeyTestJSON(t, protocol.CollectedClientData{
		Type: ceremony, Origin: origin, Challenge: prompt.Options.Challenge.String(),
	})
	hash := sha256.Sum256([]byte(rpID))
	authData := binary.BigEndian.AppendUint32(append(hash[:], byte(flags)), k.signCount)

	fields := map[string]protocol.URLEncodedBase64{"clientDataJSON": clientData}
	if creating {
		// A zero AAGUID and COSE OKP Ed25519 key describe an unattested synthetic authenticator.
		public, err := webauthncbor.Marshal(map[int]any{1: 1, 3: -8, -1: 6, -2: []byte(k.public)})
		if err != nil {
			t.Fatal(err)
		}

		authData = append(authData, make([]byte, 16)...)
		authData = binary.BigEndian.AppendUint16(
			authData,
			uint16(len(k.id)),
		) // #nosec G115 -- fixed synthetic IDs.
		authData = append(authData, k.id...)
		authData = append(authData, public...)

		attestation, err := webauthncbor.Marshal(map[string]any{
			"fmt": protocol.AttestationFormatNone, "attStmt": map[string]any{}, "authData": authData,
		})
		if err != nil {
			t.Fatal(err)
		}

		fields["attestationObject"] = attestation
	} else {
		clientHash := sha256.Sum256(clientData)
		fields["authenticatorData"] = authData
		fields["signature"] = ed25519.Sign(k.private, append(bytes.Clone(authData), clientHash[:]...))
		fields["userHandle"] = userHandle
	}

	return marshalPasskeyTestJSON(t, map[string]any{
		"type": "public-key", "id": k.id.String(), "rawId": k.id, "response": fields,
	})
}
