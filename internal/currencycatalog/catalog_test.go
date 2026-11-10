// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package currencycatalog

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshKeepsLastGoodList(t *testing.T) {
	const published = "2026-10-01"

	codes := slices.DeleteFunc(BundledCodes(), func(code string) bool { return code == "EUR" })
	codes = append(codes, "QZZ", "QZZ", "XXX", "XTS")
	good := listXML(published, codes)

	var status atomic.Int64

	var body atomic.Pointer[string]

	status.Store(http.StatusOK)
	body.Store(&good)

	requests := make(chan string, 1)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" ||
			r.Header.Get("Cookie") != "" || r.Referer() != "" {
			t.Error("currency request carried unrelated data")
		}

		requests <- r.Header.Get("If-None-Match")

		w.Header().Set("ETag", `"list-v1"`)
		w.WriteHeader(int(status.Load()))

		if status.Load() == http.StatusOK {
			_, _ = io.WriteString(w, *body.Load())
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), CacheName)
	catalog := Open(path, logger)
	today := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	now := func() time.Time { return today }

	refresh := func(c *Catalog, tag string, want error) {
		t.Helper()

		if err := c.refresh(t.Context(), server.Client(), server.URL, now); !errors.Is(err, want) {
			t.Fatalf("refresh: got %v, want %v", err, want)
		}

		if got := <-requests; got != tag {
			t.Fatalf("If-None-Match = %q, want %q", got, tag)
		}
	}

	if !slices.Equal(catalog.Codes(), BundledCodes()) {
		t.Fatal("missing cache did not use bundled choices")
	}

	refresh(catalog, "", nil)

	got := catalog.Codes()
	if len(got) != len(bundledCodes) || !slices.Contains(got, "QZZ") ||
		slices.Contains(got, "EUR") || slices.Contains(got, "XXX") || slices.Contains(got, "XTS") {
		t.Fatal("wrong refreshed list", got)
	}

	got[0] = "BAD"

	if slices.Contains(catalog.Codes(), "BAD") {
		t.Fatal("caller mutated the catalog")
	}

	previous := catalog.current.Load()
	today = today.Add(interval)

	status.Store(http.StatusNotModified)
	refresh(catalog, `"list-v1"`, nil)

	restarted := Open(path, logger)
	if saved := restarted.current.Load(); saved == nil || !saved.Checked.Equal(today) ||
		!slices.Equal(saved.Codes, previous.Codes) || saved.ETag != previous.ETag {
		t.Fatal("restart did not recover the revalidated cache", saved)
	}

	if got := nextCheck(restarted.current.Load().Checked, today.Add(time.Hour)); got != 23*time.Hour {
		t.Fatal("fresh cache did not defer the next check", got)
	}

	invalidLists := []struct {
		name string
		body string
		want error
	}{
		{"malformed XML", "<ISO_4217>", errInvalidList},
		{"extra document", good + "<wrong/>", errInvalidList},
		{"invalid code", listXML(published, append(codes, "bad")), errInvalidList},
		{"too few codes", listXML(published, []string{"USD"}), errInvalidList},
		{"older publication", listXML("2026-09-18", codes), errOlderList},
		{"oversized", strings.Repeat("x", maxSize+1), errSize},
	}

	status.Store(http.StatusOK)

	today = today.Add(interval)

	for _, tc := range invalidLists {
		t.Run(tc.name, func(t *testing.T) {
			body.Store(&tc.body)
			refresh(restarted, `"list-v1"`, tc.want)
		})
	}

	server.Close()

	if err := restarted.refresh(t.Context(), server.Client(), server.URL, now); !errors.Is(err, errRequest) {
		t.Fatal("outage error", err)
	}

	if saved := Open(path, logger).current.Load(); saved == nil ||
		saved.Published != published || !saved.Checked.Equal(today.Add(-interval)) ||
		!slices.Equal(saved.Codes, restarted.Codes()) {
		t.Fatal("failed refresh replaced the good list", saved)
	}
}

func TestCacheIgnoresCorruptionAndLinks(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	path := filepath.Join(dir, CacheName)
	target := filepath.Join(dir, "other-file")

	if err := os.WriteFile(target, []byte("other data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	if c := Open(path, logger); !slices.Equal(c.Codes(), BundledCodes()) {
		t.Fatal("followed cache symlink")
	}

	if err := saveCache(path, snapshot{}); !errors.Is(err, errCacheWrite) {
		t.Fatal("replaced cache symlink", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(`{"codes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if c := Open(path, logger); !slices.Equal(c.Codes(), BundledCodes()) {
		t.Fatal("corrupt cache displaced the bundled list")
	}
}

func listXML(published string, codes []string) string {
	var out strings.Builder

	fmt.Fprintf(&out, `<?xml version="1.0" encoding="UTF-8"?><ISO_4217 Pblshd="%s"><CcyTbl>`, published)

	for _, code := range codes {
		fmt.Fprintf(&out, "<CcyNtry><Ccy>%s</Ccy></CcyNtry>", code)
	}

	out.WriteString("<CcyNtry><CcyNm>No universal currency</CcyNm></CcyNtry></CcyTbl></ISO_4217>\n")

	return out.String()
}
