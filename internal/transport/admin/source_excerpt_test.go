// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"strings"
	"testing"
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
