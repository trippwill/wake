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

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// NewDocument waits for a successful full-document load. Unlike the default
// navigation wait, grid History API updates cannot satisfy its loader-bound wait.
func NewDocument(action chromedp.Action) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		tree, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}

		listen, stop := context.WithCancel(ctx)
		defer stop()

		events := make(chan *page.EventLifecycleEvent, 32)

		var response atomic.Pointer[network.Response]
		chromedp.ListenTarget(listen, func(event any) {
			switch event := event.(type) {
			case *page.EventLifecycleEvent:
				if event.FrameID == tree.Frame.ID && (event.Name == "init" || event.Name == "load") {
					select {
					case events <- event:
					default:
					}
				}
			case *network.EventResponseReceived:
				if event.FrameID == tree.Frame.ID && event.Type == network.ResourceTypeDocument {
					response.Store(event.Response)
				}
			}
		})

		if err := action.Do(ctx); err != nil {
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
