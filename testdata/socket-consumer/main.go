// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// An independent current-state client using only documented HTTP/JSON over a Unix socket.
package main

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/getkin/kin-openapi/openapi3"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type resource struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Revision int64  `json:"revision"`
}
type snapshot struct {
	WarehouseID string            `json:"warehouse_id"`
	Version     string            `json:"version"`
	Resources   []json.RawMessage `json:"resources"`
}
type state struct {
	WarehouseID string                     `json:"warehouse_id"`
	Version     string                     `json:"version"`
	Resources   map[string]json.RawMessage `json:"resources"`
}
type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e protocolError) Error() string { return e.Code + ": " + e.Message }

type client struct {
	http   *http.Client
	base   string
	stderr io.Writer
}

func decode(data []byte, out any) error {
	if err := uniqueJSON(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("protocol: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("protocol: trailing JSON")
	}
	return nil
}

// Reject duplicate keys rather than accepting a later replacement in a complete response.
func uniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("protocol: excessive nesting")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, e := decoder.Token()
				if e != nil {
					return e
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("protocol: duplicate object key")
				}
				seen[name] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for decoder.More() {
				if err = value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("protocol: invalid JSON")
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("protocol: trailing JSON")
	}
	return nil
}

func (c *client) request(ctx context.Context, path, version string) ([]byte, string, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, "", false, err
	}
	if version != "" {
		request.Header.Set("If-None-Match", `"`+version+`"`)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, "", false, fmt.Errorf("socket transport: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotModified {
		if version == "" || response.Header.Get("ETag") != `"`+version+`"` {
			return nil, "", false, errors.New("protocol: conditional response lacks matching baseline")
		}
		return nil, version, true, nil
	}
	if response.Header.Get("Content-Type") != "application/json" {
		return nil, "", false, errors.New("protocol: expected application/json")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (128<<20)+1))
	if err != nil {
		return nil, "", false, fmt.Errorf("interrupted response: %w", err)
	}
	if len(data) > 128<<20 {
		return nil, "", false, errors.New("protocol: response exceeds 128 MiB bound")
	}
	if response.StatusCode != http.StatusOK {
		var failure protocolError
		if decode(data, &failure) != nil || failure.Code == "" {
			return nil, "", false, errors.New("protocol: malformed error")
		}
		return nil, "", false, failure
	}
	return data, response.Header.Get("ETag"), false, nil
}
func (c *client) get(ctx context.Context, path string, out any) error {
	data, _, _, err := c.request(ctx, path, "")
	if err != nil {
		return err
	}
	if strings.Contains(path, "/explanation?") {
		if err = validateContract("Explanation", data); err != nil {
			return err
		}
	}
	return decode(data, out)
}

func metadata(raw json.RawMessage) (resource, error) {
	if err := validateContract("Resource", raw); err != nil {
		return resource{}, err
	}
	var r resource
	if json.Unmarshal(raw, &r) != nil || r.ID == "" || len(r.ID) > 256 || r.Revision < 1 {
		return r, errors.New("protocol: invalid resource identity/revision")
	}
	name := map[string]string{"accounts": "account", "instruments": "instrument", "listings": "listing", "activity": "activity"}[r.Kind]
	var fields map[string]json.RawMessage
	if name == "" || json.Unmarshal(raw, &fields) != nil || len(fields[name]) == 0 || string(fields[name]) == "null" {
		return r, errors.New("protocol: missing canonical body")
	}
	for _, kind := range []string{"account", "instrument", "listing", "activity"} {
		if kind != name && len(fields[kind]) > 0 && string(fields[kind]) != "null" {
			return r, errors.New("protocol: resource has multiple bodies")
		}
	}
	var body map[string]any
	decoder := json.NewDecoder(bytes.NewReader(fields[name]))
	decoder.UseNumber()
	if decoder.Decode(&body) != nil || body == nil {
		return r, errors.New("protocol: invalid body")
	}
	if err := validateScalars(body); err != nil {
		return r, err
	}
	return r, nil
}

