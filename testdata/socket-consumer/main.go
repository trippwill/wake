// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// An independent acceptance client. It uses only documented HTTP/JSON over Unix sockets.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

type resource struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Revision int64  `json:"revision"`
}
type change struct {
	Cursor   string          `json:"cursor"`
	Resource json.RawMessage `json:"resource"`
}
type state struct {
	revisions map[resource]json.RawMessage
	dirty     bool
	persisted bool
	Version   int                        `json:"version"`
	Cursor    string                     `json:"cursor"`
	Through   string                     `json:"through"`
	Resources map[string]json.RawMessage `json:"resources"`
	History   []change                   `json:"history"`
}
type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e protocolError) Error() string { return e.Code + ": " + e.Message }

type client struct {
	http   *http.Client
	base   string
	limit  int
	stderr io.Writer
}

func (c *client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("socket transport: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.Header.Get("Content-Type") != "application/json" {
		return errors.New("protocol: expected application/json")
	}
	// Read through EOF so interrupted/chunked responses cannot be accepted as complete pages.
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20+1))
	if err != nil {
		return fmt.Errorf("interrupted response: %w", err)
	}
	if len(data) > 32<<20 {
		return errors.New("protocol: response exceeds bound")
	}
	if response.StatusCode != http.StatusOK {
		var failure protocolError
		if json.Unmarshal(data, &failure) != nil || failure.Code == "" {
			return errors.New("protocol: malformed error")
		}
		return failure
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		return fmt.Errorf("protocol: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("protocol: trailing JSON")
	}
	return nil
}

func metadata(raw json.RawMessage) (resource, error) {
	var r resource
	if json.Unmarshal(raw, &r) != nil || r.ID == "" || r.Revision < 1 {
		return r, errors.New("protocol: invalid resource identity/revision")
	}
	switch r.Kind {
	case "accounts", "instruments", "listings", "activity":
	default:
		return r, errors.New("protocol: invalid resource kind")
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return r, errors.New("protocol: invalid resource")
	}
	name := map[string]string{"accounts": "account", "instruments": "instrument", "listings": "listing", "activity": "activity"}[r.Kind]
	if len(body[name]) == 0 || string(body[name]) == "null" {
		return r, errors.New("protocol: missing canonical body")
	}
	return r, nil
}

func sameJSON(a, b []byte) bool {
	var x, y any
	left, right := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	left.UseNumber()
	right.UseNumber()
	return left.Decode(&x) == nil && right.Decode(&y) == nil && reflect.DeepEqual(x, y)
}

// Resource revisions identify immutable content even when the current mirror is newer.
func retainRevision(index map[resource]json.RawMessage, raw json.RawMessage) (resource, error) {
	r, err := metadata(raw)
	if err != nil {
		return r, err
	}
	if previous, exists := index[r]; exists && !sameJSON(previous, raw) {
		return r, errors.New("protocol: same revision has different content")
	}
	index[r] = raw
	return r, nil
}

func (s *state) indexRevisions() error {
	if s.revisions != nil {
		return nil
	}
	index := make(map[resource]json.RawMessage, len(s.Resources)+len(s.History))
	for _, raw := range s.Resources {
		if _, err := retainRevision(index, raw); err != nil {
			return err
		}
	}
	for _, entry := range s.History {
		if _, err := retainRevision(index, entry.Resource); err != nil {
			return err
		}
	}
	s.revisions = index
	return nil
}

func (s *state) apply(raw json.RawMessage) error {
	if err := s.indexRevisions(); err != nil {
		return err
	}
	r, err := retainRevision(s.revisions, raw)
	if err != nil {
		return err
	}
	key := r.Kind + "/" + r.ID
	if old, ok := s.Resources[key]; ok {
		previous, err := metadata(old)
		if err != nil {
			return err
		}
		if previous.Revision >= r.Revision {
			return nil
		}
	}
	s.Resources[key] = append(json.RawMessage(nil), raw...)
	s.dirty = true
	return nil
}

