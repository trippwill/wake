// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// HTTP work is joined before storage closes, including handlers cancelled after
// the grace period. Closing admission under the same mutex makes Wait/Add safe.
type requestGroup struct {
	mutex   sync.Mutex
	stopped bool
	active  sync.WaitGroup
}

func (group *requestGroup) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		group.mutex.Lock()

		if group.stopped {
			group.mutex.Unlock()
			http.Error(w, "daemon stopping", http.StatusServiceUnavailable)

			return
		}

		group.active.Add(1)
		group.mutex.Unlock()
		defer group.active.Done()

		next.ServeHTTP(w, r)
	})
}

func (group *requestGroup) stop() {
	group.mutex.Lock()
	group.stopped = true
	group.mutex.Unlock()
	group.active.Wait()
}

func boundedReads(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()

			r = r.WithContext(ctx)
		}

		next.ServeHTTP(w, r)
	})
}
