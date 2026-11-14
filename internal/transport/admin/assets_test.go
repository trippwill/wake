// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func assetTestHandler(t *testing.T) http.Handler {
	t.Helper()

	dir := privateAuthDir(t)
	if err := SetUnlock(dir, []byte("synthetic-asset-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}

	handler, err := NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	return handler
}

func TestCompressedAssetsPreserveEverySource(t *testing.T) {
	handler := assetTestHandler(t)

	err := fs.WalkDir(os.DirFS("assets"), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		original, err := fs.ReadFile(os.DirFS("assets"), name)
		if err != nil {
			return err
		}

		for _, encoding := range []string{"gzip", "identity"} {
			t.Run(name+"/"+encoding, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/assets/"+name, nil)
				r.Header.Set("Accept-Encoding", encoding)

				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)

				if w.Code != http.StatusOK {
					t.Fatal(w.Code, w.Body.String())
				}

				data := w.Body.Bytes()
				if w.Header().Get("Content-Length") != strconv.Itoa(len(data)) {
					t.Fatal("incorrect representation length", w.Header())
				}

				if encoding == "gzip" {
					if w.Header().Get("Content-Encoding") != "gzip" {
						t.Fatal("gzip response missing content encoding")
					}

					reader, err := gzip.NewReader(bytes.NewReader(data))
					if err != nil {
						t.Fatal(err)
					}

					data, err = io.ReadAll(reader)
					closeErr := reader.Close()
					if err != nil || closeErr != nil {
						t.Fatal(err, closeErr)
					}
				} else if w.Header().Get("Content-Encoding") != "" {
					t.Fatal("uncompressed response has content encoding")
				}

				if !bytes.Equal(data, original) {
					t.Fatal("asset differs from source")
				}

				vary := strings.Join(w.Header().Values("Vary"), ",")
				if !strings.Contains(vary, "HX-Request") || !strings.Contains(vary, "Accept-Encoding") {
					t.Fatal("missing cache variation", vary)
				}

				wantCache := "no-cache"
				if strings.HasPrefix(name, "vendor/") {
					wantCache = "public, max-age=31536000, immutable"
				}

				if w.Header().Get("Cache-Control") != wantCache ||
					w.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatal("asset cache or security policy changed", w.Header())
				}
			})
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAssetHTTPRepresentations(t *testing.T) {
	handler := assetTestHandler(t)
	request := func(method, encoding, header, value string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8787/assets/app.css", nil)
		r.Header.Set("Accept-Encoding", encoding)

		if header != "" {
			r.Header.Set(header, value)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		return w
	}

	var previousETag string
	for _, encoding := range []string{"gzip", "identity"} {
		full := request(http.MethodGet, encoding, "", "")
		if full.Code != http.StatusOK || !strings.HasPrefix(full.Header().Get("Content-Type"), "text/css") {
			t.Fatal("CSS response", full.Code, full.Header())
		}

		etag := full.Header().Get("ETag")
		if etag == "" || etag == previousETag {
			t.Fatal("missing representation-specific validator", etag)
		}

		if previousETag != "" &&
			request(http.MethodGet, encoding, "If-None-Match", previousETag).Code != http.StatusOK {
			t.Fatal("different representations share a validator")
		}

		previousETag = etag

		head := request(http.MethodHead, encoding, "", "")
		if head.Code != http.StatusOK || head.Body.Len() != 0 ||
			head.Header().Get("Content-Length") != full.Header().Get("Content-Length") {
			t.Fatal("HEAD differs from GET", head.Code, head.Header())
		}

		unchanged := request(http.MethodGet, encoding, "If-None-Match", etag)
		if unchanged.Code != http.StatusNotModified || unchanged.Body.Len() != 0 {
			t.Fatal("conditional response", unchanged.Code)
		}

		partial := request(http.MethodGet, encoding, "Range", "bytes=0-11")
		if partial.Code != http.StatusPartialContent ||
			!bytes.Equal(partial.Body.Bytes(), full.Body.Bytes()[:12]) ||
			partial.Header().Get("Content-Length") != "12" {
			t.Fatal("range does not address selected representation", partial.Code, partial.Header())
		}

		if encoding == "gzip" {
			multiple := request(http.MethodGet, encoding, "Range", "bytes=0-11,20-31")
			if multiple.Code != http.StatusOK || !bytes.Equal(multiple.Body.Bytes(), full.Body.Bytes()) {
				t.Fatal("gzip multipart request did not receive a complete encoded response", multiple.Code)
			}
		}
	}

	if rejected := request(
		http.MethodGet,
		"gzip;q=0, identity;q=0",
		"",
		"",
	); rejected.Code != http.StatusNotAcceptable {
		t.Fatal("unacceptable encodings admitted", rejected.Code)
	}

	for _, name := range []string{"missing.css", "vendor/missing.js", "", "brand/", "app.js.gz"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/assets/"+name, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("non-asset served", name, w.Code)
		}
	}
}

func TestAssetEncodingNegotiation(t *testing.T) {
	for _, tc := range []struct {
		values []string
		want   string
	}{
		{nil, "identity"},
		{[]string{""}, "identity"},
		{[]string{"gzip, deflate, br"}, "gzip"},
		{[]string{"GZip"}, "gzip"},
		{[]string{"br"}, "identity"},
		{[]string{"gzip;q=0"}, "identity"},
		{[]string{"gzip;q=0.5, identity;q=0.1"}, "gzip"},
		{[]string{"gzip;q=0.1, identity;q=0.5"}, "identity"},
		{[]string{"*"}, "gzip"},
		{[]string{"*;q=0"}, ""},
		{[]string{"*;q=0, identity;q=1"}, "identity"},
		{[]string{"gzip;q=0, *;q=1"}, "identity"},
		{[]string{"gzip;q=1, *;q=0"}, "gzip"},
		{[]string{"gzip;q=NaN, identity;q=0"}, ""},
		{[]string{"gzip;q=invalid, identity;q=0"}, ""},
		{[]string{"gzip;q=2, identity;q=0"}, ""},
		{[]string{"gzip;q=-1, identity;q=0"}, ""},
		{[]string{"br", "gzip;q=0", "identity;q=0"}, ""},
	} {
		if got := assetEncoding(tc.values); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.values, got, tc.want)
		}
	}
}

