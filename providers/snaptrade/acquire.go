// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package snaptrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
)

const continuation = "snaptrade-scan-1"

type scan struct {
	Authorizations map[string]accountScope
	Verified       bool
	End            string
	Offset         int
	Total          int
	Boundary       string
	Failures       uint
}

func (s scan) state() (provider.State, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return provider.State{}, provider.ErrInvalid
	}

	return provider.NewState(continuation, body)
}

func (s scan) retry(now, until time.Time, reason provider.RetryReason, code string) (provider.Step, error) {
	s.Failures = min(s.Failures+1, 7)
	backoff := (15 * time.Second) << s.Failures

	state, err := s.state()
	if err != nil {
		return provider.Step{}, err
	}

	return provider.NewRetryStep(state, latest(now.Add(backoff), until), reason, code)
}

func latest(times ...time.Time) time.Time {
	var result time.Time
	for _, candidate := range times {
		if candidate.After(result) {
			result = candidate
		}
	}

	return result
}

func report(
	ctx context.Context,
	access provider.Access,
	phase provider.ProgressPhase,
	completed, total int,
) error {
	if completed < 0 || total < 0 {
		return provider.ErrInvalid
	}

	progress, err := provider.NewProgress(phase, uint64(completed), uint64(total))
	if err != nil {
		return err
	}

	return access.Report(ctx, progress)
}

func (a *adapter) Discover(
	ctx context.Context,
	r provider.Request,
	access provider.Access,
) (provider.Discovery, error) {
	step, candidates, err := a.attempt(ctx, r, access, true)
	if err != nil {
		return provider.Discovery{}, err
	}

	result, err := provider.NewDiscovery(step, candidates)
	if err == nil && !step.Artifact().IsZero() && step.State().IsZero() {
		return result.WithCandidateSnapshot()
	}

	return result, err
}

func (a *adapter) Acquire(
	ctx context.Context,
	r provider.Request,
	access provider.Access,
) (provider.Step, error) {
	if r.SourceAccount.IsZero() {
		return provider.Step{}, provider.ErrUnauthorized
	}

	step, _, err := a.attempt(ctx, r, access, false)

	return step, err
}

func (a *adapter) attempt(
	ctx context.Context, r provider.Request, access provider.Access, discovery bool,
) (provider.Step, []provider.Candidate, error) {
	if r.Stream != streamID {
		return provider.Step{}, nil, provider.ErrUnavailable
	}

	var cursor scan
	if !r.State.IsZero() && (r.State.Version() != continuation ||
		json.Unmarshal(r.State.Payload(), &cursor) != nil ||
		cursor.Offset < 0 || cursor.Offset > cursor.Total || cursor.Failures > 7 ||
		(cursor.Verified && cursor.End == "") || (cursor.Boundary != "" && !cursor.Verified)) {
		return provider.Step{}, nil, provider.ErrInvalid
	}

	path := "/authorizations"
	params := url.Values{}
	account := ""

	var scope accountScope

	if !discovery {
		var err error

		scope, err = sourceScope(r.SourceAccount)
		if err != nil {
			return provider.Step{}, nil, err
		}

		if cursor.End == "" {
			cursor.End = r.Now.UTC().Format(time.DateOnly)
		} else if _, err = financial.ParseDate(cursor.End); err != nil {
			return provider.Step{}, nil, provider.ErrInvalid
		}

		if cursor.Verified {
			account = r.SourceAccount.Value()
			path = "/accounts/" + account + "/activities"

			params.Set("endDate", cursor.End)
			params.Set("offset", strconv.Itoa(cursor.Offset))
			params.Set("limit", strconv.Itoa(pageLimit))
			params.Set("type", "BUY,SELL")
		}
	}

	if !cursor.Verified && cursor.Authorizations != nil {
		path = "/accounts/all"
	}

	reply, deferred, err := a.fetch(ctx, r, access, cursor, path, params, account)
	if err != nil || reply.body == nil {
		return deferred, nil, err
	}

	if cursor.Verified {
		step, err := a.activityStep(ctx, r.Now, access, cursor, reply.body)

		return step, nil, err
	}

	artifact, err := provider.NewArtifact(format, "application/json", r.Now, reply.body)
	if err != nil {
		return provider.Step{}, nil, err
	}

	if cursor.Authorizations == nil {
		scopes, err := authorizationScopes(reply.body)
		if err != nil {
			return provider.Step{}, nil, err
		}

		if !discovery {
			candidate, ok := scopes[scope.Connection]
			if !ok || candidate.Broker != scope.Broker || candidate.Institution != scope.Institution {
				step, err := unavailable(ctx, access, artifact, r.Now)

				return step, nil, err
			}

			scopes = map[string]accountScope{scope.Connection: candidate}
		}

		cursor.Authorizations = scopes

		state, err := cursor.state()
		if err != nil {
			return provider.Step{}, nil, err
		}

		step, err := provider.NewStep(state, artifact, r.Now)

		return step, nil, err
	}

	candidates, err := candidateAccounts(reply.body, cursor.Authorizations)
	if err != nil {
		return provider.Step{}, nil, err
	}

	if discovery {
		if err = report(ctx, access, provider.ProgressRetained, len(candidates), len(candidates)); err != nil {
			return provider.Step{}, nil, err
		}

		step, err := provider.NewStep(provider.State{}, artifact, r.Now)

		return step, candidates, err
	}

	for _, candidate := range candidates {
		if candidate.Reference() == r.SourceAccount {
			state, err := (scan{Verified: true, End: cursor.End, Failures: cursor.Failures}).state()
			if err != nil {
				return provider.Step{}, nil, err
			}

			step, err := provider.NewStep(state, artifact, r.Now)

			return step, nil, err
		}
	}

	step, err := unavailable(ctx, access, artifact, r.Now)

	return step, nil, err
}

