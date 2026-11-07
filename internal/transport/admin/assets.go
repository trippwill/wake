// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

//go:embed assetsgzip
var compressedAssets embed.FS

func serveAsset(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Accept-Encoding")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}

	data, err := compressedAssets.ReadFile("assetsgzip/" + name + ".gz")
	if err != nil {
		http.NotFound(w, r)
		return
	}

	encoding := assetEncoding(r.Header.Values("Accept-Encoding"))
	if encoding == "" {
		http.Error(w, "no acceptable asset encoding", http.StatusNotAcceptable)
		return
	}

	if encoding == "gzip" {
		w.Header().Set("Content-Encoding", "gzip")
		// A multipart envelope is not itself gzip data. Serve the complete
		// compressed representation rather than wrap encoded slices in multipart.
		if strings.Contains(r.Header.Get("Range"), ",") {
			r = r.Clone(r.Context())
			r.Header.Del("Range")
		}
	} else {
		reader, e := gzip.NewReader(bytes.NewReader(data))
		if e != nil {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}

		data, err = io.ReadAll(reader)
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}
	}

	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = "text/plain; charset=utf-8"
	}

	cacheControl := "no-cache"
	if strings.HasPrefix(name, "vendor/") {
		cacheControl = "public, max-age=31536000, immutable"
	}

	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(data)))
	http.ServeContent(assetResponseWriter{w}, r, name, time.Time{}, bytes.NewReader(data))
}

type assetResponseWriter struct{ http.ResponseWriter }

func (w assetResponseWriter) WriteHeader(status int) {
	if status >= http.StatusBadRequest {
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Encoding")
		w.Header().Del("ETag")
		w.Header().Set("Cache-Control", "no-store")
	}

	w.ResponseWriter.WriteHeader(status)
}

func assetEncoding(values []string) string {
	qualities := make(map[string]float64)
	for item := range strings.SplitSeq(strings.Join(values, ","), ",") {
		coding, params, err := mime.ParseMediaType(strings.TrimSpace(item))
		if err != nil {
			continue
		}

		quality := 1.0
		if value, ok := params["q"]; ok {
			quality, err = strconv.ParseFloat(value, 64)
			if err != nil || !(quality >= 0 && quality <= 1) {
				quality = 0
			}
		}

		qualities[coding] = quality
	}

	gzipQuality, explicit := qualities["gzip"]
	if !explicit {
		gzipQuality = qualities["*"]
	}

	identityQuality, explicit := qualities["identity"]
	if !explicit {
		identityQuality = 1
		if wildcard, ok := qualities["*"]; ok && wildcard == 0 {
			identityQuality = 0
		}
	}

	if gzipQuality > 0 && gzipQuality >= identityQuality {
		return "gzip"
	}

	if identityQuality > 0 {
		return "identity"
	}

	return ""
}