var decimal = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)

func validateScalars(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if child != nil && (key == "amount" || key == "multiplier" || key == "coupon" || key == "value" && v["unit"] != nil) {
				text, ok := child.(string)
				if !ok || len(text) > 256 || !decimal.MatchString(text) {
					return errors.New("protocol: invalid exact decimal")
				}
			}
			if key == "currency" && child != nil {
				currency, ok := child.(string)
				if !ok || len(currency) != 3 || strings.Trim(currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
					return errors.New("protocol: invalid currency")
				}
			}
			if err := validateScalars(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := validateScalars(child); err != nil {
				return err
			}
		}
	}
	return nil
}
func validateSnapshot(input snapshot) (state, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return state{}, err
	}
	if err = validateContract("FinancialSnapshot", data); err != nil {
		return state{}, err
	}
	out := state{WarehouseID: input.WarehouseID, Version: input.Version, Resources: map[string]json.RawMessage{}}
	if input.WarehouseID == "" || input.Version == "" || len(input.Version) > 256 || strings.ContainsAny(input.Version, "\"\r\n") || input.Resources == nil {
		return out, errors.New("protocol: incomplete snapshot envelope")
	}
	for _, raw := range input.Resources {
		r, err := metadata(raw)
		if err != nil {
			return out, err
		}
		key := r.Kind + "/" + r.ID
		if _, exists := out.Resources[key]; exists {
			return out, errors.New("protocol: duplicate resource identity")
		}
		out.Resources[key] = append(json.RawMessage(nil), raw...)
	}
	for _, raw := range out.Resources {
		var r map[string]json.RawMessage
		_ = json.Unmarshal(raw, &r)
		var body map[string]any
		var meta resource
		_ = json.Unmarshal(raw, &meta)
		name := map[string]string{"accounts": "account", "instruments": "instrument", "listings": "listing", "activity": "activity"}[meta.Kind]
		_ = json.Unmarshal(r[name], &body)
		if err := validateLinks(body, out.Resources); err != nil {
			return out, err
		}
		if meta.Kind == "activity" {
			listingID, _ := body["listing_id"].(string)
			if listingID != "" {
				var listing struct {
					Listing struct {
						InstrumentID string `json:"instrument_id"`
					} `json:"listing"`
				}
				_ = json.Unmarshal(out.Resources["listings/"+listingID], &listing)
				if body["instrument_id"] != listing.Listing.InstrumentID {
					return out, errors.New("protocol: activity listing belongs to another instrument")
				}
			}
		}
	}
	return out, nil
}

//go:embed mirror.sql
var mirrorSQL string

