// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"os"
	"testing"

	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestShellMeasuresWarehouseOnEachReadWithoutBreakingContact(t *testing.T) {
	bytes := int64(1024)
	failure := false

	s := shell{
		status: admin.Status{StartedAt: "synthetic-start", CredentialProtection: true},
		warehouseSize: func() (int64, error) {
			if failure {
				return 0, os.ErrClosed
			}

			return bytes, nil
		},
	}
	for _, size := range []int64{1024, 2048, 0} {
		bytes = size
		status, err := s.Status(t.Context())
		if err != nil || status.WarehouseBytes == nil || *status.WarehouseBytes != size ||
			status.StartedAt != "synthetic-start" ||
			!status.CredentialProtection {
			t.Fatal(status, err)
		}
	}

	failure = true

	status, err := s.Status(t.Context())
	if err != nil || status.WarehouseBytes != nil || !status.CredentialProtection {
		t.Fatal("metadata failure broke daemon status", status, err)
	}
}
