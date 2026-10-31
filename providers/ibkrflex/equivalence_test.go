// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"strings"
	"testing"
)

func TestReportEquivalenceExcludesOnlyReportWindowValues(t *testing.T) {
	original := `<FlexQueryResponse type="TCF" generated="2026-10-20"><FlexStatements><FlexStatement fromDate="2026-10-01" toDate='2026-10-20' note="fromDate='unchanged'"><TradeConfirms><TradeConfirm fromDate="row-date" quantity="10"/></TradeConfirms></FlexStatement></FlexStatements></FlexQueryResponse>`

	digest, err := reportEquivalence([]byte(original))
	if err != nil {
		t.Fatal(err)
	}

	window := strings.NewReplacer("2026-10-01", "2026-10-19", "toDate='2026-10-20'", "toDate='2026-10-21'").
		Replace(original)

	same, err := reportEquivalence([]byte(window))
	if err != nil || same != digest {
		t.Fatal("report window changed equivalence", same, err)
	}

	for _, change := range [][2]string{{`generated="2026-10-20"`, `generated="2026-10-21"`}, {`quantity="10"`, `quantity="12"`}, {`row-date`, `another-date`}, {`unchanged`, `changed`}, {`<TradeConfirms>`, `<TradeConfirms> `}, {`fromDate="2026-10-01" toDate=`, `toDate="2026-10-01" fromDate=`}} {
		changed := strings.Replace(original, change[0], change[1], 1)
		next, e := reportEquivalence([]byte(changed))
		if e != nil || next == digest {
			t.Fatal("nonwindow change lost", change, next, e)
		}
	}
}