func TestAssetPreconditionFailuresOverHTTP(t *testing.T) {
	server := httptest.NewServer(assetTestHandler(t))
	defer server.Close()

	transport := &http.Transport{DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)

	client := &http.Client{Transport: transport}
	for _, encoding := range []string{"gzip", "identity"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, tc := range []struct {
				header string
				value  string
				status int
			}{
				{"If-Match", `"different-asset"`, http.StatusPreconditionFailed},
				{"Range", "bytes=999999999-", http.StatusRequestedRangeNotSatisfiable},
			} {
				t.Run(encoding+"/"+method+"/"+tc.header, func(t *testing.T) {
					r, err := http.NewRequest(method, server.URL+"/assets/vendor/daisyui-5.7.50/daisyui.css", nil)
					if err != nil {
						t.Fatal(err)
					}

					r.Host = "127.0.0.1:8787"
					r.Header.Set("Accept-Encoding", encoding)
					r.Header.Set(tc.header, tc.value)

					response, err := client.Do(r)
					if err != nil {
						t.Fatal(err)
					}

					body, readErr := io.ReadAll(response.Body)
					closeErr := response.Body.Close()
					if readErr != nil || closeErr != nil {
						t.Fatal("invalid HTTP response framing", readErr, closeErr)
					}

					if response.StatusCode != tc.status || response.Header.Get("Content-Encoding") != "" ||
						response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("ETag") != "" {
						t.Fatal("asset metadata leaked into error response", response.StatusCode, response.Header)
					}

					if (method == http.MethodHead || tc.status == http.StatusPreconditionFailed) && len(body) != 0 {
						t.Fatal("unexpected precondition or HEAD response body", string(body))
					}
				})
			}
		}
	}
}
