// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.trippwill.dev/wake/provider"
)

type node struct {
	name       string
	ordinal    int
	attributes map[string]string
	children   map[string]int
}

// walk checks the entire bounded XML document, including ignored sections.
// Locations are structural paths, not reported identifiers or row contents.
func walk(ctx context.Context, body []byte, visit func([]node) error) error {
	if !utf8.Valid(body) {
		return provider.ErrMalformed
	}

	decoder := xml.NewDecoder(bytes.NewReader(body))

	var stack []node

	root := false
	nodes := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		token, err := decoder.Token()
		if err == io.EOF {
			break
		}

		if err != nil {
			return provider.ErrMalformed
		}

		switch token := token.(type) {
		case xml.StartElement:
			nodes++
			if nodes > 500000 {
				return provider.ErrLimit
			}

			if token.Name.Space != "" {
				return provider.ErrMalformed
			}

			if len(stack) >= 64 || len(token.Attr) > 256 {
				return provider.ErrLimit
			}

			ordinal := 1
			if len(stack) == 0 {
				if root {
					return provider.ErrMalformed
				}

				root = true
			} else {
				parent := &stack[len(stack)-1]
				parent.children[token.Name.Local]++
				ordinal = parent.children[token.Name.Local]
			}

			entry := node{
				name:       token.Name.Local,
				ordinal:    ordinal,
				attributes: make(map[string]string),
				children:   make(map[string]int),
			}
			for _, attribute := range token.Attr {
				if attribute.Name.Space != "" {
					return provider.ErrMalformed
				}

				if _, duplicate := entry.attributes[attribute.Name.Local]; duplicate {
					return provider.ErrMalformed
				}

				entry.attributes[attribute.Name.Local] = attribute.Value
			}

			stack = append(stack, entry)
			if visit != nil {
				if err = visit(stack); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return provider.ErrMalformed
			}

			stack = stack[:len(stack)-1]
		case xml.Directive:
			return provider.ErrMalformed
		case xml.ProcInst:
			if token.Target != "xml" || root {
				return provider.ErrMalformed
			}
		case xml.CharData:
			if len(stack) == 0 && len(bytes.TrimSpace(token)) != 0 {
				return provider.ErrMalformed
			}
		}
	}

	if !root || len(stack) != 0 {
		return provider.ErrMalformed
	}

	return nil
}

type reportMetadata struct {
	lastFrom, lastTo string
	statements       int
	tradeSections    int
}

func inspectReport(
	ctx context.Context,
	body []byte,
	kind string,
	row func([]node) error,
) (reportMetadata, error) {
	metadata := reportMetadata{}
	err := walk(ctx, body, func(path []node) error {
		entry := path[len(path)-1]
		if len(path) == 4 && path[1].name == "FlexStatements" && path[2].name == "FlexStatement" &&
			(kind == "TCF" && entry.name == "TradeConfirms") {
			metadata.tradeSections++
		}

		if len(path) == 1 && (entry.name != "FlexQueryResponse" || entry.attributes["type"] != kind) {
			return provider.ErrMalformed
		}

		if len(path) == 3 && path[1].name == "FlexStatements" && entry.name == "FlexStatement" {
			metadata.statements++
			if metadata.statements > 1024 || !safeText(entry.attributes["accountId"], 1024) {
				return provider.ErrMalformed
			}

			from, fromErr := parseDate(entry.attributes["fromDate"])
			to, toErr := parseDate(entry.attributes["toDate"])
			if fromErr == nil && toErr == nil && from.String() <= to.String() {
				if metadata.lastFrom == "" || from.String() > metadata.lastFrom {
					metadata.lastFrom = from.String()
				}

				if metadata.lastTo == "" || to.String() < metadata.lastTo {
					metadata.lastTo = to.String()
				}
			} else {
				// Missing bounds never prevent retention, but establish no coverage.
				metadata.lastFrom, metadata.lastTo = "invalid", "invalid"
			}
		}

		if len(path) == 5 && path[1].name == "FlexStatements" && path[2].name == "FlexStatement" &&
			path[3].name == "TradeConfirms" &&
			entry.name == "TradeConfirm" &&
			row != nil {
			return row(path)
		}

		return nil
	})
	if err == nil && metadata.statements == 0 {
		err = provider.ErrMalformed
	}

	if metadata.lastFrom == "invalid" || metadata.lastTo == "invalid" || metadata.lastFrom > metadata.lastTo {
		metadata.lastFrom, metadata.lastTo = "", ""
	}

	return metadata, err
}

func location(path []node) string {
	var result strings.Builder
	for _, item := range path {
		fmt.Fprintf(&result, "/%s[%d]", item.name, item.ordinal)
	}

	return result.String()
}

func safeText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}