func statement(name string) string {
	_, tail, ok := strings.Cut(mirrorSQL, "-- name: "+name+"\n")
	if !ok {
		panic("unknown mirror statement")
	}
	sql, _, _ := strings.Cut(tail, "-- name:")
	return sql
}
func openMirror(path string) (*sql.DB, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		err = file.Close()
	} else if errors.Is(err, os.ErrExist) {
		info, e := os.Lstat(path)
		if e != nil {
			return nil, e
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return nil, errors.New("mirror must be a regular private file")
		}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(statement("Initialize")); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
func readState(db *sql.DB) (state, error) {
	var data []byte
	err := db.QueryRow(statement("Read")).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return state{}, nil
	}
	if err != nil {
		return state{}, err
	}
	var stored state
	if err = decode(data, &stored); err != nil || stored.Resources == nil {
		return state{}, errors.New("invalid consumer mirror; preserve it and choose a new state path")
	}
	resources := make([]json.RawMessage, 0, len(stored.Resources))
	for key, raw := range stored.Resources {
		r, e := metadata(raw)
		if e != nil || key != r.Kind+"/"+r.ID {
			return state{}, errors.New("invalid consumer mirror identity")
		}
		resources = append(resources, raw)
	}
	return validateSnapshot(snapshot{stored.WarehouseID, stored.Version, resources})
}
func save(ctx context.Context, db *sql.DB, next state) error {
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, statement("Replace"), data); err != nil {
		return err
	}
	return tx.Commit()
}
func sameJSON(a, b []byte) bool {
	var x, y any
	left, right := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	left.UseNumber()
	right.UseNumber()
	return left.Decode(&x) == nil && right.Decode(&y) == nil && reflect.DeepEqual(x, y)
}
func (c *client) sync(ctx context.Context, current *state, db *sql.DB) (bool, error) {
	data, etag, unchanged, err := c.request(ctx, "/v1/snapshot", current.Version)
	if err != nil || unchanged {
		return false, err
	}
	var input snapshot
	if err = decode(data, &input); err != nil {
		return false, err
	}
	next, err := validateSnapshot(input)
	if err != nil {
		return false, err
	}
	if etag != `"`+next.Version+`"` {
		return false, errors.New("protocol: snapshot version and ETag differ")
	}
	replaced := current.WarehouseID != "" && current.WarehouseID != next.WarehouseID
	if !replaced && current.Version != "" {
		for key, raw := range next.Resources {
			if old, ok := current.Resources[key]; ok {
				before, _ := metadata(old)
				after, _ := metadata(raw)
				if after.Revision < before.Revision || after.Revision == before.Revision && !sameJSON(old, raw) {
					return false, errors.New("protocol: resource revision regressed or changed content")
				}
			}
		}
		if current.Version == next.Version && !reflect.DeepEqual(current.Resources, next.Resources) {
			return false, errors.New("protocol: unchanged version has different resources")
		}
	}
	if err = save(ctx, db, next); err != nil {
		return false, fmt.Errorf("persist mirror: %w", err)
	}
	*current = next
	if replaced {
		fmt.Fprintln(c.stderr, "Warehouse changed; installed a new complete baseline")
	}
	return replaced, nil
}
func (c *client) verifyLookups(ctx context.Context, s state) error {
	for _, raw := range s.Resources {
		expected, err := metadata(raw)
		if err != nil {
			return err
		}
		path := "/v1/" + expected.Kind + "/" + url.PathEscape(expected.ID)
		var current json.RawMessage
		if err = c.get(ctx, path, &current); err != nil {
			return err
		}
		got, err := metadata(current)
		if err != nil || got.ID != expected.ID || got.Kind != expected.Kind || got.Revision < expected.Revision {
			return errors.New("protocol: inconsistent resource lookup")
		}
		if got.Revision == expected.Revision && !sameJSON(raw, current) {
			return errors.New("protocol: revision content differs")
		}
		var why struct {
			ID          string            `json:"id"`
			Kind        string            `json:"kind"`
			Revision    int64             `json:"revision"`
			Selection   string            `json:"selection"`
			Conflict    bool              `json:"conflict"`
			Corrections []json.RawMessage `json:"corrections"`
			Decisions   []json.RawMessage `json:"decisions"`
		}
		if err = c.get(ctx, path+"/explanation?revision="+fmt.Sprint(expected.Revision), &why); err != nil {
			var failure protocolError
			if errors.As(err, &failure) && failure.Code == "stale_revision" {
				continue
			}
			return err
		}
		if why.ID != expected.ID || why.Kind != expected.Kind || why.Revision != expected.Revision || why.Selection == "" || why.Corrections == nil || why.Decisions == nil {
			return errors.New("protocol: inconsistent explanation revision")
		}
	}
	return nil
}

type assertion struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Equals  any    `json:"equals"`
	Matches int    `json:"matches"`
}

