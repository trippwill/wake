// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestSourceExcerptPreservesExactEntryAndNearbyBytes(t *testing.T) {
	body := []byte(
		`<R><Statements><S><Trade price="0"/></S><S><Trade price="1"/><Note/><Trade price="2.0000001">&lt;script&gt;</Trade><Trade price="3"/></S></Statements></R>`,
	)

	excerpt := xmlSourceExcerpt(t.Context(), body, "/R[1]/Statements[1]/S[2]/Trade[2]")
	if excerpt.Previous.Text != "<Note/>" ||
		excerpt.Selected.Text != `<Trade price="2.0000001">&lt;script&gt;</Trade>` ||
		excerpt.Next.Text != `<Trade price="3"/>` {
		t.Fatal(excerpt)
	}

	if missing := xmlSourceExcerpt(
		t.Context(),
		body,
		"/R[1]/Statements[1]/S[2]/Trade[4]",
	); missing != (sourceExcerpt{}) {
		t.Fatal("an absent locator matched other source bytes", missing)
	}

	var rendered bytes.Buffer
	if err := SourceExcerptRow(
		"synthetic-report",
		"Selected source row",
		excerpt.Selected,
		true,
	).Render(t.Context(), &rendered); err != nil {
		t.Fatal(err)
	}

	if html := rendered.String(); strings.Contains(html, "<Trade") ||
		!strings.Contains(html, "&amp;lt;script&amp;gt;") {
		t.Fatal("source bytes were interpreted as HTML", html)
	}

	json := jsonSourceExcerpt(
		[]byte(`{"data":[{"price":1.0},{"price":2.00000000000000001},{"price":3}]}`),
		"/data/1",
	)
	if json.Previous.Text != `{"price":1.0}` || json.Selected.Text != `{"price":2.00000000000000001}` ||
		json.Next.Text != `{"price":3}` {
		t.Fatal(json)
	}
}

func TestRetainedSourceExcerptDispatchAndEntryBoundaries(t *testing.T) {
	const (
		flex = `<Report><Transfer id="first"/><Transfer id="last"/><Total/></Report>`
		json = `{"data":[{"price": 1.000},{ "price":2.000 }]}`
	)

	for _, tc := range []struct {
		name, format, body, location string
		previous, selected, next     string
	}{
		{
			"first Flex row", ibkrflex.ConfirmationFormat, flex, "/Report[1]/Transfer[1]", "",
			`<Transfer id="first"/>`, `<Transfer id="last"/>`,
		},
		{
			"cash leg reuses transfer", ibkrflex.ActivityFormat, flex, "/Report[1]/Transfer[2]/cash",
			`<Transfer id="first"/>`, `<Transfer id="last"/>`, "<Total/>",
		},
		{"cash suffix outside transfer", ibkrflex.ActivityFormat, flex, "/Report[1]/Total[1]/cash", "", "", ""},
		{"root element", ibkrflex.ConfirmationFormat, flex, "/Report[1]", "", flex, ""},
		{
			"last Flex row", ibkrflex.ConfirmationFormat, flex, "/Report[1]/Total[1]",
			`<Transfer id="last"/>`, "<Total/>", "",
		},
		{"noncanonical XML index", ibkrflex.ConfirmationFormat, flex, "/Report[1]/Transfer[01]", "", "", ""},
		{"relative XML locator", ibkrflex.ConfirmationFormat, flex, "Report[1]/Transfer[1]", "", "", ""},
		{"incomplete XML entry", ibkrflex.ConfirmationFormat, `<Report><Transfer>`, "/Report[1]/Transfer[1]", "", "", ""},
		{"first JSON row", "snaptrade-json", json, "/data/0", "", `{"price": 1.000}`, `{ "price":2.000 }`},
		{"last JSON row", "snaptrade-json", json, "/data/1", `{"price": 1.000}`, `{ "price":2.000 }`, ""},
		{"noncanonical JSON index", "snaptrade-json", json, "/data/01", "", "", ""},
		{"out of range JSON index", "snaptrade-json", json, "/data/2", "", "", ""},
		{"invalid JSON entry", "snaptrade-json", `{"data":[`, "/data/0", "", "", ""},
		{"unsupported format", "unstructured-text", flex, "/Report[1]", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact, err := provider.NewArtifact(
				tc.format,
				"application/octet-stream",
				time.Unix(1, 0),
				[]byte(tc.body),
			)
			if err != nil {
				t.Fatal(err)
			}

			got := retainedSourceExcerpt(t.Context(), artifact, tc.location)
			want := sourceExcerpt{
				Previous: sourceSnippet{Text: tc.previous},
				Selected: sourceSnippet{Text: tc.selected},
				Next:     sourceSnippet{Text: tc.next},
			}
			if got != want {
				t.Fatalf("excerpt = %#v, want %#v", got, want)
			}
		})
	}
}

func TestSourceExcerptStopsAtUTF8BoundaryAndLinksToFullReport(t *testing.T) {
	const limit = 16 * 1024

	prefix := "<Row>" + strings.Repeat("x", limit-len("<Row>")-1)
	body := []byte("<Report>" + prefix + "€ tail</Row></Report>")
	excerpt := xmlSourceExcerpt(t.Context(), body, "/Report[1]/Row[1]")
	if excerpt.Selected.Text != prefix || !excerpt.Selected.Truncated ||
		!utf8.ValidString(excerpt.Selected.Text) {
		t.Fatal("excerpt split a UTF-8 character or failed to mark the omitted bytes")
	}

	var html strings.Builder
	if err := SourceExcerptRow("synthetic-report", "Selected source row", excerpt.Selected, true).Render(
		t.Context(),
		&html,
	); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(html.String(), "Excerpt limited to 16 KiB") ||
		!strings.Contains(html.String(), `href="/evidence/synthetic-report/original"`) {
		t.Fatal("truncated excerpt did not link to the full report")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if got := xmlSourceExcerpt(ctx, body, "/Report[1]/Row[1]"); got != (sourceExcerpt{}) {
		t.Fatal("a canceled reader returned source content", got)
	}
}
