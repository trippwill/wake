// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

(() => {
  // biome-ignore lint/suspicious/noRedundantUseStrict: Embedded classic scripts are not JavaScript modules.
  "use strict";

  const key = "wake.theme";
  const system = matchMedia("(prefers-color-scheme: dark)");
  const normalize = (value) =>
    value === "light" || value === "dark" ? value : "system";
  let preference = "system";
  try {
    preference = normalize(localStorage.getItem(key));
  } catch {
    // Storage can be disabled; the theme still works for this page.
  }

  function apply() {
    const dark =
      preference === "dark" || (preference === "system" && system.matches);
    document.documentElement.dataset.theme = dark ? "wake-dark" : "wake-light";
    const chrome = document.querySelector('meta[name="theme-color"]');
    if (chrome !== null) chrome.content = dark ? "#2b3036" : "#b0b6bc";
    for (const control of document.querySelectorAll(
      "[data-theme-preference]",
    )) {
      control.value = preference;
      control.closest("[data-theme-control]").hidden = false;
    }
  }

  // This script runs before stylesheets so a remembered dark theme is applied
  // before the first paint. Controls are synchronized once their markup exists.
  apply();
  document.addEventListener("DOMContentLoaded", apply, { once: true });
  document.addEventListener("change", (event) => {
    if (!event.target.matches("[data-theme-preference]")) return;
    preference = normalize(event.target.value);
    try {
      localStorage.setItem(key, preference);
    } catch {
      // Keep the selection usable even if the browser cannot remember it.
    }
    apply();
  });
  system.addEventListener("change", () => {
    if (preference === "system") apply();
  });
  window.addEventListener("storage", (event) => {
    if (event.key !== key && event.key !== null) return;
    preference = normalize(event.newValue);
    apply();
  });
})();
