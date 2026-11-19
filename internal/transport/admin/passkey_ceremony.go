// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"go.trippwill.dev/wake/internal/clientaccess"
	"golang.org/x/net/publicsuffix"
)

const (
	ceremonyLifetime   = 3 * time.Minute
	maxCeremonies      = 64
	maxLoginCeremonies = 48
	loginStartBurst    = 8
	loginStartInterval = time.Second
	ceremonyCookie     = "wake_passkey_browser"
)

type PasskeyView struct {
	ID, Label, RPID string
	CanRemove       bool
}
type BrowserAccessView struct {
	Password, Eligible, CanAssert bool
	Passkeys                      []PasskeyView
}

func passkeyParty(origin string) *webauthn.WebAuthn {
	u, err := url.Parse(origin)
	if err != nil {
		return nil
	}

	host := u.Hostname()
	local := host == "localhost" || strings.HasSuffix(host, ".localhost")
	if u.Scheme != "https" && !local {
		return nil
	}

	if suffix, _ := publicsuffix.PublicSuffix(host); !local && suffix == host {
		return nil
	}

	timeout := webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyLifetime}

	rp, err := webauthn.New(&webauthn.Config{
		RPID: host, RPDisplayName: "Wake", RPOrigins: []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: new(true),
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{Login: timeout, Registration: timeout},
	})
	if err != nil {
		return nil
	}

	return rp
}

func (a *authenticator) accessView(r *http.Request, access clientaccess.BrowserAccess) BrowserAccessView {
	view := BrowserAccessView{Password: access.Password != ""}

	var rpID string
	// A GET through a tunnel carries the browser authority, but may omit Origin.
	// The browser checks its secure context; each POST selects one exact origin.
	for origin, party := range a.relyingParties {
		if strings.EqualFold(a.origins[origin], r.Host) {
			rpID = party.Config.RPID
			view.Eligible = true

			break
		}
	}

	for _, k := range access.Passkeys {
		view.CanAssert = view.CanAssert || k.RPID == rpID
		view.Passkeys = append(
			view.Passkeys,
			PasskeyView{
				ID:        base64.RawURLEncoding.EncodeToString(k.Credential.ID),
				Label:     k.Label,
				RPID:      k.RPID,
				CanRemove: view.Password || a.hasConfiguredKey(access.Passkeys, k.Credential.ID),
			},
		)
	}

	return view
}

type passkeyUser struct {
	handle      []byte
	credentials webauthn.Credentials
}

func (u passkeyUser) WebAuthnID() []byte                         { return u.handle }
func (u passkeyUser) WebAuthnName() string                       { return "Wake operator" }
func (u passkeyUser) WebAuthnDisplayName() string                { return "Wake operator" }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

type passkeyChange struct {
	operation, label, target, verifier string
}

type ceremony struct {
	change                   passkeyChange
	origin, browser, session string
	expires                  time.Time
	access                   clientaccess.BrowserAccess
	user                     passkeyUser
	data                     *webauthn.SessionData
	kind                     string
}

func (c *ceremony) prepare(party *webauthn.WebAuthn, create bool) (passkeyReply, error) {
	c.user = passkeyUser{handle: c.access.UserHandle}
	for _, k := range c.access.Passkeys {
		if k.RPID == party.Config.RPID {
			c.user.credentials = append(c.user.credentials, k.Credential)
		}
	}

	if create {
		if len(c.user.handle) == 0 {
			c.user.handle = make([]byte, clientaccess.BrowserUserHandleBytes)
			_, _ = rand.Read(c.user.handle)
		}

		options, data, err := party.BeginRegistration(
			c.user,
			webauthn.WithRegistrationOrigin(c.origin),
			webauthn.WithExclusions(c.user.credentials.CredentialDescriptors()),
		)
		if err != nil {
			return passkeyReply{}, err
		}

		c.kind, c.data = "create", data

		return passkeyReply{Kind: c.kind, Options: options.Response}, nil
	}

	if len(c.user.credentials) == 0 {
		return passkeyReply{}, errNoPasskey
	}

	options, data, err := party.BeginLogin(
		c.user,
		webauthn.WithLoginOrigin(c.origin),
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		return passkeyReply{}, err
	}

	c.kind, c.data = "get", data

	return passkeyReply{Kind: c.kind, Options: options.Response}, nil
}

func (c *ceremony) verify(party *webauthn.WebAuthn, raw []byte) (*webauthn.Credential, error) {
	if c.kind == "create" {
		parsed, err := protocol.ParseCredentialCreationResponseBytes(raw)
		if err != nil || parsed.Response.CollectedClientData.Origin != c.origin {
			return nil, errPasskeyRejected
		}

		result, err := party.CreateCredential(c.user, *c.data, parsed)
		if err != nil {
			return nil, errPasskeyRejected
		}

		return result, nil
	}

	parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil || parsed.Response.CollectedClientData.Origin != c.origin {
		return nil, errPasskeyRejected
	}

	result, err := party.ValidateLogin(c.user, *c.data, parsed)
	// Synced passkeys can report different counters on different devices. The
	// library keeps the high-water mark and records the counter warning.
	if err != nil || (result.Authenticator.CloneWarning && !result.Flags.BackupEligible) {
		return nil, errPasskeyRejected
	}

	return result, nil
}

type pendingCeremonies struct {
	mu             sync.Mutex
	items          map[string]*ceremony
	nextLoginStart time.Time
}

func (p *pendingCeremonies) put(c *ceremony, now time.Time) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.items == nil {
		p.items = make(map[string]*ceremony)
	}

	var (
		oldestLogin, previousLogin  string
		loginCount, managementCount int
	)

	for id, pending := range p.items {
		switch {
		case !now.Before(pending.expires):
			delete(p.items, id)
		case pending.session != "":
			managementCount++
		default:
			loginCount++

			if oldestLogin == "" || pending.expires.Before(p.items[oldestLogin].expires) {
				oldestLogin = id
			}

			if pending.browser == c.browser {
				previousLogin = id
			}
		}
	}

	if c.session == "" {
		// Keep public starts within a bounded burst and steady rate. Each
		// browser gets one pending login, and a full login pool rolls forward.
		delay := max(0, p.nextLoginStart.Sub(now))
		if delay > (loginStartBurst-1)*loginStartInterval {
			return "", errBusyPasskey
		}

		p.nextLoginStart = now.Add(delay + loginStartInterval)
		if previousLogin != "" {
			delete(p.items, previousLogin)
		} else if loginCount >= maxLoginCeremonies {
			delete(p.items, oldestLogin)
		}
	} else if managementCount >= maxCeremonies-maxLoginCeremonies {
		return "", errBusyPasskey
	}

	id := rand.Text()
	c.expires = now.Add(ceremonyLifetime)
	p.items[id] = c

	return id, nil
}

func (p *pendingCeremonies) take(id, origin, browser string, now time.Time) (*ceremony, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	c := p.items[id]
	if c == nil || c.origin != origin || c.browser != browser {
		return nil, errCeremonyExpired
	}

	delete(p.items, id)

	if !now.Before(c.expires) {
		return nil, errCeremonyExpired
	}

	return c, nil
}

func credentialIndex(keys []clientaccess.Passkey, id []byte) int {
	for i, k := range keys {
		if bytes.Equal(k.Credential.ID, id) {
			return i
		}
	}

	return -1
}
