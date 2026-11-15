// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package upstream carries the durable authorization revision across a single
// network operation. Pacing does not change this revision.
package upstream

import "context"

type revisionKey struct{}

func WithRevision(ctx context.Context, revision string) context.Context {
	return context.WithValue(ctx, revisionKey{}, revision)
}

func Revision(ctx context.Context) string {
	revision, _ := ctx.Value(revisionKey{}).(string)
	return revision
}
