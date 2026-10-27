// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

(() => {
  const toggle = document.querySelector(".wake-menu-toggle");
  const navigation = document.querySelector("#main-navigation");
  if (toggle && navigation) {
    toggle.hidden = false;
    toggle.addEventListener("click", () => {
      const expanded = toggle.getAttribute("aria-expanded") === "true";
      toggle.setAttribute("aria-expanded", String(!expanded));
      navigation.hidden = expanded;
    });
  }
})();

(() => {
  const regions = [...document.querySelectorAll("[data-refresh]")];
  if (!regions.length) return;
  let refreshing = false;
  const refresh = async () => {
    if (refreshing || document.hidden) return;
    refreshing = true;
    try {
      await Promise.all(
        regions.map(async (region) => {
          // Editable forms live outside status regions; keep focused links usable.
          if (region.contains(document.activeElement)) return;
          const response = await fetch(region.dataset.refresh, {
            credentials: "same-origin",
            headers: { Accept: "text/html" },
          });
          if (!response.ok || response.redirected) return;
          const content = await response.text();
          if (region.contains(document.activeElement)) return;
          region.innerHTML = content;
        }),
      );
    } catch {
      // Polling continues through a lost SSE connection or a daemon restart.
    } finally {
      refreshing = false;
    }
  };
  let events = null;
  let poll = null;
  const stop = () => {
    if (poll !== null) clearInterval(poll);
    poll = null;
    events?.close();
    events = null;
  };
  const start = () => {
    stop();
    events = new EventSource("/events");
    events.addEventListener("refresh", refresh);
    poll = setInterval(refresh, 10000);
  };
  start();
  window.addEventListener("pagehide", stop);
  window.addEventListener("pageshow", (event) => {
    if (event.persisted) {
      start();
      refresh();
    }
  });
})();
