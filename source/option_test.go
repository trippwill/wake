// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package source_test

import (
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func TestReportedAdjustedDeliverablesAreImmutable(t *testing.T) {
	ref, _ := source.NewReference("synthetic", "security", "", "adjusted-delivery")
	q, _ := financial.NewQuantity("12.5")
	component, _ := source.NewSecurityDeliverable(ref, q)
	components := []source.Deliverable{component}

	terms, err := source.NewOptionTerms(source.OptionInput{Multiplier: q, Deliverables: components})
	if err != nil {
		t.Fatal(err)
	}

	components[0] = source.Deliverable{}

	fields := terms.Fields()
	fields.Deliverables[0] = source.Deliverable{}
	if terms.Fields().Deliverables[0].Quantity().Value() != "12.5" {
		t.Fatal("mutable deliverable")
	}

	bad, _ := financial.NewQuantity("-1")
	if _, err = source.NewOptionTerms(source.OptionInput{Multiplier: bad}); err == nil {
		t.Fatal("negative multiplier")
	}
}