func save(path string, s state) (result error) {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".wake-consumer-*")
	if err != nil {
		return fmt.Errorf("persist mirror: %w", err)
	}
	temp := file.Name()
	defer func() { _ = os.Remove(temp); _ = file.Close() }()
	if err = file.Chmod(0o600); err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func readState(path string) (state, error) {
	s := state{Version: 1, Resources: map[string]json.RawMessage{}, History: []change{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	var stored struct {
		Version   *int                       `json:"version"`
		Cursor    *string                    `json:"cursor"`
		Through   *string                    `json:"through"`
		Resources map[string]json.RawMessage `json:"resources"`
		History   []change                   `json:"history"`
	}
	if json.Unmarshal(data, &stored) != nil || stored.Version == nil || *stored.Version != 1 || stored.Cursor == nil || *stored.Cursor == "" || stored.Through == nil || stored.Resources == nil || stored.History == nil {
		return s, errors.New("invalid consumer state; preserve it and choose a new state path")
	}
	s = state{Version: *stored.Version, Cursor: *stored.Cursor, Through: *stored.Through, Resources: stored.Resources, History: stored.History}
	for key, raw := range s.Resources {
		r, e := metadata(raw)
		if e != nil || key != r.Kind+"/"+r.ID {
			return s, errors.New("invalid consumer mirror")
		}
	}
	// Replay has already deduplicated persisted history.
	seen := map[string]bool{}
	for _, entry := range s.History {
		recorded, err := metadata(entry.Resource)
		if err != nil || entry.Cursor == "" {
			return s, errors.New("invalid consumer history")
		}
		if seen[entry.Cursor] {
			return s, errors.New("invalid consumer history: repeated change cursor")
		}
		seen[entry.Cursor] = true
		current, ok := s.Resources[recorded.Kind+"/"+recorded.ID]
		mirrored, err := metadata(current)
		if !ok || err != nil || mirrored.Revision < recorded.Revision || (mirrored.Revision == recorded.Revision && !sameJSON(current, entry.Resource)) {
			return s, errors.New("invalid consumer mirror/history consistency")
		}
	}
	if err := s.indexRevisions(); err != nil {
		return s, fmt.Errorf("invalid consumer state: %w", err)
	}
	s.persisted = true
	return s, nil
}

func (c *client) bootstrap(ctx context.Context) (state, error) {
	s := state{Version: 1, Resources: map[string]json.RawMessage{}, History: []change{}}
	var checkpoint struct {
		Cursor string `json:"cursor"`
	}
	if err := c.get(ctx, "/v1/checkpoint", &checkpoint); err != nil {
		return s, err
	}
	if checkpoint.Cursor == "" {
		return s, errors.New("protocol: missing checkpoint")
	}
	s.Cursor = checkpoint.Cursor
	for _, kind := range []string{"accounts", "instruments", "listings", "activity"} {
		cursor := ""
		seen := map[string]bool{}
		for {
			var page struct {
				Items []json.RawMessage `json:"items"`
				Next  *string           `json:"next"`
			}
			if err := c.get(
				ctx,
				"/v1/"+kind+"?limit="+fmt.Sprint(c.limit)+optional("cursor", cursor),
				&page,
			); err != nil {
				return s, err
			}
			if page.Items == nil || len(page.Items) > c.limit {
				return s, errors.New("protocol: invalid resource page")
			}
			for _, raw := range page.Items {
				r, err := metadata(raw)
				if err != nil {
					return s, err
				}
				if r.Kind != kind {
					return s, errors.New("protocol: collection kind mismatch")
				}
				if err = s.apply(raw); err != nil {
					return s, err
				}
			}
			if page.Next == nil {
				break
			}
			if *page.Next == "" || seen[*page.Next] || len(page.Items) == 0 {
				return s, errors.New("protocol: non-progressing page")
			}
			cursor = *page.Next
			seen[cursor] = true
		}
	}
	return s, nil
}

func optional(key, value string) string {
	if value == "" {
		return ""
	}
	return "&" + key + "=" + url.QueryEscape(value)
}

func (c *client) catchup(ctx context.Context, s *state, path string) error {
	var seen map[string]json.RawMessage
	for {
		var page struct {
			Items   []change `json:"items"`
			Cursor  string   `json:"cursor"`
			Through string   `json:"through"`
			More    bool     `json:"more"`
		}
		if err := c.get(
			ctx,
			"/v1/changes?limit="+fmt.Sprint(c.limit)+optional("cursor", s.Cursor)+optional("through", s.Through),
			&page,
		); err != nil {
			return err
		}
		if page.Items == nil || len(page.Items) > c.limit || page.Cursor == "" || page.Through == "" ||
			(s.Through != "" && s.Through != page.Through) ||
			(page.More && (page.Cursor == s.Cursor || len(page.Items) == 0)) ||
			(!page.More && page.Cursor != page.Through) {
			return errors.New("protocol: invalid change page")
		}
		if len(page.Items) > 0 && seen == nil {
			seen = make(map[string]json.RawMessage, len(s.History)+len(page.Items))
			for _, entry := range s.History {
				seen[entry.Cursor] = entry.Resource
			}
		}
		for _, entry := range page.Items {
			if entry.Cursor == "" {
				return errors.New("protocol: missing change cursor")
			}
			if err := s.apply(entry.Resource); err != nil {
				return err
			}
			if previous, duplicate := seen[entry.Cursor]; duplicate {
				if !sameJSON(previous, entry.Resource) {
					return errors.New("protocol: change cursor reused for different content")
				}
			} else {
				s.History = append(s.History, entry)
				s.dirty = true
				seen[entry.Cursor] = entry.Resource
			}
		}
		through := page.Through
		if !page.More {
			through = ""
		}
		if s.Cursor != page.Cursor || s.Through != through {
			s.dirty = true
		}
		s.Cursor, s.Through = page.Cursor, through
		if s.dirty || !s.persisted {
			if err := save(path, *s); err != nil {
				return err
			}
			s.dirty, s.persisted = false, true
		}
		if !page.More {
			return nil
		}
	}
}

func (c *client) sync(ctx context.Context, s *state, path string) (bool, error) {
	rebootstrap := false
	for range 3 {
		if s.Cursor == "" {
			fmt.Fprintln(c.stderr, "Capturing checkpoint and scanning canonical resources")
			baseline, err := c.bootstrap(ctx)
			if err != nil {
				var failure protocolError
				if errors.As(err, &failure) && failure.Code == "cursor_invalidated" {
					rebootstrap = true
					fmt.Fprintln(c.stderr, "Cursor invalidated during scan; starting a fresh baseline")
					continue
				}
				return rebootstrap, err
			}
			// A new mirror only replaces the previous one after its checkpoint has been validated by replay.
			err = c.catchup(ctx, &baseline, path)
			if err == nil {
				*s = baseline
				return rebootstrap, nil
			}
			var failure protocolError
			if errors.As(err, &failure) && failure.Code == "cursor_invalidated" {
				rebootstrap = true
				fmt.Fprintln(c.stderr, "Cursor invalidated during bootstrap; starting a fresh baseline")
				continue
			}
			return rebootstrap, err
		}
		err := c.catchup(ctx, s, path)
		var failure protocolError
		if errors.As(err, &failure) && failure.Code == "cursor_invalidated" {
			rebootstrap = true
			fmt.Fprintln(c.stderr, "Cursor invalidated; explicitly replacing mirror after fresh bootstrap")
			*s = state{Version: 1, Resources: map[string]json.RawMessage{}, History: []change{}}
			continue
		}
		return rebootstrap, err
	}
	return rebootstrap, errors.New("publishing generation repeatedly changed during bootstrap")
}

// Assertion runs also exercise individual lookups and separate explanations through the public contract.
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
		if err != nil {
			return err
		}
		if got.ID != expected.ID || got.Kind != expected.Kind || got.Revision < expected.Revision {
			return errors.New("protocol: inconsistent resource lookup")
		}
		if got.Revision == expected.Revision {
			if err = s.apply(current); err != nil {
				return err
			}
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
		if err = c.get(ctx, path+"/explanation", &why); err != nil {
			return err
		}
		if why.ID != expected.ID || why.Kind != expected.Kind || why.Revision < got.Revision ||
			why.Selection == "" ||
			why.Corrections == nil ||
			why.Decisions == nil {
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
	path := flags.String("state", "", "atomic mirror/cursor state file in an existing directory")
	fixture := flags.String("assert", "", "JSON fixture assertions")
	follow := flags.Bool("follow", false, "synchronize continuously until interrupted")
	interval := flags.Duration("interval", time.Second, "follow polling interval")
	limit := flags.Int("page-size", 100, "bounded page size (1..500)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *socket == "" || *path == "" || *interval <= 0 || *limit < 1 || *limit > 500 || flags.NArg() != 0 {
		return errors.New("--socket and --state are required; interval must be positive and page-size 1..500")
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
	c := client{
		http:   &http.Client{Transport: transport, Timeout: 20 * time.Second},
		base:   "http://wake",
		limit:  *limit,
		stderr: stderr,
	}
	s, err := readState(*path)
	if err != nil {
		return err
	}
	for {
		rebootstrap, err := c.sync(ctx, &s, *path)
		if err != nil {
			return err
		}
		if *fixture != "" {
			if err = c.verifyLookups(ctx, s); err != nil {
				return err
			}
		}
		if err = assertions(*fixture, s); err != nil {
			return err
		}
		if err = json.NewEncoder(stdout).Encode(struct {
			Status      string `json:"status"`
			Resources   int    `json:"resources"`
			Transitions int    `json:"transitions"`
			Rebootstrap bool   `json:"rebootstrap"`
			Cursor      string `json:"cursor"`
		}{"synchronized", len(s.Resources), len(s.History), rebootstrap, s.Cursor}); err != nil {
			return err
		}
		if !*follow {
			return nil
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
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
