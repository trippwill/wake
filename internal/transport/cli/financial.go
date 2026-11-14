// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/transport/financialapi"
)

const (
	snapshotRoute        = "/v1/snapshot"
	maxFinancialResponse = 128 << 20
	maxFinancialError    = 8 << 10
	maxIdentityRedirects = 5
)

func financialCommands() []*cli.Command {
	read := func(name, usage, args string, action cli.ActionFunc) *cli.Command {
		return &cli.Command{
			Name:      name,
			Usage:     usage,
			ArgsUsage: args,
			Action:    action,
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "endpoint",
					Value:   "http://127.0.0.1:8788",
					Sources: cli.EnvVars("WAKE_ENDPOINT"),
					Usage:   "Financial API HTTP or HTTPS origin",
				},
				&cli.StringFlag{
					Name:    "token-file",
					Sources: cli.EnvVars("WAKE_TOKEN_FILE"),
					Usage:   "Absolute path to a private client token file",
				},
			},
		}
	}
	group := func(name, usage string, children ...*cli.Command) *cli.Command {
		return &cli.Command{
			Name: name, Usage: usage, Commands: children, HideHelpCommand: true,
			Action: func(_ context.Context, c *cli.Command) error {
				printCommandHelp(c.ErrWriter, c)
				return usageError("specify a " + name + " command")
			},
		}
	}

	query := read(
		"query",
		"Filter one financial snapshot and return one page of activity as JSON",
		"",
		queryActivity,
	)
	query.Flags = append(
		query.Flags,
		&cli.StringFlag{Name: "account", Usage: "Exact Wake account ID"},
		&cli.StringFlag{Name: "from", Usage: "Earliest activity date, inclusive (YYYY-MM-DD)"},
		&cli.StringFlag{Name: "to", Usage: "Latest activity date, inclusive (YYYY-MM-DD)"},
		&cli.StringFlag{
			Name:  "family",
			Usage: "Activity family: trade, lifecycle, conversion, security-transfer or cash",
		},
		&cli.StringFlag{
			Name:  "state",
			Value: string(financialapi.Accepted),
			Usage: "Activity state: accepted, cancelled, replaced or all",
		},
		&cli.IntFlag{Name: "limit", Value: 100, Usage: "Maximum results on this page (1–1000)"},
		&cli.IntFlag{Name: "offset", Usage: "Skip this many matching results"},
	)

	return []*cli.Command{
		group("accounts", "Read financial accounts",
			read("list", "List Wake account resources as JSON", "", listAccounts),
			read("get", "Read one account as JSON", "ACCOUNT_ID", getAccount),
		),
		group("activity", "Read financial activity", query),
		group("positions", "Read reported account positions",
			read("get", "Read the latest reported positions view as JSON", "ACCOUNT_ID", getPositions),
		),
		read("doctor", "Check access to Wake's private financial API as JSON", "", financialDoctor),
		group("request", "Call the private financial API",
			read("get", "Read a finite v1 API route as JSON", "PATH", rawFinancialGet),
		),
	}
}

func financialEndpoint(c *cli.Command) (string, error) {
	value := c.String("endpoint")

	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Path != "" ||
		parsed.RawQuery != "" ||
		parsed.ForceQuery ||
		parsed.Fragment != "" ||
		parsed.Opaque != "" {
		return "", usageError("--endpoint must be an HTTP or HTTPS origin without credentials, path or query")
	}

	return value, nil
}

type financialResponse struct {
	body json.RawMessage
	path string
}

func resourceKind(u *url.URL) string {
	if !strings.HasPrefix(u.Path, "/v1/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ""
	}

	kind, id, ok := strings.Cut(strings.TrimPrefix(u.Path, "/v1/"), "/")
	if !ok || !validFinancialID(id) {
		return ""
	}

	switch kind {
	case "accounts", "instruments", "listings", "activity":
		return kind
	default:
		return ""
	}
}

func financialRedirect(req *http.Request, via []*http.Request) error {
	// Compare the original Location with the resolved path to require a root-relative destination.
	locations := req.Response.Header.Values("Location")
	if req.Response.StatusCode != http.StatusPermanentRedirect ||
		len(locations) != 1 || len(via) > maxIdentityRedirects ||
		locations[0] != req.URL.EscapedPath() ||
		req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host || req.URL.User != nil {
		return http.ErrUseLastResponse
	}

	kind := resourceKind(via[0].URL)
	if kind == "" || resourceKind(req.URL) != kind {
		return http.ErrUseLastResponse
	}

	for _, prior := range via {
		if prior.URL.Path == req.URL.Path {
			return http.ErrUseLastResponse
		}
	}

	return nil
}

