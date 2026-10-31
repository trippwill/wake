// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package host

import (
	"errors"

	"go.trippwill.dev/wake/provider"
)

type FailureCode string

const (
	FailureUnauthorized FailureCode = "unauthorized"
	FailureInvalid      FailureCode = "invalid"
	FailureUnavailable  FailureCode = "unavailable"
	FailureMalformed    FailureCode = "malformed"
	FailureLimit        FailureCode = "limit"
	FailureTemporary    FailureCode = "temporary"
)

func failureCode(err error) FailureCode {
	for _, entry := range []struct {
		err  error
		code FailureCode
	}{
		{provider.ErrUnauthorized, FailureUnauthorized},
		{provider.ErrInvalid, FailureInvalid},
		{provider.ErrUnavailable, FailureUnavailable},
		{provider.ErrMalformed, FailureMalformed},
		{provider.ErrLimit, FailureLimit},
	} {
		if errors.Is(err, entry.err) {
			return entry.code
		}
	}

	return FailureTemporary
}
