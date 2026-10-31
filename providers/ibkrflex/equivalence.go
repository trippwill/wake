// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"io"
)

// reportEquivalence excludes only the values of fromDate/toDate on report-window
// FlexStatement elements. It preserves every other byte, including attribute
// spelling/order, whitespace, generated timestamps and row-level dates.
// The caller has already validated the complete bounded report.
func reportEquivalence(body []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))

	digest := sha256.New()

	var path []string

	written := 0
	for {
		start := int(decoder.InputOffset())

		token, err := decoder.Token()
		if err == io.EOF {
			break
		}

		if err != nil {
			return "", err
		}

		end := int(decoder.InputOffset())
		switch token := token.(type) {
		case xml.StartElement:
			path = append(path, token.Name.Local)
			if len(path) != 3 || path[0] != "FlexQueryResponse" || path[1] != "FlexStatements" ||
				path[2] != "FlexStatement" {
				continue
			}

			for _, span := range windowValues(body[start:end]) {
				_, _ = digest.Write(body[written : start+span[0]])
				written = start + span[1]
			}
		case xml.EndElement:
			path = path[:len(path)-1]
		}
	}

	_, _ = digest.Write(body[written:])

	return hex.EncodeToString(digest.Sum(nil)), nil
}

func xmlSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }

// windowValues scans validated XML start-tag syntax. Unlike a regular expression,
// it cannot mistake text inside another attribute's quoted value for a field.
func windowValues(tag []byte) [][2]int {
	var spans [][2]int

	i := bytes.IndexByte(tag, '<') + 1
	for i < len(tag) && !xmlSpace(tag[i]) && tag[i] != '>' && tag[i] != '/' {
		i++
	}

	for i < len(tag) {
		for i < len(tag) && xmlSpace(tag[i]) {
			i++
		}

		if i == len(tag) || tag[i] == '/' || tag[i] == '>' {
			break
		}

		name := i
		for i < len(tag) && !xmlSpace(tag[i]) && tag[i] != '=' {
			i++
		}

		key := string(tag[name:i])
		for i < len(tag) && (xmlSpace(tag[i]) || tag[i] == '=') {
			i++
		}

		if i == len(tag) {
			break
		}

		quote := tag[i]
		i++
		value := i
		for i < len(tag) && tag[i] != quote {
			i++
		}

		if key == "fromDate" || key == "toDate" {
			spans = append(spans, [2]int{value, i})
		}

		i++
	}

	return spans
}
