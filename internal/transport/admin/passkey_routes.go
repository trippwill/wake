// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"slices"

	"github.com/go-webauthn/webauthn/webauthn"
	"go.trippwill.dev/wake/internal/clientaccess"
)

// #nosec G101 -- These strings are operator-facing response messages.
const (
	errBadPasskeyRequest Error = "Check the requested change and try again."
	errCeremonyExpired   Error = "This request expired or belongs to another browser. Start again."
	errPasskeyRejected   Error = "Wake could not verify this passkey."
	errNoPasskey         Error = "Use a configured passkey address or run wake access set password on the Wake host."
	errPasskeySession    Error = "Sign in again before changing browser access."
	errPasskeyCSRF       Error = "Reload this page before changing browser access."
	errBusyPasskey       Error = "Wait a moment and try again."
	errPasswordCheck     Error = "Current password is incorrect."
	errPasswordConfirm   Error = "The passwords do not match."
)

type passkeyReply struct {
	ID      string `json:"id,omitempty"`
	Kind    string `json:"kind"`
	Options any    `json:"options,omitempty"`
}

type beginPasskeyRequest struct {
	Operation   string `json:"operation"`
	Method      string `json:"method"`
	Password    string `json:"password"`
	Label       string `json:"label"`
	Target      string `json:"target"`
	NewPassword string `json:"newPassword"`
	Confirm     string `json:"confirm"`
}
type finishPasskeyRequest struct {
	ID         string          `json:"id"`
	Credential json.RawMessage `json:"credential"`
}

func registerPasskeyRoutes(mux *http.ServeMux, a *authenticator) {
	handle := func(handler func(http.ResponseWriter, *http.Request) (passkeyReply, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			if !a.sameOrigin(r) {
				writePasskeyError(w, errPasskeyCSRF)
				return
			}

			contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || contentType != "application/json" {
				writePasskeyError(w, errBadPasskeyRequest)
				return
			}

			r.Body = http.MaxBytesReader(w, r.Body, 128<<10)

			result, err := handler(w, r)
			if err != nil {
				writePasskeyError(w, err)
				return
			}

			_ = json.NewEncoder(w).Encode(result)
		}
	}
	mux.HandleFunc("POST /passkeys/begin", handle(a.beginPasskey))
	mux.HandleFunc("POST /passkeys/finish", handle(a.finishPasskey))
}

func writePasskeyError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch err {
	case errCeremonyExpired, clientaccess.ErrBrowserChanged:
		status = http.StatusConflict
	case errPasskeyCSRF, errPasskeyRejected, errNoPasskey, errPasswordCheck:
		status = http.StatusForbidden
	case errPasskeySession:
		status = http.StatusUnauthorized
	case errBusyPasskey:
		status = http.StatusTooManyRequests
	case errBadPasskeyRequest, errPasswordConfirm, ErrWeakPassword, ErrLongPassword, ErrInvalidPassword,
		clientaccess.ErrPasskeyLimit, clientaccess.ErrLastBrowserMethod, clientaccess.ErrInvalidPasskey:
	default:
		status = http.StatusServiceUnavailable
		err = Error("Browser access is temporarily unavailable.")
	}

	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func readPasskeyJSON(r *http.Request, target any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()

	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return errBadPasskeyRequest
	}

	return nil
}

func (a *authenticator) managementSession(
	r *http.Request,
	access clientaccess.BrowserAccess,
) (string, error) {
	id, ok := a.sessionWithKey(r, access.Key[:])
	if !ok || access.Key == [32]byte{} {
		return "", errPasskeySession
	}

	values := r.Header.Values("X-Wake-CSRF")
	if len(values) != 1 || subtle.ConstantTimeCompare([]byte(id), []byte(values[0])) != 1 {
		return "", errPasskeyCSRF
	}

	return id, nil
}

