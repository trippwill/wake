// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package testbrowser

import (
	"context"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

// Capture saves an action's result when it runs as part of a sequence.
func Capture[T any](destination *T, action chromedp.Action[T]) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		value, err := action(ctx, target)
		if err == nil {
			*destination = value
		}

		return err
	})
}

func Evaluate[T any](
	expression string,
	destination *T,
	options ...chromedp.EvaluateOption,
) chromedp.Action[chromedp.Void] {
	return Capture(destination, chromedp.Evaluate[T](expression, options...))
}

func Poll[T any](
	expression string,
	destination *T,
	options ...chromedp.PollOption,
) chromedp.Action[chromedp.Void] {
	return Capture(destination, chromedp.Poll[T](expression, options...))
}

// Command runs a protocol command as a step in an action sequence.
func Command[P any](command cdp.Command[P, cdp.Empty], params P) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		_, err := cdp.Call(ctx, target, command, params)
		return err
	})
}