func financialResponseError(res *http.Response) error {
	message := failure("Wake API returned HTTP %d", res.StatusCode)
	expected := financialapi.ErrorCode("")
	switch res.StatusCode {
	case http.StatusBadRequest:
		expected = financialapi.InvalidRequest
	case http.StatusNotFound:
		expected = financialapi.NotFound
	case http.StatusMethodNotAllowed:
		expected = financialapi.MethodNotAllowed
	case http.StatusConflict:
		expected = financialapi.StaleRevision
	case http.StatusServiceUnavailable:
		expected = financialapi.HistoryUpdating
	case http.StatusInternalServerError:
		expected = financialapi.InternalError
	default:
		return message
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxFinancialError+1))

	var value financialapi.Error
	if err != nil || len(body) > maxFinancialError || json.Unmarshal(body, &value) != nil ||
		value.Code != expected || value.Message == "" {
		return message
	}

	return commandError{status: exitFailure, message: message.Error(), code: string(value.Code)}
}

// financialGET stays on the configured origin and accepts finite JSON
// objects only. A failed read leaves stdout untouched.
func financialGET(
	ctx context.Context,
	endpoint, tokenFile, target string,
	probe bool,
) (financialResponse, error) {
	token, err := clientaccess.ReadTokenFile(tokenFile)
	if err != nil {
		return financialResponse{}, failure("read client token: %v", err)
	}
	defer token.Clear()

	value, err := token.Copy()
	if err != nil {
		return financialResponse{}, failure("client token unavailable")
	}
	defer clear(value)

	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}
	defer transport.CloseIdleConnections()

	client := http.Client{
		Transport:     transport,
		Timeout:       20 * time.Second,
		CheckRedirect: financialRedirect,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+target, nil)
	if err != nil {
		return financialResponse{}, usageError("invalid Wake API path")
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+string(value))

	if probe {
		req.Header.Set("If-None-Match", "*")
	}

	res, err := client.Do(req)
	if err != nil {
		return financialResponse{}, failure(
			"cannot read the Wake API; check the endpoint, transport and daemon",
		)
	}
	defer func() { _ = res.Body.Close() }()

	if probe && res.StatusCode == http.StatusNotModified {
		return financialResponse{}, nil
	}

	if probe || res.StatusCode != http.StatusOK {
		return financialResponse{}, financialResponseError(res)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxFinancialResponse+1))
	if err != nil {
		return financialResponse{}, failure("could not read the complete Wake API response")
	}

	if len(body) > maxFinancialResponse {
		return financialResponse{}, failure("Wake API response exceeds 128 MiB")
	}

	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' || !json.Valid(body) {
		return financialResponse{}, failure("Wake API response is not a complete JSON object")
	}

	return financialResponse{body: body, path: res.Request.URL.Path}, nil
}

func financialRead(ctx context.Context, c *cli.Command, route string) (financialResponse, error) {
	endpoint, err := financialEndpoint(c)
	if err != nil {
		return financialResponse{}, err
	}

	return financialGET(ctx, endpoint, c.String("token-file"), route, false)
}

type financialCollection struct {
	WarehouseID string            `json:"warehouse_id"`
	Version     string            `json:"version"`
	Resources   []json.RawMessage `json:"resources"`
}

type activityFields struct {
	AccountID string                      `json:"account_id"`
	Date      string                      `json:"date"`
	Family    financialapi.ActivityFamily `json:"family"`
	State     financialapi.ActivityState  `json:"state"`
}

type financialResource struct {
	ID       string                `json:"id"`
	Kind     string                `json:"kind"`
	Revision int64                 `json:"revision"`
	Account  *financialapi.Account `json:"account"`
	Activity *activityFields       `json:"activity"`
	raw      json.RawMessage
}

func readFinancialCollection(
	ctx context.Context, c *cli.Command, kind string,
) (financialCollection, []financialResource, error) {
	var collection financialCollection

	response, err := financialRead(ctx, c, snapshotRoute)
	if err != nil {
		return collection, nil, err
	}

	if json.Unmarshal(response.body, &collection) != nil || collection.WarehouseID == "" ||
		collection.Version == "" || collection.Resources == nil {
		return collection, nil, failure("invalid Wake financial snapshot")
	}

	var resources []financialResource
	for _, raw := range collection.Resources {
		var resource financialResource
		if json.Unmarshal(raw, &resource) != nil || resource.ID == "" || resource.Kind == "" ||
			resource.Revision < 1 {
			return collection, nil, failure("invalid resource in Wake financial snapshot")
		}

		if resource.Kind == kind {
			resource.raw = raw
			resources = append(resources, resource)
		}
	}

	return collection, resources, nil
}