func (a *authenticator) beginPasskey(w http.ResponseWriter, r *http.Request) (passkeyReply, error) {
	var input beginPasskeyRequest
	if err := readPasskeyJSON(r, &input); err != nil {
		return passkeyReply{}, err
	}

	access, err := a.state.BrowserAccess()
	if err != nil {
		return passkeyReply{}, err
	}

	origin := r.Header.Get("Origin")
	c := &ceremony{
		origin: origin,
		access: access,
		change: passkeyChange{operation: input.Operation, target: input.Target},
	}
	if input.Operation != "login" {
		c.session, err = a.managementSession(r, access)
		if err != nil {
			return passkeyReply{}, err
		}
	}

	if err := a.validateChange(input, c); err != nil {
		return passkeyReply{}, err
	}

	if input.Method == "password" && input.Operation != "add" {
		return a.commitPasskey(w, r, c, nil)
	}
	// Cookies are shared across ports. The independently checked Origin binds each
	// outstanding ceremony to its initiating port as well as its browser.
	cookie, err := r.Cookie(ceremonyCookie)
	if err == nil && cookie.Value != "" && len(cookie.Value) <= 128 {
		c.browser = cookie.Value
	} else {
		c.browser = rand.Text()
	}

	// #nosec G124 -- Use Secure for HTTPS and allow the configured HTTP localhost tunnel.
	http.SetCookie(w, &http.Cookie{
		Name:     ceremonyCookie,
		Value:    c.browser,
		Path:     "/passkeys",
		HttpOnly: true,
		Secure:   secureBrowserOrigin(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(2 * ceremonyLifetime.Seconds()),
	})

	return a.prompt(c, input.Method == "password")
}

func (a *authenticator) validateChange(in beginPasskeyRequest, c *ceremony) error {
	switch in.Operation {
	case "login", "disable", "set-password":
		if in.Method != "passkey" {
			return errBadPasskeyRequest
		}
	case "rename", "remove", "add":
		if in.Method != "passkey" && in.Method != "password" {
			return errBadPasskeyRequest
		}
	default:
		return errBadPasskeyRequest
	}

	if (in.Method == "passkey" || in.Operation == "add") && a.relyingParties[c.origin] == nil {
		return errNoPasskey
	}

	if in.Operation == "add" && len(c.access.Passkeys) >= clientaccess.MaxPasskeys {
		return clientaccess.ErrPasskeyLimit
	}

	if in.Operation == "add" || in.Operation == "rename" {
		label, err := clientaccess.ParsePasskeyLabel(in.Label)
		if err != nil {
			return err
		}

		c.change.label = label
	}

	if in.Operation == "rename" || in.Operation == "remove" {
		id, err := base64.RawURLEncoding.Strict().DecodeString(in.Target)
		if err != nil || credentialIndex(c.access.Passkeys, id) < 0 {
			return errBadPasskeyRequest
		}

		if in.Operation == "remove" && c.access.Password == "" && !a.hasConfiguredKey(c.access.Passkeys, id) {
			return clientaccess.ErrLastBrowserMethod
		}
	}

	if in.Method == "password" {
		if !a.reserveAttempt() || !a.tryPasswordWork() {
			return errBusyPasskey
		}
		defer a.finishPasswordWork()

		if c.access.Password == "" || !matchesPassword(c.access.Password, in.Password) {
			return errPasswordCheck
		}
	}

	if in.Operation == "set-password" {
		if in.NewPassword != in.Confirm {
			return errPasswordConfirm
		}

		if !a.tryPasswordWork() {
			return errBusyPasskey
		}
		defer a.finishPasswordWork()

		var err error

		c.change.verifier, err = passwordVerifier([]byte(in.NewPassword))

		return err
	}

	return nil
}

func (a *authenticator) prompt(c *ceremony, create bool) (passkeyReply, error) {
	result, err := c.prepare(a.relyingParties[c.origin], create)
	if err != nil {
		return passkeyReply{}, err
	}

	result.ID, err = a.ceremonies.put(c, a.now())

	return result, err
}

func (a *authenticator) finishPasskey(w http.ResponseWriter, r *http.Request) (passkeyReply, error) {
	var input finishPasskeyRequest
	if err := readPasskeyJSON(r, &input); err != nil {
		return passkeyReply{}, err
	}

	cookie, err := r.Cookie(ceremonyCookie)
	if err != nil {
		return passkeyReply{}, errCeremonyExpired
	}

	c, err := a.ceremonies.take(input.ID, r.Header.Get("Origin"), cookie.Value, a.now())
	if err != nil {
		return passkeyReply{}, err
	}

	if c.session != "" {
		id, err := a.managementSession(r, c.access)
		if err != nil {
			return passkeyReply{}, err
		}

		if id != c.session {
			return passkeyReply{}, errPasskeySession
		}
	}

	cred, err := c.verify(a.relyingParties[c.origin], input.Credential)
	if err != nil {
		return passkeyReply{}, err
	}

	return a.commitPasskey(w, r, c, cred)
}

func (a *authenticator) commitPasskey(
	w http.ResponseWriter,
	r *http.Request,
	c *ceremony,
	cred *webauthn.Credential,
) (passkeyReply, error) {
	op := c.change.operation
	rotate := op == "disable" || op == "set-password" || op == "remove"

	updated, err := a.commitBrowserAccess(
		w,
		r,
		c.access,
		c.session,
		rotate,
		func(access *clientaccess.BrowserAccess) error {
			if c.kind == "get" {
				i := credentialIndex(access.Passkeys, cred.ID)
				if i < 0 {
					return clientaccess.ErrBrowserChanged
				}

				access.Passkeys[i].Credential = *cred
			}

			switch op {
			case "add":
				if c.kind == "create" {
					access.UserHandle = c.user.handle
					access.Passkeys = append(
						access.Passkeys,
						clientaccess.Passkey{
							RPID:       a.relyingParties[c.origin].Config.RPID,
							Label:      c.change.label,
							Credential: *cred,
						},
					)
				}
			case "rename", "remove":
				id, err := base64.RawURLEncoding.DecodeString(c.change.target)
				i := credentialIndex(access.Passkeys, id)
				if err != nil || i < 0 {
					return clientaccess.ErrBrowserChanged
				}

				if op == "rename" {
					access.Passkeys[i].Label = c.change.label
				} else {
					access.Passkeys = slices.Delete(access.Passkeys, i, i+1)
				}
			case "disable":
				access.Password = ""
			case "set-password":
				access.Password = c.change.verifier
			}

			if access.Password == "" && !a.hasConfiguredKey(access.Passkeys, nil) {
				return clientaccess.ErrLastBrowserMethod
			}

			return nil
		},
	)
	if err != nil {
		return passkeyReply{}, err
	}

	if op == "add" && c.kind == "get" {
		c.access = updated
		return a.prompt(c, true)
	}

	return passkeyReply{Kind: "done"}, nil
}

func (a *authenticator) hasConfiguredKey(keys []clientaccess.Passkey, exceptID []byte) bool {
	for _, party := range a.relyingParties {
		for _, key := range keys {
			if key.RPID == party.Config.RPID && !bytes.Equal(key.Credential.ID, exceptID) {
				return true
			}
		}
	}

	return false
}
