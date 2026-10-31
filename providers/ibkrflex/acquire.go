// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"slices"
	"strconv"
	"time"

	"go.trippwill.dev/wake/provider"
)

type (
	window struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	missingWindow struct {
		Window window `json:"window"`
		Due    int64  `json:"due"`
	}
	continuation struct {
		Query      string          `json:"query"`
		Started    bool            `json:"started"`
		Reference  string          `json:"reference,omitempty"`
		Period     int             `json:"period,omitempty"`
		Anchor     string          `json:"anchor,omitempty"`
		Current    *window         `json:"current,omitempty"`
		Queue      []window        `json:"queue,omitempty"`
		Missing    []missingWindow `json:"missing,omitempty"`
		LastFrom   string          `json:"last_from,omitempty"`
		LastTo     string          `json:"last_to,omitempty"`
		LastRecent string          `json:"last_recent,omitempty"`
		Recent     bool            `json:"recent,omitempty"`
		Failures   int             `json:"failures,omitempty"`
	}
)

func (s *Source) Acquire(
	ctx context.Context,
	request provider.Request,
	access provider.Access,
) (provider.Step, error) {
	return s.acquire(ctx, request, access, false)
}

func (s *Source) acquire(
	ctx context.Context,
	request provider.Request,
	access provider.Access,
	discovery bool,
) (provider.Step, error) {
	format, kind, err := streamFormat(request.Stream)
	if err != nil || request.Now.IsZero() || access == nil {
		return provider.Step{}, provider.ErrInvalid
	}

	query, ok := request.Settings.Get("query_id")
	if !ok || !digits(query) || len(query) > 64 {
		return provider.Step{}, provider.ErrInvalid
	}

	state, err := readContinuation(request.State, query)
	if err != nil {
		return provider.Step{}, err
	}

	if state.Reference == "" {
		state.choose(request.Now, request.Stream)
	}

	step := provider.Step{}
	err = access.WithSecret(ctx, "token", func(secret []byte) error {
		scope, scopeErr := provider.NewPacingScope(secret)
		if scopeErr != nil {
			return provider.ErrUnauthorized
		}

		permit, paceErr := access.ReservePacing(ctx, webBudget, scope)
		if paceErr != nil {
			return paceErr
		}

		if !permit.Granted() {
			if paceErr = progress(ctx, access, "paced"); paceErr != nil {
				return paceErr
			}

			step, paceErr = state.step(provider.Artifact{}, permit.NextEligibleAt())

			return paceErr
		}

		operation, parameter := "GetStatement", state.Reference
		parameters := url.Values{}
		if state.Reference == "" {
			operation, parameter = "SendRequest", query

			if state.Current != nil {
				parameters.Set("fd", compactDate(state.Current.From))
				parameters.Set("td", compactDate(state.Current.To))
			} else {
				parameters.Set("p", strconv.Itoa(state.Period))
			}
		}

		if paceErr = progress(ctx, access, "requesting"); paceErr != nil {
			return paceErr
		}

		response, callErr := s.request(ctx, operation, secret, parameter, parameters)
		until := retryAfter(response.header, request.Now)
		if response.code == "1018" || response.code == "429" {
			if minimum := request.Now.Add(time.Minute); until.Before(minimum) {
				until = minimum
			}
		}

		if until.After(request.Now) {
			if paceErr = access.DelayPacing(ctx, webBudget, scope, until); paceErr != nil {
				return paceErr
			}
		}

		if callErr != nil {
			if callErr != provider.ErrTemporary {
				return callErr
			}

			state.Failures = min(state.Failures+1, 10)

			step, paceErr = state.step(
				provider.Artifact{},
				later(request.Now.Add(time.Duration(1<<min(state.Failures, 7))*time.Second), until),
			)
			if paceErr == nil {
				step, paceErr = provider.NewRetryStep(
					step.State(),
					step.NextEligibleAt(),
					provider.RetryNetwork,
					"network-failure",
				)
			}

			return paceErr
		}

		if response.reference != "" {
			state.Reference, state.Started, state.Failures = response.reference, true, 0

			if paceErr = progress(ctx, access, "generating"); paceErr != nil {
				return paceErr
			}

			step, paceErr = state.step(provider.Artifact{}, later(request.Now.Add(10*time.Second), until))

			return paceErr
		}

		if response.body != nil {
			metadata, inspectErr := inspectReport(ctx, response.body, kind, nil)
			if inspectErr != nil {
				return inspectErr
			}

			artifact, artifactErr := provider.NewArtifact(format, "application/xml", request.Now, response.body)
			if artifactErr != nil {
				return artifactErr
			}

			digest, artifactErr := reportEquivalence(response.body)
			if artifactErr != nil {
				return artifactErr
			}

			artifact, artifactErr = artifact.WithEquivalence("ibkr-report-window/1", digest)
			if artifactErr != nil {
				return artifactErr
			}

			requested := periodWindow(state.Anchor, state.Period)
			if state.Current != nil {
				requested = *state.Current
			}

			requestedFrom, _ := parseDate(requested.From)
			requestedTo, _ := parseDate(requested.To)
			reportedFrom, _ := parseDate(metadata.lastFrom)
			reportedTo, _ := parseDate(metadata.lastTo)

			coverage, coverageErr := provider.NewCoverage(requestedFrom, requestedTo, reportedFrom, reportedTo)
			if coverageErr != nil {
				return coverageErr
			}

			state.Reference, state.Current, state.Period, state.Failures = "", nil, 0, 0
			state.Anchor = ""
			if state.Recent || state.LastRecent == "" {
				state.LastRecent = request.Now.UTC().Format(time.RFC3339Nano)
			}

			state.Recent = false
			if metadata.lastTo > state.LastTo {
				state.LastFrom, state.LastTo = metadata.lastFrom, metadata.lastTo
			}

			next := request.Now.Add(5 * time.Minute)

			if len(state.Queue) != 0 {
				next = request.Now.Add(6 * time.Second)
			}

			if paceErr = progress(ctx, access, "retained"); paceErr != nil {
				return paceErr
			}

			step, artifactErr = state.step(artifact, later(next, until))
			if artifactErr == nil {
				step, artifactErr = step.WithCoverage(coverage)
			}

			return artifactErr
		}

		switch response.code {
		case "1010", "1014", "1016", "1020":
			return provider.ErrInvalid
		case "1011", "1012", "1013", "1015", "1025":
			return provider.ErrUnauthorized
		case "1017":
			state.Reference = ""
		case "1003":
			if discovery {
				return provider.ErrUnavailable
			}

			state.unavailable(request.Now)
		}

		state.Started = true
		state.Failures = min(state.Failures+1, 10)
		delay := min(10*time.Second*time.Duration(1<<min(state.Failures-1, 7)), 15*time.Minute)
		if response.code == "1003" {
			delay = 5 * time.Minute

			if len(state.Queue) != 0 {
				delay = 6 * time.Second
			}
		}

		if paceErr = progress(ctx, access, "ibkr-"+response.code); paceErr != nil {
			return paceErr
		}

		step, paceErr = state.step(provider.Artifact{}, later(request.Now.Add(delay), until))
		if paceErr == nil && response.code != "1019" && response.code != "1003" {
			code := "ibkr-" + response.code
			if len(response.code) == 3 {
				code = "http-" + response.code
			}

			reason := provider.RetryUpstream
			if response.code == "1018" || response.code == "429" {
				reason = provider.RetryThrottled
			}

			step, paceErr = provider.NewRetryStep(step.State(), step.NextEligibleAt(), reason, code)
		}

		return paceErr
	})

	return step, err
}