func listAccounts(ctx context.Context, c *cli.Command) error {
	if c.NArg() != 0 {
		return usageError("accounts list takes no arguments")
	}

	result, resources, err := readFinancialCollection(ctx, c, "accounts")
	if err != nil {
		return err
	}

	slices.SortFunc(resources, func(a, b financialResource) int { return cmp.Compare(a.ID, b.ID) })

	result.Resources = make([]json.RawMessage, 0, len(resources))
	for _, r := range resources {
		if r.Account == nil {
			return failure("invalid account in Wake financial snapshot")
		}

		result.Resources = append(result.Resources, r.raw)
	}

	return json.NewEncoder(c.Writer).Encode(result)
}

func validFinancialID(id string) bool {
	return id != "" && len(id) <= 256 && id != "." && id != ".." &&
		utf8.ValidString(id) && !strings.ContainsRune(id, '/') && strings.IndexFunc(id, unicode.IsControl) < 0
}

func readAccountEndpoint(
	ctx context.Context,
	c *cli.Command,
	suffix string,
) (string, financialResponse, error) {
	id := c.Args().First()
	if c.NArg() != 1 || !validFinancialID(id) {
		return "", financialResponse{}, usageError("provide one valid Wake account ID")
	}

	response, err := financialRead(ctx, c, "/v1/accounts/"+url.PathEscape(id)+suffix)

	return id, response, err
}

func getAccount(ctx context.Context, c *cli.Command) error {
	_, response, err := readAccountEndpoint(ctx, c, "")
	if err != nil {
		return err
	}

	id := strings.TrimPrefix(response.path, "/v1/accounts/")

	var account financialResource
	if json.Unmarshal(response.body, &account) != nil || account.ID != id ||
		account.Kind != "accounts" || account.Revision < 1 || account.Account == nil {
		return failure("invalid account response from Wake")
	}

	return json.NewEncoder(c.Writer).Encode(response.body)
}

func getPositions(ctx context.Context, c *cli.Command) error {
	id, response, err := readAccountEndpoint(ctx, c, "/positions")
	if err != nil {
		return err
	}

	var view financialapi.PositionsView
	if json.Unmarshal(response.body, &view) != nil || view.AccountId != id ||
		view.WarehouseId == "" || view.Version == "" || !view.Status.Valid() {
		return failure("invalid positions response from Wake")
	}

	switch view.Status {
	case financialapi.PositionsViewStatusAvailable:
		if view.AsOf == nil || view.Positions == nil || view.Reason != nil {
			return failure("invalid available positions view from Wake")
		}
	case financialapi.PositionsViewStatusUnavailable:
		if view.Positions != nil || view.Reason == nil || !view.Reason.Valid() {
			return failure("invalid unavailable positions view from Wake")
		}
	}

	return json.NewEncoder(c.Writer).Encode(response.body)
}

func financialDoctor(ctx context.Context, c *cli.Command) error {
	if c.NArg() != 0 {
		return usageError("doctor takes no arguments")
	}

	endpoint, err := financialEndpoint(c)
	if err != nil {
		return err
	}

	if _, err := financialGET(ctx, endpoint, c.String("token-file"), snapshotRoute, true); err != nil {
		return err
	}

	version := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		version = info.Main.Version
	}

	return json.NewEncoder(c.Writer).Encode(map[string]any{
		"build_version": version, "api_version": "v1", "endpoint": endpoint,
		"auth": "client_token", "reachable": true,
	})
}

func rawFinancialGet(ctx context.Context, c *cli.Command) error {
	target := c.Args().First()
	parsed, err := url.ParseRequestURI(target)
	if c.NArg() != 1 || err != nil || parsed.IsAbs() || parsed.Host != "" ||
		!strings.HasPrefix(parsed.Path, "/v1/") || path.Clean(parsed.Path) != parsed.Path ||
		parsed.Path == "/v1/events" || strings.ContainsRune(target, '#') {
		return usageError("provide a finite root-relative Wake v1 API path")
	}

	response, err := financialRead(ctx, c, target)
	if err != nil {
		return err
	}

	return json.NewEncoder(c.Writer).Encode(response.body)
}
