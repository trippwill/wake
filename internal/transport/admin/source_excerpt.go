// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

type sourceSnippet struct {
	Text      string
	Truncated bool
}

type sourceExcerpt struct {
	Previous, Selected, Next sourceSnippet
}

func retainedSourceExcerpt(ctx context.Context, artifact provider.Artifact, location string) sourceExcerpt {
	switch artifact.Format() {
	case ibkrflex.ConfirmationFormat, ibkrflex.ActivityFormat:
		return xmlSourceExcerpt(ctx, artifact.Content(), location)
	case "snaptrade-json":
		return jsonSourceExcerpt(artifact.Content(), location)
	default:
		return sourceExcerpt{}
	}
}

type elementOrdinal struct {
	name    string
	ordinal int
}

func sourceElementPath(location string) []elementOrdinal {
	if !strings.HasPrefix(location, "/") {
		return nil
	}

	// Transfer cash movements are a second interpretation of the same Flex entry.
	if base, ok := strings.CutSuffix(location, "/cash"); ok {
		if !strings.HasPrefix(base[strings.LastIndex(base, "/")+1:], "Transfer[") {
			return nil
		}

		location = base
	}

	parts := strings.Split(location[1:], "/")

	path := make([]elementOrdinal, 0, len(parts))
	for _, part := range parts {
		name, index, ok := strings.Cut(part, "[")
		ordinal, err := strconv.Atoi(strings.TrimSuffix(index, "]"))
		if !ok || name == "" || err != nil || ordinal < 1 || index != strconv.Itoa(ordinal)+"]" {
			return nil
		}

		path = append(path, elementOrdinal{name, ordinal})
	}

	return path
}

func xmlSourceExcerpt(ctx context.Context, body []byte, location string) sourceExcerpt {
	path := sourceElementPath(location)
	if len(path) == 0 {
		return sourceExcerpt{}
	}

	decoder := xml.NewDecoder(bytes.NewReader(body))

	type frame struct {
		match, sibling bool
		children       int
		start          int64
	}

	stack := make([]frame, 0, len(path))

	var excerpt sourceExcerpt

	for ctx.Err() == nil {
		offset := decoder.InputOffset()

		token, err := decoder.Token()
		if err != nil {
			return sourceExcerpt{}
		}

		switch t := token.(type) {
		case xml.StartElement:
			depth := len(stack)
			f := frame{start: offset}
			if depth < len(path) && (depth == 0 || stack[depth-1].match) {
				expected := path[depth]
				f.sibling = depth == len(path)-1

				if t.Name.Space == "" && t.Name.Local == expected.name {
					ordinal := 1
					if depth > 0 {
						parent := &stack[depth-1]
						parent.children++
						ordinal = parent.children
					}

					f.match = ordinal == expected.ordinal
				}
			}

			stack = append(stack, f)
		case xml.EndElement:
			depth := len(stack) - 1
			f := stack[depth]
			stack = stack[:depth]

			if f.sibling {
				snippet := limitSourceSnippet(body[f.start:decoder.InputOffset()])
				switch {
				case f.match:
					excerpt.Selected = snippet
					if depth == 0 {
						return excerpt
					}
				case excerpt.Selected.Text == "":
					excerpt.Previous = snippet
				default:
					excerpt.Next = snippet
					return excerpt
				}
			}

			if depth == len(path)-2 && f.match {
				if excerpt.Selected.Text == "" {
					return sourceExcerpt{}
				}

				return excerpt
			}
		}
	}

	return sourceExcerpt{}
}

func jsonSourceExcerpt(body []byte, location string) sourceExcerpt {
	index, ok := strings.CutPrefix(location, "/data/")
	n, err := strconv.Atoi(index)
	if !ok || err != nil || n < 0 || index != strconv.Itoa(n) {
		return sourceExcerpt{}
	}

	var report struct{ Data []json.RawMessage }
	if json.Unmarshal(body, &report) != nil || n >= len(report.Data) {
		return sourceExcerpt{}
	}

	excerpt := sourceExcerpt{Selected: limitSourceSnippet(report.Data[n])}
	if n > 0 {
		excerpt.Previous = limitSourceSnippet(report.Data[n-1])
	}

	if n+1 < len(report.Data) {
		excerpt.Next = limitSourceSnippet(report.Data[n+1])
	}

	return excerpt
}

func limitSourceSnippet(raw []byte) sourceSnippet {
	const limit = 16 * 1024
	if len(raw) <= limit {
		return sourceSnippet{Text: string(raw)}
	}

	end := limit
	for end > 0 && !utf8.RuneStart(raw[end]) {
		end--
	}

	return sourceSnippet{Text: string(raw[:end]), Truncated: true}
}
