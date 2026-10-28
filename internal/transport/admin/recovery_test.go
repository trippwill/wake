// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
)

func TestMaintenanceRetryOnlyLatestRebuild(t *testing.T) {
	for _, status := range []string{"complete", "failed", "running"} {
		t.Run(status, func(t *testing.T) {
			var output bytes.Buffer

			view := application.Dashboard{
				Recovery: []application.RecoveryJob{{ID: "latest", Status: status}, {ID: "older", Status: "failed"}},
			}
			if err := MaintenancePage("csrf", view).Render(t.Context(), &output); err != nil {
				t.Fatal(err)
			}

			if strings.Contains(output.String(), `/recovery/older/retry`) {
				t.Fatal("superseded retry offered")
			}

			if strings.Contains(output.String(), `/recovery/latest/retry`) != (status == "failed") {
				t.Fatal("latest retry visibility incorrect")
			}
		})
	}
}
