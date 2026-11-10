// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/transport/socket"
)

const (
	snapshotRoute        = "/v1/snapshot"
	maxFinancialResponse = 128 << 20
)

func financialCommands(common func() []cli.Flag) []*cli.Command {
	read := func(name, usage, args string, action cli.ActionFunc) *cli.Command {
		return &cli.Command{
			Name:      name,
			Usage:     usage,
			ArgsUsage: args,
			Action:    action,
			Flags: append(
				common(),
				&cli.StringFlag{Name: "socket", Usage: "Absolute path to the private Wake socket"},
			),
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
			Value: string(socket.Accepted),
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

func financialSocketPath(c *cli.Command) (string, error) {
	if c.IsSet("socket") {
		if c.IsSet("config") {
			return "", usageError("use either --socket or --config")
		}

		value := c.String("socket")
		if !filepath.IsAbs(value) || strings.ContainsRune(value, 0) {
			return "", usageError("--socket must be an absolute Unix socket path")
		}

		return value, nil
	}

	dir, err := config.ResolveRuntimeDir(c.String("config"), c.IsSet("config"))
	if errors.Is(err, config.ErrRuntimeDirRequired) {
		return "", failure("Wake runtime directory is missing; configure it or pass --socket")
	}

	if err != nil {
		return "", failure("cannot resolve the Wake profile; check configuration or pass --socket")
	}

	return filepath.Join(dir, "wake.sock"), nil
}

// financialGET is pinned to the configured Unix socket and accepts finite JSON
// objects only. A failed read leaves stdout untouched.
func financialGET(ctx context.Context, socketPath, target string, probe bool) (json.RawMessage, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()

	client := http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://wake"+target, nil)
	if err != nil {
		return nil, usageError("invalid Wake API path")
	}

	req.Header.Set("Accept", "application/json")

	if probe {
		req.Header.Set("If-None-Match", "*")
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, failure(
			"cannot read the Wake socket; check that Wake is running and the socket is accessible",
		)
	}
	defer func() { _ = res.Body.Close() }()

	if probe && res.StatusCode == http.StatusNotModified {
		return nil, nil
	}

	if probe || res.StatusCode != http.StatusOK {
		return nil, failure("Wake API returned HTTP %d", res.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxFinancialResponse+1))
	if err != nil {
		return nil, failure("could not read the complete Wake API response")
	}

	if len(body) > maxFinancialResponse {
		return nil, failure("Wake API response exceeds 128 MiB")
	}

	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' || !json.Valid(body) {
		return nil, failure("Wake API response is not a complete JSON object")
	}

	return body, nil
}

func financialRead(ctx context.Context, c *cli.Command, route string) (json.RawMessage, error) {
	socketPath, err := financialSocketPath(c)
	if err != nil {
		return nil, err
	}

	return financialGET(ctx, socketPath, route, false)
}

type financialCollection struct {
	WarehouseID string            `json:"warehouse_id"`
	Version     string            `json:"version"`
	Resources   []json.RawMessage `json:"resources"`
}

type activityFields struct {
	AccountID string                `json:"account_id"`
	Date      string                `json:"date"`
	Family    socket.ActivityFamily `json:"family"`
	State     socket.ActivityState  `json:"state"`
}

type financialResource struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Revision int64           `json:"revision"`
	Account  *socket.Account `json:"account"`
	Activity *activityFields `json:"activity"`
	raw      json.RawMessage
}

func readFinancialCollection(
	ctx context.Context, c *cli.Command, kind string,
) (financialCollection, []financialResource, error) {
	var collection financialCollection

	body, err := financialRead(ctx, c, snapshotRoute)
	if err != nil {
		return collection, nil, err
	}

	if json.Unmarshal(body, &collection) != nil || collection.WarehouseID == "" ||
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
) (string, json.RawMessage, error) {
	id := c.Args().First()
	if c.NArg() != 1 || !validFinancialID(id) {
		return "", nil, usageError("provide one valid Wake account ID")
	}

	body, err := financialRead(ctx, c, "/v1/accounts/"+url.PathEscape(id)+suffix)

	return id, body, err
}

func getAccount(ctx context.Context, c *cli.Command) error {
	id, body, err := readAccountEndpoint(ctx, c, "")
	if err != nil {
		return err
	}

	var account financialResource
	if json.Unmarshal(body, &account) != nil || account.ID != id ||
		account.Kind != "accounts" || account.Revision < 1 || account.Account == nil {
		return failure("invalid account response from Wake")
	}

	return json.NewEncoder(c.Writer).Encode(body)
}

func getPositions(ctx context.Context, c *cli.Command) error {
	id, body, err := readAccountEndpoint(ctx, c, "/positions")
	if err != nil {
		return err
	}

	var view socket.PositionsView
	if json.Unmarshal(body, &view) != nil || view.AccountId != id ||
		view.WarehouseId == "" || view.Version == "" || !view.Status.Valid() {
		return failure("invalid positions response from Wake")
	}

	switch view.Status {
	case socket.PositionsViewStatusAvailable:
		if view.AsOf == nil || view.Positions == nil || view.Reason != nil {
			return failure("invalid available positions view from Wake")
		}
	case socket.PositionsViewStatusUnavailable:
		if view.Positions != nil || view.Reason == nil || !view.Reason.Valid() {
			return failure("invalid unavailable positions view from Wake")
		}
	}

	return json.NewEncoder(c.Writer).Encode(body)
}

func financialDoctor(ctx context.Context, c *cli.Command) error {
	if c.NArg() != 0 {
		return usageError("doctor takes no arguments")
	}

	socketPath, err := financialSocketPath(c)
	if err != nil {
		return err
	}

	if _, err := financialGET(ctx, socketPath, snapshotRoute, true); err != nil {
		return err
	}

	version := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		version = info.Main.Version
	}

	return json.NewEncoder(c.Writer).Encode(map[string]any{
		"build_version": version, "api_version": "v1", "socket_path": socketPath,
		"auth": "same_uid", "reachable": true,
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

	body, err := financialRead(ctx, c, target)
	if err != nil {
		return err
	}

	return json.NewEncoder(c.Writer).Encode(body)
}
