// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package financial supplies exact financial values shared by source contracts
// and the financial domain. It has no acquisition, storage or UI dependencies.
package financial

// Error classifies invalid financial values without retaining source payloads.
type Error string

func (e Error) Error() string { return string(e) }