func unavailable(
	ctx context.Context, access provider.Access, artifact provider.Artifact, now time.Time,
) (provider.Step, error) {
	if err := report(ctx, access, provider.ProgressUnavailable, 0, 0); err != nil {
		return provider.Step{}, err
	}

	return provider.NewStep(provider.State{}, artifact, now.Add(time.Hour))
}

func (a *adapter) fetch(
	ctx context.Context, r provider.Request, access provider.Access, cursor scan,
	path string, params url.Values, account string,
) (response, provider.Step, error) {
	clientID, _ := r.Settings.Get(clientField)

	customer, err := provider.NewPacingScope([]byte(clientID))
	if err != nil {
		return response{}, provider.Step{}, err
	}

	var accountScope provider.PacingScope
	if account != "" {
		accountScope, err = provider.NewPacingScope([]byte(account))
		if err != nil {
			return response{}, provider.Step{}, err
		}
	}

	reservation, err := reserveRequest(ctx, access, customer, accountScope)
	if err != nil {
		return response{}, provider.Step{}, err
	}

	if !reservation.Granted() {
		if err = report(ctx, access, provider.ProgressPaced, cursor.Offset, cursor.Total); err != nil {
			return response{}, provider.Step{}, err
		}

		step, err := provider.NewStep(r.State, provider.Artifact{}, reservation.NextEligibleAt())

		return response{}, step, err
	}

	if err = report(ctx, access, provider.ProgressRequesting, cursor.Offset, cursor.Total); err != nil {
		return response{}, provider.Step{}, err
	}

	var reply response

	started := time.Now()

	err = access.WithSecret(ctx, keyField, func(key []byte) error {
		var err error

		reply, err = a.get(ctx, r.Now, clientID, path, params, key)

		return err
	})

	now := r.Now.Add(time.Since(started))
	if errors.Is(err, provider.ErrTemporary) {
		step, err := cursor.retry(now, time.Time{}, provider.RetryNetwork, "snaptrade-network")

		return response{}, step, err
	}

	if err != nil {
		return response{}, provider.Step{}, err
	}

	retry := retryTime(reply.header, "Retry-After", now)
	customerReset := retryTime(reply.header, "X-RateLimit-Reset", now)
	accountReset := retryTime(reply.header, "X-RateLimit-Account-Reset", now)

	switch reply.status {
	case http.StatusUnauthorized, http.StatusForbidden:
		until := latest(now.Add(15*time.Minute), retry, customerReset, accountReset)
		if err = access.DelayPacing(ctx, clientPacing, customer, until); err != nil {
			return response{}, provider.Step{}, err
		}

		return response{}, provider.Step{}, provider.ErrUnauthorized
	case http.StatusTooManyRequests:
		until := latest(retry, customerReset, accountReset, now.Add(time.Minute))
		if err = access.DelayPacing(ctx, clientPacing, customer, until); err != nil {
			return response{}, provider.Step{}, err
		}

		if account != "" {
			if err = access.DelayPacing(ctx, accountPacing, accountScope, until); err != nil {
				return response{}, provider.Step{}, err
			}
		}

		step, err := cursor.retry(now, until, provider.RetryThrottled, "snaptrade-throttled")

		return response{}, step, err
	case http.StatusOK:
		if reply.header.Get("X-RateLimit-Remaining") == "0" && customerReset.After(now) {
			if err = access.DelayPacing(ctx, clientPacing, customer, customerReset); err != nil {
				return response{}, provider.Step{}, err
			}
		}

		if account != "" && reply.header.Get("X-RateLimit-Account-Remaining") == "0" && accountReset.After(now) {
			if err = access.DelayPacing(ctx, accountPacing, accountScope, accountReset); err != nil {
				return response{}, provider.Step{}, err
			}
		}

		return reply, provider.Step{}, nil
	default:
		if reply.status >= 500 || reply.status == http.StatusNotFound && account != "" {
			if reply.status == http.StatusNotFound {
				cursor = scan{Failures: cursor.Failures}
			}

			step, err := cursor.retry(now, retry, provider.RetryUpstream, "snaptrade-upstream")

			return response{}, step, err
		}

		return response{}, provider.Step{}, provider.ErrInvalid
	}
}

