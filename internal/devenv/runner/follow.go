// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package runner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"syscall"
)

type follower struct {
	adapter    adapterClient
	proc       *supervisedProcess
	proxy      *httptest.Server
	cancel     context.CancelFunc
	finished   chan struct{}
	err        error
	commits    chan publication
	before     publication
	transport  http.RoundTripper
	resume     chan struct{}
	next       http.Handler
	subscribed chan struct{}
}

func (s *acceptanceState) startFollower(
	ctx context.Context,
	adapter adapterClient,
	target *url.URL,
	transport http.RoundTripper,
	view publishedView,
) error {
	follow, err := startFollower(ctx, adapter, target, transport, view)
	if err != nil {
		return err
	}

	s.followers[adapter.profile] = follow

	return nil
}

func eventRelay(target *url.URL, transport http.RoundTripper, subscribed chan struct{}) http.Handler {
	var once sync.Once

	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite:   func(proxy *httputil.ProxyRequest) { proxy.SetURL(target) },
		ModifyResponse: func(r *http.Response) error {
			if r.Request.URL.Path == "/v1/events" && r.StatusCode == http.StatusOK {
				once.Do(func() { close(subscribed) })
			}

			return nil
		},
	}
}

func startFollower(
	ctx context.Context,
	adapter adapterClient,
	target *url.URL,
	transport http.RoundTripper,
	view publishedView,
) (*follower, error) {
	child, cancel := context.WithCancel(ctx)
	f := &follower{
		adapter:    adapter,
		cancel:     cancel,
		finished:   make(chan struct{}),
		commits:    make(chan publication, 4),
		before:     view.publication,
		transport:  transport,
		resume:     make(chan struct{}),
		subscribed: make(chan struct{}),
	}
	first, paused := make(chan struct{}), make(chan struct{})
	old := eventRelay(target, transport, first)
	f.proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-paused:
			select {
			case <-f.resume:
				f.next.ServeHTTP(w, r)
			case <-r.Context().Done():
			}
		default:
			old.ServeHTTP(w, r)
		}
	}))

	var err error

	f.proc, err = adapter.start(child, "follow", f.proxy.URL)
	if err != nil {
		cancel()
		f.proxy.Close()

		return nil, err
	}
	go func() {
		defer close(f.finished)

		f.err = readAdapter(f.proc.stdout, "follow", false, func(event adapterEvent) error {
			if event.Event == "committed" {
				select {
				case f.commits <- event.publication:
				case <-child.Done():
					return child.Err()
				}
			}

			return nil
		})
		if f.err != nil {
			_ = f.proc.stop()

			return
		}

		f.err = f.proc.wait()
	}()

	if err = f.awaitCommit(ctx, view.publication); err == nil {
		err = f.awaitSubscription(ctx, first)
	}

	if err != nil {
		f.close()

		return nil, err
	}

	// Park the reconnect until the next stage finishes starting its daemon.
	close(paused)
	f.proxy.CloseClientConnections()

	return f, nil
}

func (f *follower) awaitCommit(ctx context.Context, want publication) error {
	for {
		select {
		case got := <-f.commits:
			if got == want {
				return nil
			}

			if got != f.before {
				return errors.New("follower committed an unexpected publication")
			}
		case <-f.finished:
			return errors.Join(errors.New("follower exited before committing expected publication"), f.err)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (f *follower) awaitSubscription(ctx context.Context, subscribed <-chan struct{}) error {
	select {
	case <-subscribed:
		return nil
	case <-f.finished:
		return errors.Join(errors.New("follower exited before subscribing"), f.err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *follower) reconnect(ctx context.Context, target *url.URL, view publishedView) error {
	defer f.close()

	f.next = eventRelay(target, f.transport, f.subscribed)
	close(f.resume)

	if err := f.awaitSubscription(ctx, f.subscribed); err != nil {
		return err
	}

	if err := f.awaitCommit(ctx, view.publication); err != nil {
		return err
	}

	if err := f.proc.signal(syscall.SIGINT); err != nil {
		return err
	}

	<-f.finished

	if err := ctx.Err(); err != nil {
		return err
	}

	if f.err != nil {
		return f.err
	}

	return f.adapter.run(ctx, "export", "", view, false)
}

func (f *follower) close() {
	f.cancel()
	<-f.finished
	f.proxy.Close()
}