func assertions(path string, s state) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var checks []assertion
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&checks); err != nil {
		return err
	}
	for _, check := range checks {
		matches := 0
		for _, raw := range s.Resources {
			r, e := metadata(raw)
			if e != nil {
				return e
			}
			if r.Kind != check.Kind {
				continue
			}
			var value any
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			if e = d.Decode(&value); e != nil {
				return e
			}
			found := true
			for part := range strings.SplitSeq(check.Path, ".") {
				fields, ok := value.(map[string]any)
				if !ok {
					found = false
					break
				}
				value, ok = fields[part]
				if !ok {
					found = false
					break
				}
			}
			if found && reflect.DeepEqual(value, check.Equals) {
				matches++
			}
		}
		if matches != check.Matches {
			return fmt.Errorf(
				"assertion failed: %s %s matched %d, expected %d",
				check.Kind,
				check.Path,
				matches,
				check.Matches,
			)
		}
	}
	return nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) (result error) {
	flags := flag.NewFlagSet("socket-consumer", flag.ContinueOnError)
	flags.SetOutput(stderr)
	socket := flags.String("socket", "", "Wake Unix socket path")
	path := flags.String("state", "", "private SQLite mirror path")
	fixture := flags.String("assert", "", "JSON fixture assertions")
	follow := flags.Bool("follow", false, "synchronize until interrupted")
	interval := flags.Duration("interval", time.Second, "polling interval")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *socket == "" || *path == "" || *interval <= 0 || flags.NArg() != 0 {
		return errors.New("--socket and --state are required; interval must be positive")
	}
	defer func() {
		if *follow && ctx.Err() == context.Canceled && errors.Is(result, context.Canceled) {
			result = nil
		}
	}()
	fmt.Fprintln(stderr, "Connecting to Wake; synchronizing canonical mirror")
	progressCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-progressCtx.Done():
				return
			case <-ticker.C:
				fmt.Fprintln(stderr, "Still synchronizing; waiting for socket work or next follow pass")
			}
		}
	}()
	defer func() { cancel(); <-done }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", *socket)
	}}
	defer transport.CloseIdleConnections()
	c := client{http: &http.Client{Transport: transport, Timeout: 20 * time.Second}, base: "http://wake", stderr: stderr}
	db, err := openMirror(*path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	current, err := readState(db)
	if err != nil {
		return err
	}
	for {
		replaced, e := c.sync(ctx, &current, db)
		if e != nil {
			return e
		}
		if *fixture != "" {
			if e = c.verifyLookups(ctx, current); e != nil {
				return e
			}
		}
		if e = assertions(*fixture, current); e != nil {
			return e
		}
		if e = json.NewEncoder(stdout).Encode(struct {
			Status      string `json:"status"`
			Resources   int    `json:"resources"`
			NewBaseline bool   `json:"new_baseline"`
			Version     string `json:"version"`
		}{"synchronized", len(current.Resources), replaced, current.Version}); e != nil {
			return e
		}
		if !*follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(*interval):
		}
	}
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}{"failed", err.Error()})
		fmt.Fprintln(os.Stderr, "Socket consumer failed:", err)
		os.Exit(1)
	}
}

//go:embed contract.yaml
var contract []byte
var contractOnce sync.Once
var contractDocument *openapi3.T
var contractError error

func validateContract(name string, data []byte) error {
	contractOnce.Do(func() { contractDocument, contractError = openapi3.NewLoader().LoadFromData(contract) })
	if contractError != nil {
		return contractError
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	schema := contractDocument.Components.Schemas[name]
	if schema == nil {
		return errors.New("protocol: missing contract definition")
	}
	if err := schema.Value.VisitJSON(value); err != nil {
		return fmt.Errorf("protocol: response violates %s contract: %w", name, err)
	}
	return nil
}

func validateLinks(value any, resources map[string]json.RawMessage) error {
	switch v := value.(type) {
	case map[string]any:
		for field, child := range v {
			kind := map[string]string{"account_id": "accounts", "instrument_id": "instruments", "listing_id": "listings", "underlier_id": "instruments", "target_id": "activity"}[field]
			if kind != "" && child != nil {
				id, ok := child.(string)
				if !ok || resources[kind+"/"+id] == nil {
					return fmt.Errorf("protocol: missing %s target", field)
				}
			}
			if err := validateLinks(child, resources); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := validateLinks(child, resources); err != nil {
				return err
			}
		}
	}
	return nil
}