func reserveRequest(
	ctx context.Context,
	access provider.Access,
	customer, account provider.PacingScope,
) (provider.Reservation, error) {
	if account.Digest() == "" {
		return access.ReservePacing(ctx, clientPacing, customer)
	}

	pacer, ok := access.(provider.GroupPacer)
	if !ok {
		return provider.Reservation{}, provider.ErrUnavailable
	}

	return pacer.ReservePacingTogether(
		ctx,
		provider.PacingBudget{Policy: clientPacing, Scope: customer},
		provider.PacingBudget{Policy: accountPacing, Scope: account},
	)
}

func boundary(id string, raw json.RawMessage) string {
	if id != "" {
		return id
	}

	digest := sha256.Sum256(raw)

	return ":" + hex.EncodeToString(digest[:])
}

func (a *adapter) activityStep(
	ctx context.Context, now time.Time, access provider.Access, cursor scan, body []byte,
) (provider.Step, error) {
	page, ids, err := parsePage(body)
	if err != nil {
		return (scan{Failures: cursor.Failures}).retry(
			now,
			time.Time{},
			provider.RetryUpstream,
			"snaptrade-invalid-page",
		)
	}

	total := *page.Pagination.Total
	offset := *page.Pagination.Offset
	next := offset + len(ids)

	if offset != cursor.Offset || (cursor.Boundary != "" &&
		(total != cursor.Total || len(ids) < 2 || boundary(ids[0], page.Data[0]) != cursor.Boundary)) ||
		(next < total && len(ids) < 2) {
		return (scan{Failures: cursor.Failures}).retry(
			now,
			time.Time{},
			provider.RetryUpstream,
			"snaptrade-history-shifted",
		)
	}

	artifact, err := provider.NewArtifact(format, "application/json", now, body)
	if err != nil {
		return provider.Step{}, err
	}

	var state provider.State

	due := now.Add(24 * time.Hour)
	if next < total {
		cursor.Offset, cursor.Total = next-1, total
		cursor.Boundary = boundary(ids[len(ids)-1], page.Data[len(ids)-1])
		cursor.Failures = 0

		state, err = cursor.state()
		if err != nil {
			return provider.Step{}, err
		}

		due = now
	}

	if err = report(ctx, access, provider.ProgressRetained, next, total); err != nil {
		return provider.Step{}, err
	}

	step, err := provider.NewStep(state, artifact, due)
	if err != nil {
		return provider.Step{}, err
	}

	end, err := financial.ParseDate(cursor.End)
	if err != nil {
		return provider.Step{}, provider.ErrInvalid
	}

	coverage, err := provider.NewCoverage(financial.Date{}, end, financial.Date{}, financial.Date{})
	if err != nil {
		return provider.Step{}, err
	}

	return step.WithCoverage(coverage)
}
