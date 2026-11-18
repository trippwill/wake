// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package devfixture owns the synthetic financial reports used to exercise Wake.
package devfixture

import "embed"

//go:embed confirmations/*.xml activity/*.xml
var reports embed.FS

// ReadReport returns a private copy of a synthetic report.
func ReadReport(name string) ([]byte, error) {
	return reports.ReadFile(name)
}
