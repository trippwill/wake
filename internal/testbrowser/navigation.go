// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

// Package testbrowser contains shared support for real-browser verification.
package testbrowser

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// NewDocument waits for a successful full-document load. Unlike the default
// navigation wait, grid History API updates cannot satisfy its loader-bound wait.
func NewDocument(action chromedp.Action[chromedp.Void]) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		result, err := cdp.Call(ctx, target, page.GetFrameTree, cdp.Empty{})
		if err != nil {
			return err
		}

		frame := result.FrameTree.Frame.ID

		listen, stop := context.WithCancel(ctx)
		defer stop()

		events := make(chan *page.EventLifecycleEvent, 32)

		var response atomic.Pointer[network.Response]

		err = chromedp.Listen(listen, func(event any) {
			switch event := event.(type) {
			case *page.EventLifecycleEvent:
				if event.FrameID == frame && (event.Name == "init" || event.Name == "load") {
					select {
					case events <- event:
					default:
					}
				}
			case *network.EventResponseReceived:
				if event.FrameID == frame && event.Type == network.ResourceTypeDocument {
					response.Store(event.Response)
				}
			}
		})
		if err != nil {
			return err
		}

		if _, err := action(ctx, target); err != nil {
			return err
		}

		loader := ""
		for {
			select {
			case event := <-events:
				if event.Name == "init" {
					loader = string(event.LoaderID)
				}

				if event.Name == "load" && loader != "" && string(event.LoaderID) == loader {
					result := response.Load()
					if result == nil {
						return fmt.Errorf("document navigation produced no HTTP response")
					}

					if result.Status != http.StatusOK {
						return fmt.Errorf("document navigation returned %v: %s", result.Status, result.URL)
					}

					return nil
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
}
