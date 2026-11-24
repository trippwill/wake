// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

(() => {
  const mobile = window.matchMedia("screen and (max-width: 768px)");
  const button = document.querySelector(".wake-menu-button");
  const navigation = document.querySelector(".wy-nav-side");
  const content = document.querySelector(".wy-nav-content");
  const search = navigation.querySelector('input[name="q"]');

  navigation.id = "wake-docs-navigation";

  function setOpen(open) {
    navigation.classList.toggle("wake-open", open);
    button.setAttribute("aria-expanded", String(open));
    content.inert = open;
  }

  button.addEventListener("click", () => {
    const open = button.getAttribute("aria-expanded") === "false";
    setOpen(open);
    if (open) {
      search.focus();
    }
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && button.getAttribute("aria-expanded") === "true") {
      setOpen(false);
      button.focus();
    }
  });

  navigation.addEventListener("click", (event) => {
    if (mobile.matches && event.target.closest("a[href]")) {
      setOpen(false);
    }
  });

  mobile.addEventListener("change", () => setOpen(false));
})();