func readContinuation(saved provider.State, query string) (continuation, error) {
	state := continuation{Query: query}
	if saved.IsZero() {
		return state, nil
	}

	if saved.Version() != continuationVersion {
		return continuation{}, provider.ErrUnavailable
	}

	decoder := json.NewDecoder(bytes.NewReader(saved.Payload()))
	decoder.DisallowUnknownFields()

	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF || state.Query != query ||
		state.Failures < 0 || state.Failures > 10 || len(state.Queue) > 64 || len(state.Missing) > 512 ||
		(state.Reference != "" && !safeText(state.Reference, 1024)) || state.Period < 0 || state.Period > 365 ||
		(state.Current != nil && state.Period != 0) || (state.Period != 0 && state.Anchor == "") {
		return continuation{}, provider.ErrInvalid
	}

	for _, value := range []string{state.LastFrom, state.LastTo, state.Anchor} {
		if value != "" {
			if _, err := parseDate(value); err != nil {
				return continuation{}, provider.ErrInvalid
			}
		}
	}

	if state.LastRecent != "" {
		if _, err := time.Parse(time.RFC3339Nano, state.LastRecent); err != nil {
			return continuation{}, provider.ErrInvalid
		}
	}

	for _, entry := range state.Queue {
		if !entry.valid() {
			return continuation{}, provider.ErrInvalid
		}
	}

	for _, entry := range state.Missing {
		if !entry.Window.valid() || entry.Due <= 0 {
			return continuation{}, provider.ErrInvalid
		}
	}

	if state.Current != nil && !state.Current.valid() {
		return continuation{}, provider.ErrInvalid
	}

	if state.Reference != "" && state.Current == nil && state.Period == 0 {
		return continuation{}, provider.ErrInvalid
	}

	return state, nil
}

