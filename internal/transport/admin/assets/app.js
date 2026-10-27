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
