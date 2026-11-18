// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

type adapterRequest struct {
	Endpoint  string `json:"endpoint,omitempty"`
	TokenFile string `json:"token_file,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	Protocol  int    `json:"protocol"`
	Operation string `json:"operation"`
	Profile   string `json:"profile"`
	StateDir  string `json:"state_dir"`
}

func adapter(ctx context.Context, input io.Reader, output io.Writer) error {
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	var req adapterRequest
	if err := decoder.Decode(&req); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF || req.Protocol != 1 || (req.Profile != "history" && req.Profile != "positions") || !filepath.IsAbs(req.StateDir) {
		return errors.New("invalid history export request")
	}
	encoder := json.NewEncoder(output)
	var mu sync.Mutex
	emit := func(v any) error { mu.Lock(); defer mu.Unlock(); return encoder.Encode(v) }
	positionPath := filepath.Join(req.StateDir, "positions.json")
	if req.Operation != "export" {
		if req.Operation != "sync" && req.Operation != "follow" {
			return errors.New("unsupported operation")
		}
		args := []string{"--endpoint", req.Endpoint, "--token-file", req.TokenFile, "--state", filepath.Join(req.StateDir, "mirror.db")}
		var prior positionsView
		if req.Profile == "positions" {
			if req.AccountID == "" {
				return errors.New("missing account")
			}
			data, err := os.ReadFile(positionPath)
			switch {
			case err == nil:
				if err = decode(data, &prior); err != nil {
					return err
				}
				if prior.AccountID != req.AccountID {
					prior = positionsView{}
				}
			case !errors.Is(err, os.ErrNotExist):
				return err
			}
			args = append(args, "--positions-account", req.AccountID)
		}
		if req.Operation == "follow" {
			args = append(args, "--follow")
		}
		if err := emit(map[string]any{"protocol": 1, "event": "progress", "phase": "connecting"}); err != nil {
			return err
		}
		reader, writer := io.Pipe()
		defer reader.Close()
		child, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			e := runWithPositions(child, args, writer, os.Stderr, prior, func(phase string) { _ = emit(map[string]any{"protocol": 1, "event": "progress", "phase": phase}) })
			_ = writer.CloseWithError(e)
			done <- e
		}()
		defer func() { cancel(); _ = reader.Close(); <-done }()
		dec := json.NewDecoder(reader)
		for {
			var raw json.RawMessage
			err := dec.Decode(&raw)
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			var identity struct {
				BaselineID string `json:"baseline_id"`
				Version    string `json:"version"`
			}
			if err = json.Unmarshal(raw, &identity); err != nil {
				return err
			}
			if req.Profile == "positions" {
				if err = os.WriteFile(positionPath+".new", raw, 0600); err != nil {
					return err
				}
				if err = os.Rename(positionPath+".new", positionPath); err != nil {
					return err
				}
			}
			if err = emit(map[string]any{"protocol": 1, "event": "committed", "baseline_id": identity.BaselineID, "version": identity.Version}); err != nil {
				return err
			}
		}
		return emit(map[string]any{"protocol": 1, "event": "complete"})
	}
	if req.Profile == "positions" {
		data, err := os.ReadFile(positionPath)
		if err != nil {
			return err
		}
		return emit(map[string]any{"protocol": 1, "event": "complete", "view": json.RawMessage(data)})
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.Join(req.StateDir, "mirror.db")}).String()
	db, err := sql.Open("sqlite", uri+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	stored, err := readState(db)
	if err != nil {
		return err
	}
	if stored.Version == "" {
		return errors.New("no committed mirror")
	}
	view := snapshot{Mode: "full", BaselineID: stored.BaselineID, Version: stored.Version, Resources: make([]json.RawMessage, 0, len(stored.Resources))}
	for _, key := range slices.Sorted(maps.Keys(stored.Resources)) {
		view.Resources = append(view.Resources, stored.Resources[key])
	}
	return json.NewEncoder(output).Encode(struct {
		Protocol int      `json:"protocol"`
		Event    string   `json:"event"`
		View     snapshot `json:"view"`
	}{1, "complete", view})
}
