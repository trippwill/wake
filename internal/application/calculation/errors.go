// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package calculation

type Error string

func (e Error) Error() string { return string(e) }

const ErrInvalid Error = "invalid account details"
