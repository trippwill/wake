// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type publication struct {
	WarehouseID string `json:"warehouse_id"`
	Version     string `json:"version"`
}

// Each connection starts with current publication, so reconnect needs no cursor.
func (c *client) follow(ctx context.Context, delay time.Duration, synchronize func(publication) error) error {
	backoff := min(delay, 30*time.Second)
	for {
		var syncError error
		connected := false
		_ = c.events(ctx, func(p publication) error {
			if !connected {
				fmt.Fprintln(c.stderr, "Following accepted publication events")
			}
			connected = true
			syncError = synchronize(p)
			return syncError
		})
		var interruption transportError
		if syncError != nil && !errors.As(syncError, &interruption) {
			return syncError
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connected {
			backoff = min(delay, 30*time.Second)
		}
		fmt.Fprintln(c.stderr, "Publication stream disconnected; reconnecting")
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

func (c *client) events(ctx context.Context, receive func(publication) error) error {
	streamContext, cancel := context.WithCancel(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(streamContext, http.MethodGet, c.base+"/v1/events", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	streamClient := *c.http
	streamClient.Timeout = 0
	// An idle or partial-frame stream cannot leave follow frozen indefinitely.
	idle := time.AfterFunc(45*time.Second, cancel)
	defer idle.Stop()
	response, err := streamClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		return errors.New("protocol: expected publication event stream")
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 1024), 8192)
	var event string
	var data strings.Builder
	frameSize := 0
	for scanner.Scan() {
		line := scanner.Text()
		frameSize += len(line) + 1
		if frameSize > 8192 {
			return errors.New("protocol: publication frame exceeds 8 KiB")
		}
		if line == "" {
			idle.Reset(45 * time.Second)
			if event == "publication" && data.Len() > 0 {
				payload := []byte(data.String())
				if err = validateContract("Publication", payload); err != nil {
					return err
				}
				var p publication
				if err = decode(payload, &p); err != nil {
					return err
				}
				if err = receive(p); err != nil {
					return err
				}
			}
			event = ""
			data.Reset()
			frameSize = 0
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}
