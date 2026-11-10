// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package currencycatalog

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"go.trippwill.dev/wake/financial"
)

const (
	listURL        = "https://www.six-group.com/dam/download/financial-information/data-center/iso-currrency/lists/list-one.xml"
	interval       = 24 * time.Hour
	requestTimeout = 10 * time.Second
	maxSize        = 256 << 10
	// The official list has well over 100 codes. Reject partial or wrong lists.
	minCodes = 100
)

type catalogError string

func (e catalogError) Error() string { return string(e) }

const (
	errInvalidList = catalogError("invalid currency list")
	errOlderList   = catalogError("older currency list")
	errRequest     = catalogError("currency list request failed")
	errRead        = catalogError("currency list response unreadable")
	errSize        = catalogError("currency list exceeds size limit")
	errUnmatched   = catalogError("currency list returned 304 without a matching cache")
	errCacheRead   = catalogError("currency list cache unreadable")
	errCacheWrite  = catalogError("currency list cache could not be saved")
)

type snapshot struct {
	Codes     []string  `json:"codes"`
	Published string    `json:"published"`
	ETag      string    `json:"etag,omitempty"`
	Checked   time.Time `json:"checked_at"`
}

type Catalog struct {
	path    string
	logger  *slog.Logger
	current atomic.Pointer[snapshot]
}

// Options allows daemon tests to use synthetic upstreams and clocks.
type Options struct {
	Client *http.Client
	URL    string
	Now    func() time.Time
}

func (c *Catalog) Codes() []string {
	if current := c.current.Load(); current != nil {
		return slices.Clone(current.Codes)
	}

	return BundledCodes()
}

// Run refreshes the catalog until the daemon cancels its work context.
func (c *Catalog) Run(ctx context.Context, options Options) {
	if options.URL == "" {
		options.URL = listURL
	}

	if options.Now == nil {
		options.Now = time.Now
	}

	client := http.Client{Timeout: requestTimeout, CheckRedirect: refuseRedirect}
	if options.Client != nil {
		client.Transport = options.Client.Transport
	}

	delay := time.Duration(0)
	if current := c.current.Load(); current != nil {
		delay = nextCheck(current.Checked, options.Now())
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			err := c.refresh(ctx, &client, options.URL, options.Now)
			if err != nil && ctx.Err() == nil {
				c.logger.WarnContext(
					ctx,
					"currency catalog refresh failed",
					"event",
					"currency_catalog_refresh_failed",
					"reason",
					err.Error(),
				)
			}

			timer.Reset(interval)
		}
	}
}

func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func nextCheck(checked, now time.Time) time.Duration {
	elapsed := now.Sub(checked)
	if elapsed < 0 || elapsed >= interval {
		return 0
	}

	return interval - elapsed
}

func (c *Catalog) refresh(ctx context.Context, client *http.Client, url string, now func() time.Time) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errRequest
	}

	current := c.current.Load()
	if current != nil && current.ETag != "" {
		request.Header.Set("If-None-Match", current.ETag)
	}

	response, err := client.Do(request)
	if err != nil {
		return errRequest
	}
	defer func() { _ = response.Body.Close() }()

	var next snapshot
	switch response.StatusCode {
	case http.StatusNotModified:
		if current == nil || current.ETag == "" {
			return errUnmatched
		}

		next = *current
	case http.StatusOK:
		body, err := readBounded(response.Body)
		if err != nil {
			return err
		}

		next, err = parseList(body)
		if err != nil {
			return err
		}

		if next.Published < bundledPublication || (current != nil && next.Published < current.Published) {
			return errOlderList
		}

		next.ETag = validETag(response.Header.Get("ETag"))
	default:
		return fmt.Errorf("currency list returned HTTP %d", response.StatusCode)
	}

	next.Checked = now().UTC()

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if c.path != "" {
		if err := saveCache(c.path, next); err != nil {
			return errCacheWrite
		}
	}

	c.current.Store(&next)

	if response.StatusCode == http.StatusOK && (current == nil || current.Published != next.Published ||
		!slices.Equal(current.Codes, next.Codes)) {
		c.logger.InfoContext(ctx, "currency catalog refreshed", "event", "currency_catalog_refreshed",
			"published", next.Published, "codes", len(next.Codes))
	}

	return nil
}

func readBounded(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxSize+1))
	if err != nil {
		return nil, errRead
	}

	if len(body) > maxSize {
		return nil, errSize
	}

	return body, nil
}

func parseList(body []byte) (snapshot, error) {
	var list struct {
		XMLName   xml.Name `xml:"ISO_4217"`
		Published string   `xml:"Pblshd,attr"`
		Entries   []struct {
			Code *string `xml:"Ccy"`
		} `xml:"CcyTbl>CcyNtry"`
	}

	decoder := xml.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&list); err != nil || len(bytes.TrimSpace(body[decoder.InputOffset():])) != 0 {
		return snapshot{}, errInvalidList
	}

	var codes []string
	for _, entry := range list.Entries {
		if entry.Code == nil || *entry.Code == "XXX" || *entry.Code == "XTS" {
			continue
		}

		codes = append(codes, *entry.Code)
	}

	slices.Sort(codes)

	next := snapshot{Published: list.Published, Codes: slices.Compact(codes)}
	if !validList(next) {
		return snapshot{}, errInvalidList
	}

	return next, nil
}

func validList(list snapshot) bool {
	if _, err := financial.ParseDate(list.Published); err != nil || len(list.Codes) < minCodes {
		return false
	}

	for i, code := range list.Codes {
		if _, err := financial.ParseCurrency(code); err != nil || code == "XTS" || code == "XXX" ||
			(i > 0 && list.Codes[i-1] >= code) {
			return false
		}
	}

	return true
}

func validETag(value string) string {
	tag := strings.TrimPrefix(value, "W/")
	if len(tag) < 2 || len(tag) > 1024 || tag[0] != '"' || tag[len(tag)-1] != '"' ||
		strings.ContainsFunc(tag[1:len(tag)-1], func(r rune) bool { return r <= ' ' || r == '"' || r == 127 }) {
		return ""
	}

	return value
}