func (state *continuation) choose(now time.Time, _ string) {
	if state.Period != 0 {
		if state.Anchor != civil(now).Format("2006-01-02") {
			selected := periodWindow(state.Anchor, state.Period)
			state.Current, state.Period, state.Anchor = &selected, 0, ""
		}

		return
	}

	if state.Current != nil {
		return
	}

	lastRecent, _ := time.Parse(time.RFC3339Nano, state.LastRecent)
	if state.Started && (len(state.Queue) > 0 || len(state.Missing) > 0) &&
		(lastRecent.IsZero() || !now.Before(lastRecent.Add(5*time.Minute))) {
		state.Period = 2
		state.Anchor = civil(now).Format("2006-01-02")
		state.Recent = true

		return
	}

	if len(state.Queue) != 0 {
		selected := state.Queue[0]
		state.Current = &selected
		state.Queue = state.Queue[1:]

		return
	}

	for i, missing := range state.Missing {
		if missing.Due <= now.Unix() {
			selected := missing.Window
			state.Current = &selected
			state.Missing = append(state.Missing[:i], state.Missing[i+1:]...)

			return
		}
	}

	state.Period = 365
	state.Anchor = civil(now).Format("2006-01-02")
	if !state.Started {
		return
	}

	state.Period = 2
	state.Recent = true

	if state.LastTo == "" {
		return
	}

	last, err := time.Parse("2006-01-02", state.LastTo)
	if err != nil {
		return
	}

	days := int(civil(now).Sub(last) / (24 * time.Hour))
	if days <= 365 {
		if days > state.Period {
			state.Period = min(365, days+7)
		}

		return
	}
	// Resume older missed ranges in bounded dated requests, newest first.
	from := last.AddDate(0, 0, 1)

	to := civil(now)
	for !from.After(to) {
		end := from.AddDate(0, 0, 364)
		if end.After(to) {
			end = to
		}

		state.Queue = append(state.Queue, window{from.Format("2006-01-02"), end.Format("2006-01-02")})
		from = end.AddDate(0, 0, 1)
	}

	state.Period = 0
	state.Anchor = ""
	state.Recent = false
	slices.Reverse(state.Queue)

	if len(state.Queue) != 0 {
		selected := state.Queue[0]
		state.Current = &selected
		state.Queue = state.Queue[1:]
	}
}

func (state *continuation) unavailable(now time.Time) {
	state.Reference = ""
	if state.Recent {
		state.LastRecent = now.UTC().Format(time.RFC3339Nano)
		state.Recent = false
	}

	if state.Current == nil {
		selected := periodWindow(state.Anchor, state.Period)
		state.Current, state.Period = &selected, 0
	}

	from, _ := time.Parse("2006-01-02", state.Current.From)
	to, _ := time.Parse("2006-01-02", state.Current.To)
	days := int(to.Sub(from)/(24*time.Hour)) + 1
	if days > 1 {
		middle := from.AddDate(0, 0, days/2-1)
		older := window{state.Current.From, middle.Format("2006-01-02")}
		newer := window{middle.AddDate(0, 0, 1).Format("2006-01-02"), state.Current.To}
		state.Queue = append([]window{newer, older}, state.Queue...)
	} else {
		due := civil(now).AddDate(0, 0, 1).Unix()
		state.Missing = append(state.Missing, missingWindow{*state.Current, due})
		slices.SortFunc(state.Missing, func(a, b missingWindow) int {
			if a.Window.From < b.Window.From {
				return -1
			}

			if a.Window.From > b.Window.From {
				return 1
			}

			return 0
		})

		var compact []missingWindow
		for _, entry := range state.Missing {
			if len(compact) != 0 {
				previous := &compact[len(compact)-1]
				end, _ := time.Parse("2006-01-02", previous.Window.To)
				merged := window{previous.Window.From, max(previous.Window.To, entry.Window.To)}
				if previous.Due == entry.Due && merged.valid() &&
					entry.Window.From <= end.AddDate(0, 0, 1).Format("2006-01-02") {
					previous.Window.To = max(previous.Window.To, entry.Window.To)
					continue
				}
			}

			compact = append(compact, entry)
		}

		state.Missing = compact
	}

	state.Current = nil
	state.Anchor = ""
}

func periodWindow(anchor string, days int) window {
	date, _ := time.Parse("2006-01-02", anchor)
	return window{date.AddDate(0, 0, -max(days-1, 0)).Format("2006-01-02"), date.Format("2006-01-02")}
}

func (state continuation) step(artifact provider.Artifact, next time.Time) (provider.Step, error) {
	if len(state.Queue) > 64 || len(state.Missing) > 512 {
		return provider.Step{}, provider.ErrLimit
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		return provider.Step{}, provider.ErrInvalid
	}

	saved, err := provider.NewState(continuationVersion, encoded)
	if err != nil {
		return provider.Step{}, err
	}

	return provider.NewStep(saved, artifact, next)
}

func (w window) valid() bool {
	from, err := time.Parse("2006-01-02", w.From)
	if err != nil {
		return false
	}

	to, err := time.Parse("2006-01-02", w.To)

	return err == nil && !to.Before(from) && to.Sub(from) < 365*24*time.Hour
}

func civil(now time.Time) time.Time {
	year, month, day := now.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
func compactDate(value string) string { return value[:4] + value[5:7] + value[8:] }
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}

	return a
}

func progress(ctx context.Context, access provider.Access, code string) error {
	phase := provider.ProgressWaiting
	switch code {
	case "requesting":
		phase = provider.ProgressRequesting
	case "paced", "ibkr-1018":
		phase = provider.ProgressPaced
	case "retained":
		phase = provider.ProgressRetained
	case "ibkr-1003":
		phase = provider.ProgressUnavailable
	}

	value, err := provider.NewProgress(phase, 0, 0)
	if err != nil {
		return err
	}

	return access.Report(ctx, value)
}
