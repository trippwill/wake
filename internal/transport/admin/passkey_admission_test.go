// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestPendingPasskeyCeremoniesReplaceBrowserLogin(t *testing.T) {
	var pending pendingCeremonies

	now := time.Unix(1000, 0)
	first := &ceremony{origin: "https://one.test", browser: "browser A"}
	second := &ceremony{origin: "https://two.test", browser: first.browser}
	other := &ceremony{origin: first.origin, browser: "browser B"}
	firstID := putPasskeyTestCeremony(t, &pending, first, now)
	otherID := putPasskeyTestCeremony(t, &pending, other, now)
	secondID := putPasskeyTestCeremony(t, &pending, second, now)

	if _, err := pending.take(firstID, first.origin, first.browser, now); !errors.Is(err, errCeremonyExpired) {
		t.Fatalf("superseded challenge returned %v", err)
	}

	assertPendingPasskeyCeremony(t, &pending, secondID, second, now)
	assertPendingPasskeyCeremony(t, &pending, otherID, other, now)
}

func TestPendingPasskeyCeremoniesRefillLoginStarts(t *testing.T) {
	var pending pendingCeremonies

	now := time.Unix(1000, 0)
	first := &ceremony{origin: "https://one.test", browser: "browser A"}
	firstID := putPasskeyTestCeremony(t, &pending, first, now)

	for i := 1; i < loginStartBurst; i++ {
		putPasskeyTestCeremony(t, &pending, &ceremony{origin: first.origin, browser: strconv.Itoa(i)}, now)
	}

	for _, offset := range []time.Duration{0, loginStartInterval / 2, loginStartInterval - time.Nanosecond} {
		retry := &ceremony{origin: first.origin, browser: first.browser}
		if _, err := pending.put(retry, now.Add(offset)); !errors.Is(err, errBusyPasskey) {
			t.Fatalf("login admission after %s returned %v", offset, err)
		}
	}

	beforeRefill := now.Add(loginStartInterval - time.Nanosecond)
	management := &ceremony{origin: first.origin, browser: first.browser, session: "session A"}
	putPasskeyTestCeremony(t, &pending, management, beforeRefill)
	assertPendingPasskeyCeremony(t, &pending, firstID, first, beforeRefill)

	refill := now.Add(loginStartInterval)
	next := &ceremony{origin: first.origin, browser: first.browser}
	nextID := putPasskeyTestCeremony(t, &pending, next, refill)
	assertPendingPasskeyCeremony(t, &pending, nextID, next, refill)

	if _, err := pending.put(
		&ceremony{origin: first.origin, browser: "browser B"},
		refill,
	); !errors.Is(
		err,
		errBusyPasskey,
	) {
		t.Fatalf("login admission after consuming the refill returned %v", err)
	}
}

func TestPendingPasskeyCeremoniesReserveManagementCapacity(t *testing.T) {
	var pending pendingCeremonies

	start := time.Unix(1000, 0)
	oldest := &ceremony{origin: "https://one.test", browser: "first"}
	oldestID := putPasskeyTestCeremony(t, &pending, oldest, start)
	second := &ceremony{origin: oldest.origin, browser: "second"}
	now := start.Add(loginStartInterval)
	secondID := putPasskeyTestCeremony(t, &pending, second, now)

	for i := 2; i < maxLoginCeremonies; i++ {
		now = now.Add(loginStartInterval)
		putPasskeyTestCeremony(t, &pending, &ceremony{origin: oldest.origin, browser: strconv.Itoa(i)}, now)
	}

	management := &ceremony{origin: oldest.origin, browser: oldest.browser, session: "session A"}
	managementID := putPasskeyTestCeremony(t, &pending, management, now)

	for i := 1; i < maxCeremonies-maxLoginCeremonies; i++ {
		change := &ceremony{origin: oldest.origin, browser: strconv.Itoa(i), session: "session A"}
		putPasskeyTestCeremony(t, &pending, change, now)
	}

	extra := &ceremony{origin: oldest.origin, browser: "extra", session: "session B"}
	if _, err := pending.put(extra, now); !errors.Is(err, errBusyPasskey) {
		t.Fatalf("management admission at capacity returned %v", err)
	}

	now = now.Add(loginStartInterval)
	newest := &ceremony{origin: oldest.origin, browser: "last"}
	newestID := putPasskeyTestCeremony(t, &pending, newest, now)
	if _, err := pending.take(
		oldestID,
		oldest.origin,
		oldest.browser,
		now,
	); !errors.Is(
		err,
		errCeremonyExpired,
	) {
		t.Fatalf("oldest login challenge returned %v", err)
	}

	if _, err := pending.put(extra, now); !errors.Is(err, errBusyPasskey) {
		t.Fatalf("management admission after login replacement returned %v", err)
	}

	assertPendingPasskeyCeremony(t, &pending, secondID, second, now)
	assertPendingPasskeyCeremony(t, &pending, newestID, newest, now)
	assertPendingPasskeyCeremony(t, &pending, managementID, management, now)
	putPasskeyTestCeremony(t, &pending, extra, now)

	afterExpiry := &ceremony{origin: oldest.origin, browser: "another", session: "session C"}
	putPasskeyTestCeremony(t, &pending, afterExpiry, management.expires)
}

func putPasskeyTestCeremony(t *testing.T, pending *pendingCeremonies, c *ceremony, now time.Time) string {
	t.Helper()

	id, err := pending.put(c, now)
	if err != nil || id == "" {
		t.Fatalf("admission for %q returned id=%q, err=%v", c.browser, id, err)
	}

	return id
}

func assertPendingPasskeyCeremony(
	t *testing.T,
	pending *pendingCeremonies,
	id string,
	want *ceremony,
	now time.Time,
) {
	t.Helper()

	got, err := pending.take(id, want.origin, want.browser, now)
	if err != nil || got != want {
		t.Fatalf("lost challenge for %q: %v", want.browser, err)
	}
}
