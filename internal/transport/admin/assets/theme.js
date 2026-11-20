// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

(() => {
  // biome-ignore lint/suspicious/noRedundantUseStrict: Embedded classic scripts are not JavaScript modules.
  "use strict";

  const system = matchMedia("(prefers-color-scheme: dark)");
  const palettes = new Map([
    ["steel", { label: "Steel & Copper", light: "#293039", dark: "#2b3036" }],
    ["reader", { label: "Source Reader", light: "#18342d", dark: "#0f211c" }],
  ]);
  const paletteChoices = [...palettes.keys()];
  const mode = {
    key: "wake.theme",
    attribute: "data-theme-preference",
    value: "system",
    choices: ["system", "light", "dark"],
  };
  const palette = {
    key: "wake.palette",
    attribute: "data-palette-preference",
    value: paletteChoices[0],
    choices: paletteChoices,
  };
  const typography = {
    key: "wake.typography",
    attribute: "data-typography-preference",
    value: "default",
    choices: ["default", "sans", "serif"],
  };
  const preferences = [mode, palette, typography];
  const normalize = (preference, value) =>
    preference.choices.includes(value) ? value : preference.choices[0];

  function readPreferences() {
    for (const preference of preferences) {
      try {
        preference.value = normalize(
          preference,
          localStorage.getItem(preference.key),
        );
      } catch {
        // Storage can be unavailable; keep the current in-page selection.
      }
    }
  }

  function syncControls() {
    for (const preference of preferences) {
      for (const control of document.querySelectorAll(
        `select[${preference.attribute}]`,
      )) {
        if (preference === palette && control.options.length === 0) {
          for (const [key, { label }] of palettes)
            control.add(new Option(label, key));
        }
        control.value = preference.value;
      }
    }
    for (const controls of document.querySelectorAll(
      "[data-appearance-controls]",
    )) {
      controls.hidden = false;
    }
  }

  function apply() {
    const dark =
      mode.value === "dark" || (mode.value === "system" && system.matches);
    const resolvedMode = dark ? "dark" : "light";
    const root = document.documentElement;
    root.dataset.theme = `wake-${resolvedMode}`;
    root.dataset.palette = palette.value;
    root.dataset.typography = typography.value;
    const chrome = document.querySelector('meta[name="theme-color"]');
    if (chrome !== null)
      chrome.content = palettes.get(palette.value)[resolvedMode];
    syncControls();
  }

  // This script runs before stylesheets. Settings markup may arrive later.
  readPreferences();
  apply();
  document.addEventListener("DOMContentLoaded", syncControls, { once: true });
  document.addEventListener("htmx:afterSwap", syncControls);
  window.addEventListener("pageshow", (event) => {
    if (!event.persisted) return;
    readPreferences();
    apply();
  });
  document.addEventListener("change", (event) => {
    const preference = preferences.find((item) =>
      event.target.matches(`select[${item.attribute}]`),
    );
    if (preference === undefined) return;
    preference.value = normalize(preference, event.target.value);
    try {
      localStorage.setItem(preference.key, preference.value);
    } catch {
      // Keep the selection usable even if the browser cannot remember it.
    }
    apply();
  });
  system.addEventListener("change", () => {
    if (mode.value === "system") apply();
  });
  window.addEventListener("storage", (event) => {
    if (event.key !== null && !preferences.some(({ key }) => key === event.key))
      return;
    try {
      if (event.storageArea !== localStorage) return;
    } catch {
      return;
    }
    readPreferences();
    apply();
  });
})();
