/* SPDX-FileCopyrightText: 2026 trippwill
 * SPDX-License-Identifier: MPL-2.0
 */
(() => {
  const localAddress = (value) => {
    if (
      typeof value !== "string" ||
      !value ||
      value.length > 8192 ||
      /[\\\r\n]/.test(value)
    )
      return null;
    try {
      const url = new URL(value, location.href);
      return url.origin === location.origin ? url.pathname + url.search : null;
    } catch {
      return null;
    }
  };
  const workspaceLinks = [...document.querySelectorAll(".wake-nav-link")];
  const workspaceAddresses = workspaceLinks.map((link) =>
    localAddress(link.href),
  );
  const defaultWorkspace = localAddress(
    workspaceLinks.find((link) => link.getAttribute("aria-current") === "page")
      ?.href || "/activity",
  );
  const resourceKey = (address) => {
    const url = new URL(address, location.href);
    return url.pathname;
  };
  const navigationLabel = (address) => {
    const url = new URL(address, location.href);
    const workspace = workspaceLinks.find(
      (link) => resourceKey(link.href) === resourceKey(address),
    );
    if (workspace) return workspace.textContent.trim();
    if (url.pathname.startsWith("/instruments/")) return "Instrument";
    if (/^\/(reference-)?evidence\//.test(url.pathname))
      return "Supporting evidence";
    if (url.pathname.startsWith("/connections/")) return "Connections";
    if (url.pathname.startsWith("/system/")) return "System";
    return "Activity detail";
  };
  const readNavigation = (value) => {
    if (
      !value ||
      !workspaceAddresses.includes(value.workspace) ||
      !Array.isArray(value.trail) ||
      value.trail.length > 16
    )
      return null;
    const trail = value.trail.map(localAddress);
    return trail.every(Boolean) ? { workspace: value.workspace, trail } : null;
  };
  let navigation = { workspace: defaultWorkspace, trail: [] };
  let navigationRevision = 0;
  let navigationIntent = 0;
  let navigationPending = false;
  let navigationReady = Promise.resolve();
  const canonicalParents = new WeakMap();
  const navigationState = () => ({
    ...history.state,
    wakeNavigation: navigation,
  });
  let navigationAddress = location.href;
  const updateHistory = (url = location.href, push = false) => {
    history[push ? "pushState" : "replaceState"](navigationState(), "", url);
    navigationAddress = location.href;
  };
  const renderNavigation = () => {
    const previous = navigation.trail.at(-1);
    const root =
      navigation.trail.find(
        (address) => resourceKey(address) === resourceKey(navigation.workspace),
      ) || navigation.workspace;
    for (const link of document.querySelectorAll("[data-return]")) {
      if (!link.dataset.defaultReturn) link.dataset.defaultReturn = link.href;
      link.href = previous || link.dataset.defaultReturn;
      link.textContent = `Return to ${navigationLabel(link.href)}`;
    }
    for (const breadcrumbs of document.querySelectorAll("[data-breadcrumbs]")) {
      const workspace = breadcrumbs.querySelector(
        "[data-workspace-breadcrumb] a",
      );
      workspace.href = root;
      workspace.textContent = navigationLabel(root);
      const parent = breadcrumbs.querySelector("[data-parent-breadcrumb]");
      const link = parent.querySelector("a");
      if (!canonicalParents.has(parent))
        canonicalParents.set(parent, {
          hidden: parent.hidden,
          href: link.href,
          label: link.textContent,
        });
      const canonical = canonicalParents.get(parent);
      parent.hidden = previous
        ? resourceKey(previous) === resourceKey(root)
        : canonical.hidden;
      link.href = previous || canonical.href;
      link.textContent = previous ? navigationLabel(previous) : canonical.label;
    }
    if (document.body.hasAttribute("data-contextual-workspace")) {
      for (const link of workspaceLinks) {
        if (localAddress(link.href) === navigation.workspace)
          link.setAttribute("aria-current", "page");
        else link.removeAttribute("aria-current");
      }
    }
  };
  const restoreNavigationEntry = async () => {
    const revision = ++navigationRevision;
    const arrived = localAddress(location.href);
    let restored = readNavigation(history.state?.wakeNavigation);
    try {
      const pending = JSON.parse(
        sessionStorage.getItem("wake.navigation.pending"),
      );
      sessionStorage.removeItem("wake.navigation.pending");
      if (
        !restored &&
        pending?.from === localAddress(document.referrer) &&
        readNavigation(pending.navigation)
      ) {
        let matches = !pending.to || pending.to === arrived;
        const target = localAddress(pending.to);
        const instrumentPath = /^\/instruments\/[^/]+(\/correct)?$/;
        if (
          !matches &&
          target &&
          instrumentPath.test(resourceKey(target)) &&
          instrumentPath.test(resourceKey(arrived)) &&
          resourceKey(target).endsWith("/correct") ===
            resourceKey(arrived).endsWith("/correct")
        ) {
          // Verify alias redirects through the existing authenticated resource route.
          navigationPending = true;
          const response = await fetch(target, {
            method: "HEAD",
            credentials: "same-origin",
            signal: AbortSignal.timeout(8000),
          });
          matches = response.ok && localAddress(response.url) === arrived;
        }
        if (matches) restored = readNavigation(pending.navigation);
      }
    } catch {
      // Storage or redirect-verification failures keep canonical navigation usable.
    }
    if (revision !== navigationRevision) return;
    const workspace = workspaceAddresses.find(
      (address) => resourceKey(address) === resourceKey(arrived),
    );
    navigation = workspace
      ? { workspace, trail: [] }
      : restored || { workspace: defaultWorkspace, trail: [] };
    const self = navigation.trail.findIndex(
      (address) => resourceKey(address) === resourceKey(location.href),
    );
    if (self !== -1) navigation.trail = navigation.trail.slice(0, self);
    updateHistory();
    renderNavigation();
  };
  const restoreNavigation = () => {
    navigationIntent++;
    navigationPending = false;
    const ready = restoreNavigationEntry();
    navigationReady = ready;
    ready.then(() => {
      if (navigationReady === ready) navigationPending = false;
    });
  };
  const rememberNavigation = (target, submission = false) => {
    const destination = localAddress(target);
    if (
      !destination ||
      new URL(destination, location.href).pathname === "/login"
    )
      return;
    navigationRevision++;
    const current = localAddress(location.href);
    let next = {
      workspace: navigation.workspace,
      trail: [...navigation.trail],
    };
    const previous = next.trail.indexOf(destination);
    const canonicalBack =
      !next.trail.length &&
      resourceKey(current).endsWith("/correct") &&
      resourceKey(destination) ===
        resourceKey(current).slice(0, -"/correct".length);
    const workspace = workspaceAddresses.find(
      (address) => resourceKey(address) === resourceKey(destination),
    );
    if (!submission && workspace) next = { workspace, trail: [] };
    else if (!submission && previous !== -1)
      next.trail = next.trail.slice(0, previous);
    else if (
      !submission &&
      !canonicalBack &&
      resourceKey(current) !== resourceKey(destination)
    ) {
      next.trail.push(current);
      if (next.trail.length > 16) next.trail.splice(1, 1);
    }
    try {
      sessionStorage.setItem(
        "wake.navigation.pending",
        JSON.stringify({
          from: current,
          to: submission ? null : destination,
          navigation: next,
        }),
      );
    } catch {
      // Resource links keep their canonical destinations without tab storage.
    }
  };
  restoreNavigation();
  window.addEventListener("pageshow", (event) => {
    if (event.persisted) restoreNavigation();
    else updateHistory();
  });
  const restoreLocation = (event) => {
    const previous = new URL(navigationAddress);
    const current = new URL(location.href);
    const fragment =
      current.pathname === previous.pathname &&
      current.search === previous.search &&
      current.href !== previous.href;
    if (
      fragment ||
      (navigationPending && current.pathname === previous.pathname)
    ) {
      if (fragment) event.wakeFragmentNavigation = true;
      updateHistory();
      renderNavigation();
    } else if (event.type === "popstate") restoreNavigation();
  };
  window.addEventListener("popstate", restoreLocation);
  window.addEventListener("hashchange", restoreLocation);
  document.addEventListener("htmx:afterSettle", renderNavigation);
  document.addEventListener("click", async (event) => {
    const link = event.target.closest("a[href]");
    if (
      !link ||
      event.defaultPrevented ||
      event.button !== 0 ||
      event.ctrlKey ||
      event.metaKey ||
      event.shiftKey ||
      event.altKey ||
      link.hasAttribute("download") ||
      (link.target && link.target !== "_self")
    )
      return;
    const intent = ++navigationIntent;
    if (
      link.href.includes("#") &&
      localAddress(link.href) === localAddress(location.href)
    )
      return;
    const deferred = navigationPending && !!localAddress(link.href);
    if (deferred) {
      event.preventDefault();
      link.setAttribute("aria-busy", "true");
      await navigationReady;
      link.removeAttribute("aria-busy");
      if (intent !== navigationIntent || !link.isConnected) return;
    }
    if (location.pathname === "/activity") {
      const record = link.closest("[data-record]")?.dataset.record;
      if (record) {
        const current = new URL(location.href);
        current.searchParams.set("selected", record);
        updateHistory(current);
      }
    }
    rememberNavigation(link.href);
    if (deferred) location.assign(link.href);
  });
  document.addEventListener("submit", async (event) => {
    const form = event.target;
    if (
      event.defaultPrevented ||
      form.matches("[data-list-filters],[data-observation-filters]")
    )
      return;
    const intent = ++navigationIntent;
    if (navigationPending) {
      event.preventDefault();
      const submitter = event.submitter;
      const inert = form.inert;
      form.inert = true;
      form.setAttribute("aria-busy", "true");
      await navigationReady;
      form.inert = inert;
      form.removeAttribute("aria-busy");
      if (intent !== navigationIntent || !form.isConnected) return;
      if (submitter) form.requestSubmit(submitter);
      else form.requestSubmit();
      return;
    }
    const target = new URL(form.action, location.href);
    if (form.method.toLowerCase() === "get")
      target.search = new URLSearchParams(new FormData(form)).toString();
    rememberNavigation(target.href, form.method.toLowerCase() === "post");
  });

  const toggle = document.querySelector(".wake-menu-toggle");
  const nav = document.querySelector("#main-navigation");
  if (toggle && nav) {
    const narrow = matchMedia("(max-width: 64rem)");
    const setOpen = (open) => {
      nav.hidden = !open;
      toggle.setAttribute("aria-expanded", String(open));
    };
    toggle.hidden = false;
    setOpen(!narrow.matches);
    narrow.addEventListener("change", () => setOpen(!narrow.matches));
    toggle.addEventListener("click", () => setOpen(nav.hidden));
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && narrow.matches && !nav.hidden) {
        setOpen(false);
        toggle.focus();
      }
    });
  }

  document.querySelector("[data-error-summary]")?.focus();
  const contactDetails = document.querySelector(".wake-contact-details");
  if (contactDetails) {
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape")
        contactDetails.dataset.tooltipDismissed = "true";
    });
    const restore = () => {
      delete contactDetails.dataset.tooltipDismissed;
    };
    contactDetails.addEventListener("pointerleave", restore);
    contactDetails.addEventListener("focusout", restore);
  }
  const resetSubmissions = () => {
    for (const form of document.querySelectorAll("form[data-submitting]")) {
      delete form.dataset.submitting;
      form.removeAttribute("aria-busy");
      for (const button of form.querySelectorAll("[data-pending-label]")) {
        button.disabled = false;
        button.textContent = button.dataset.pendingLabel;
        delete button.dataset.pendingLabel;
      }
    }
  };
  document.addEventListener("submit", (event) => {
    const form = event.target;
    if (event.defaultPrevented || form.method.toLowerCase() !== "post") return;
    if (form.dataset.submitting) {
      event.preventDefault();
      return;
    }
    form.dataset.submitting = "true";
    form.setAttribute("aria-busy", "true");
    // Let the browser collect successful controls before disabling submitters.
    setTimeout(() => {
      for (const button of form.querySelectorAll('button[type="submit"]')) {
        button.dataset.pendingLabel = button.textContent;
        button.textContent = "Saving…";
        button.disabled = true;
      }
    }, 0);
  });
  window.addEventListener("pageshow", resetSubmissions);

  for (const form of document.querySelectorAll("[data-correction]")) {
    const scope = document.querySelector("#decision-scope");
    const host = form.querySelector("[data-editor-host]");
    const feedback = form.querySelector("#decision-feedback");
    const submit = form.querySelector('button[type="submit"]');
    const retry = form.querySelector("[data-check-again]");
    const signIn = form.querySelector("[data-correction-signin]");
    const saveLabel = submit.textContent;
    const templates = [...document.querySelectorAll("[data-field-template]")];
    const drafts = new Map();
    let previous = "",
      timer,
      request,
      generation = 0,
      checked = false;
    document.querySelector("[data-field-apply]").hidden = true;

    const message = (text, state = "invalid") => {
      feedback.textContent = text;
      feedback.dataset.state = state;
      const value = form.querySelector("#decision-value");
      if (value)
        value.setAttribute("aria-invalid", String(state === "invalid"));
    };
    const check = () => {
      clearTimeout(timer);
      request?.abort();
      const version = ++generation;
      checked = false;
      submit.disabled = true;
      submit.textContent = saveLabel;
      retry.hidden = true;
      signIn.hidden = true;
      const editor = host.querySelector("[data-decision-editor]");
      if (!editor) return;
      const value = editor.querySelector("#decision-value");
      const currency = editor.querySelector("#decision-currency");
      if (currency) {
        currency.disabled =
          editor.dataset.optional === "true" && value.value === "";
        currency.required = !currency.disabled;
      }
      const invalid = [...form.querySelectorAll("input,select")].find(
        (control) => !control.checkValidity(),
      );
      if (invalid) {
        const hint = editor.querySelector("#decision-help").textContent;
        message(
          invalid === currency
            ? "Enter a three-letter currency code, such as USD."
            : hint,
        );
        return;
      }
      message("Checking this correction…", "checking");
      timer = setTimeout(async () => {
        const controller = new AbortController();
        request = controller;
        const timeout = setTimeout(() => controller.abort(), 8000);
        try {
          const response = await fetch(form.dataset.checkUrl, {
            method: "POST",
            credentials: "same-origin",
            headers: {
              "Content-Type": "application/x-www-form-urlencoded",
              "HX-Request": "true",
            },
            body: new URLSearchParams(new FormData(form)),
            signal: controller.signal,
          });
          if (version !== generation) return;
          if (response.status === 401 || response.status === 403) {
            message("Your session expired. Sign in again before saving.");
            signIn.hidden = false;
            return;
          }
          if (!response.ok) throw new Error("Validation unavailable");
          const result = await response.json();
          if (version !== generation) return;
          checked = result.valid || result.stale;
          submit.disabled = !checked;
          if (result.stale) submit.textContent = "Review updated values";
          message(result.message, result.valid ? "valid" : "invalid");
        } catch {
          if (version !== generation) return;
          message("Could not check this correction. Retry before saving.");
          retry.hidden = false;
        } finally {
          clearTimeout(timeout);
        }
      }, 250);
    };
    const configure = () => {
      const old = host.querySelector("#decision-value");
      if (old && previous)
        drafts.set(previous, {
          value: old.value,
          currency: host.querySelector("#decision-currency")?.value,
        });
      previous = scope.value;
      const template = templates.find(
        (node) => node.dataset.fieldTemplate === scope.value,
      );
      clearTimeout(timer);
      request?.abort();
      ++generation;
      form.hidden = !template;
      host.replaceChildren();
      form.elements.scope.value = scope.value;
      if (!template) {
        const url = new URL(location.href);
        url.searchParams.delete("field");
        updateHistory(url);
        return;
      }
      host.append(template.content.cloneNode(true));
      const editor = host.querySelector("[data-decision-editor]");
      const value = editor.querySelector("#decision-value");
      const currency = editor.querySelector("#decision-currency");
      const draft = drafts.get(scope.value);
      if (draft) {
        value.value = draft.value;
        if (currency) currency.value = draft.currency || "";
      }
      if (editor.dataset.control === "link") {
        editor.querySelector("[data-link-search]").hidden = false;
        const search = editor.querySelector("#decision-link-search");
        const choices = [...value.options].map((option) =>
          option.cloneNode(true),
        );
        const selection = editor.querySelector("[data-link-selection]");
        const showSelection = () => {
          selection.textContent = value.value
            ? `Selected: ${value.selectedOptions[0]?.textContent}`
            : "No retained record selected.";
        };
        search.addEventListener("keydown", (event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            value.focus();
          }
        });
        search.addEventListener("input", () => {
          const selected = value.value,
            query = search.value.toLocaleLowerCase();
          value.replaceChildren(
            ...choices
              .filter(
                (option) =>
                  !option.value ||
                  option.value === selected ||
                  option.textContent.toLocaleLowerCase().includes(query),
              )
              .map((option) => option.cloneNode(true)),
          );
          value.value = selected;
          showSelection();
        });
        value.addEventListener("change", showSelection);
        showSelection();
      }
      if (editor.dataset.control === "timestamp") {
        const group = editor.querySelector("[data-timestamp-fields]");
        const date = group.querySelector("#decision-date"),
          time = group.querySelector("#decision-time"),
          zone = group.querySelector("#decision-zone");
        const parts =
          /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?)(Z|[+-]\d{2}:\d{2})$/.exec(
            value.value,
          );
        const malformed = value.value && !parts;
        if (parts) {
          date.value = parts[1];
          time.value = parts[2];
          zone.value = parts[3];
        }
        value.type = "hidden";
        value.closest("p").hidden = true;
        group.hidden = false;
        if (malformed) {
          const retained = document.createElement("p");
          retained.className = "wake-notice";
          retained.textContent =
            "Retained draft: " +
            value.value +
            ". Choose a date, time and offset to correct it.";
          group.before(retained);
        }
        const sync = (changed) => {
          const populated = date.value || time.value || zone.value;
          for (const part of [date, time, zone])
            part.required = !!populated || (!!malformed && !changed);
          if (changed)
            value.value = populated
              ? `${date.value}T${time.value}${zone.value}`
              : "";
        };
        sync(false);
        group.addEventListener("input", () => sync(true));
        group.addEventListener("change", () => sync(true));
      }
      const url = new URL(location.href);
      url.searchParams.set("field", scope.value);
      updateHistory(url);
      check();
    };
    form.addEventListener("input", (event) => {
      if (event.target.id !== "decision-link-search") check();
    });
    form.addEventListener("change", check);
    form.addEventListener("submit", (event) => {
      if (!checked) {
        event.preventDefault();
        check();
      }
    });
    retry.addEventListener("click", check);
    scope.addEventListener("change", configure);
    document
      .querySelector("[data-field-picker]")
      .addEventListener("submit", (event) => {
        event.preventDefault();
        configure();
      });
    window.addEventListener("pagehide", () => {
      clearTimeout(timer);
      request?.abort();
      ++generation;
    });
    configure();
  }

  const grids = new Map();
  const gridPageSizes = [10, 25, 50, 100, 200];
  const gridPageSize = (value) => {
    const size = typeof value === "string" ? Number(value) : value;
    return gridPageSizes.includes(size) ? size : null;
  };
  const gridColumns = {
    activity: [
      ["label", "Activity"],
      ["date", "Date"],
      ["quantity", "Quantity / currency roles"],
      ["price", "Price / rate"],
      ["amount", "Cash amount"],
      ["realized", "Reported realized P&L"],
      ["state", "Activity state"],
      ["reference", "Reference identity"],
    ],
    instruments: [
      ["name", "Instrument"],
      ["kind", "Kind"],
      ["ticker", "Reported symbol"],
      ["state", "Reference identity"],
    ],
    observations: [
      ["report", "Report"],
      ["outcome", "Outcome"],
      ["location", "Source location"],
      ["rule", "Acceptance rule / version"],
    ],
  };
  const gridDefaultSort = (region) =>
    region.dataset.grid === "activity"
      ? "date"
      : region.dataset.grid === "instruments"
        ? "name"
        : "report";
  const selectedTextWithin = (region) => {
    const selection = document.getSelection();
    if (!selection || selection.isCollapsed) return false;
    for (let i = 0; i < selection.rangeCount; i++)
      if (selection.getRangeAt(i).intersectsNode(region)) return true;
    return false;
  };

  const gridCell = (cell) => {
    const value = cell.getValue() || {};
    const node = document.createElement("div");
    node.className = "wake-grid-cell";
    const text = document.createElement(value.href ? "a" : "span");
    text.textContent = value.text || "";
    if (value.href?.startsWith("/") && !value.href.startsWith("//"))
      text.href = value.href;
    if (value.tone) {
      text.classList.add("wake-state");
      text.dataset.state = value.tone;
    }
    node.append(text);
    if (value.note) {
      const note = document.createElement("span");
      note.className = "wake-help wake-cell-note";
      note.textContent = value.note;
      node.append(note);
    }
    return node;
  };
  const enhance = (root) => {
    for (const region of root.querySelectorAll("[data-grid]")) {
      if (
        grids.has(region) ||
        region.dataset.gridFailed ||
        !region.getClientRects().length
      )
        continue;
      const status = region.querySelector("[data-grid-status]");
      const retry = region.querySelector("[data-grid-retry]");
      const refresh = region.querySelector("[data-grid-refresh]");
      const signIn = region.querySelector("[data-grid-signin]");
      if (typeof window.Tabulator !== "function") {
        status.textContent =
          "The table component could not load. Reload this page to retry.";
        retry.hidden = false;
        retry.onclick = () => location.reload();
        continue;
      }
      try {
        const kind = region.dataset.grid;
        const artifact = region.dataset.gridVariant === "artifact";
        const preferenceKey = (id, type) =>
          `${id}${artifact && type === "columns" ? "-artifact" : ""}-${type}`;
        const emptyState = document.createElement("span");
        emptyState.textContent = "No matching records.";
        const params = new URL(location.href).searchParams;
        const initialSort = params.get("sort") || gridDefaultSort(region);
        const initialDirection =
          params.get("dir") || (kind === "instruments" ? "asc" : "desc");
        const element = region.querySelector("[data-grid-table]");
        const createColumns = () => {
          const definitions = artifact
            ? [
                ["outcome", "Outcome"],
                ["activity", "Related activity"],
                ["location", "Source location"],
                ["rule", "Acceptance rule / version"],
              ]
            : gridColumns[kind];
          const columns = definitions.map(([field, title]) => ({
            field,
            title,
            minWidth: 130,
            widthGrow: artifact && field === "location" ? 2 : 1,
            headerWordWrap: true,
            variableHeight: true,
            formatter: gridCell,
          }));
          return columns;
        };
        const state = {
          table: null,
          check: null,
          displayed: null,
          installing: null,
          available: false,
          retryForeground: false,
          request: null,
          sequence: 0,
          pending: false,
          focus: null,
          selectionRestored: false,
          restoring: false,
        };
        const rememberFocus = () => {
          state.focus = null;
          const active = document.activeElement;
          if (!region.contains(active)) return;
          const heading = active.closest(".tabulator-col");
          const row = active.closest("[data-record]");
          state.focus = heading
            ? { field: heading.getAttribute("tabulator-field") }
            : row && active.matches("a[href]")
              ? {
                  record: row.dataset.record,
                  column: active
                    .closest("[tabulator-field]")
                    ?.getAttribute("tabulator-field"),
                }
              : null;
        };
        const requestData = async (
          url,
          _config,
          parameters,
          background = false,
        ) => {
          if (!background) {
            state.retryForeground = false;
            state.installing = null;
            delete region.dataset.gridReady;
            state.available = false;
            refresh.hidden = true;
            status.textContent = "Loading table…";
          }
          state.request?.abort();
          const sequence = ++state.sequence;
          const controller = new AbortController();
          state.request = controller;
          state.pending = true;
          region.setAttribute("aria-busy", "true");
          retry.hidden = true;
          signIn.hidden = true;
          const target = new URL(url, location.href);
          const current = new URL(location.href);
          for (const key of ["q", "kind", "state"])
            if (current.searchParams.has(key))
              target.searchParams.set(key, current.searchParams.get(key));
          const sort = parameters.sort?.[0];
          if (sort) {
            target.searchParams.set("sort", sort.field);
            target.searchParams.set("dir", sort.dir);
          }
          target.searchParams.set("page", String(parameters.page || 1));
          target.searchParams.set("size", String(parameters.size || 50));
          const resetFilters = region.querySelector(
            "[data-grid-reset-filters]",
          );
          if (resetFilters) {
            const destination = new URL(resetFilters.href, location.href);
            destination.searchParams.set(
              "size",
              target.searchParams.get("size"),
            );
            resetFilters.href = destination.href;
          }
          for (const key of ["sort", "dir", "page", "size"])
            current.searchParams.set(key, target.searchParams.get(key));
          const timeout = setTimeout(() => controller.abort(), 8000);
          try {
            const response = await fetch(target, {
              credentials: "same-origin",
              headers: { Accept: "application/json", "HX-Request": "true" },
              signal: controller.signal,
            });
            if (response.status === 401 || response.status === 403) {
              signIn.hidden = false;
              throw new Error(
                "Your session expired. Sign in again to reload this table.",
              );
            }
            if (!response.ok)
              throw new Error(
                "Table data is unavailable. Previous results remain displayed; retry.",
              );
            const result = await response.json();
            if (
              !Array.isArray(result.data) ||
              !Number.isInteger(result.last_page) ||
              result.last_page < 1 ||
              !Number.isInteger(result.last_row)
            )
              throw new Error(
                "The table response was incomplete. Previous results remain displayed; retry.",
              );
            if (sequence !== state.sequence)
              throw new Error("Superseded table request");
            if (artifact)
              emptyState.textContent =
                result.total === 0
                  ? "No financial observations are attached to this report."
                  : "No matching records.";
            if (!background) {
              state.installing = result;
              updateHistory(current);
            }
            return result;
          } catch (error) {
            if (sequence === state.sequence) {
              state.retryForeground = !background;
              status.textContent = controller.signal.aborted
                ? "Table request timed out. Previous results remain displayed; retry."
                : error.message;
              retry.hidden = false;
            }
            throw error;
          } finally {
            clearTimeout(timeout);
            if (sequence === state.sequence) {
              state.pending = false;
              region.removeAttribute("aria-busy");
            }
          }
        };
        const table = new window.Tabulator(element, {
          index: "id",
          layout: "fitColumns",
          height: "100%",
          dataLoader: false,
          renderVertical: "basic",
          columns: createColumns(),
          pagination: true,
          paginationMode: "remote",
          paginationSize: gridPageSize(params.get("size")) || 50,
          paginationSizeSelector: [...gridPageSizes],
          paginationInitialPage: Number(params.get("page") || 1),
          sortMode: "remote",
          filterMode: "remote",
          initialSort: [{ column: initialSort, dir: initialDirection }],
          columnHeaderSortMulti: false,
          langs: { default: { pagination: { page_size: "Rows per page" } } },
          persistence: { columns: true, page: { size: true, page: false } },
          persistenceID: `wake-${kind}`,
          persistenceMode: "local",
          persistenceReaderFunc: (id, type) => {
            if (type === "page") {
              const size = gridPageSize(params.get("size"));
              if (size) return { paginationSize: size };
            }
            try {
              const saved = JSON.parse(
                localStorage.getItem(preferenceKey(id, type)),
              );
              if (type === "page") {
                const size = gridPageSize(saved?.paginationSize);
                return size ? { paginationSize: size } : false;
              }
              return Array.isArray(saved) ? saved : false;
            } catch {
              return false;
            }
          },
          persistenceWriterFunc: (id, type, data) => {
            if (type === "page" && !gridPageSize(data.paginationSize)) return;
            try {
              localStorage.setItem(
                preferenceKey(id, type),
                JSON.stringify(data),
              );
            } catch {
              // Browser preferences must not block table interactions.
            }
          },
          placeholder: emptyState,
          ajaxURL: region.dataset.gridUrl,
          ajaxRequestFunc: requestData,
          rowFormatter: (row) => {
            row.getElement().dataset.record = row.getData().id;
          },
        });
        state.table = table;
        grids.set(region, state);
        let previousPageSize;
        table.on("pageSizeChanged", (size) => {
          const changed =
            previousPageSize !== undefined && previousPageSize !== size;
          previousPageSize = size;
          if (!changed || state.restoring) return;
          const url = new URL(location.href);
          url.searchParams.delete("selected");
          updateHistory(url);
        });
        const readPage = (background) =>
          requestData(
            region.dataset.gridUrl,
            null,
            {
              page: table.getPage(),
              size: table.getPageSize(),
              sort: table.getSorters(),
            },
            background,
          );
        state.check = async () => {
          if (!state.displayed) return table.setPage(table.getPage());
          const result = await readPage(true);
          state.available =
            JSON.stringify(state.displayed) !== JSON.stringify(result);
          if (!state.available && document.activeElement === refresh)
            element
              .querySelector(".tabulator-col")
              ?.focus({ preventScroll: true });
          refresh.hidden = !state.available;
          const message = state.available
            ? "Updated results are available. Refresh the table to see them."
            : `${state.displayed.last_row} of ${state.displayed.total} results`;
          if (status.textContent !== message) status.textContent = message;
        };
        const refreshTable = async () => {
          const restoreFocus =
            document.activeElement === refresh ||
            document.activeElement === retry;
          const displayed = state.displayed;
          try {
            const result = await readPage(false);
            if (table.getPage() > result.last_page) {
              await table.setPage(result.last_page);
            } else if (JSON.stringify(displayed) !== JSON.stringify(result)) {
              table.setMaxPage(result.last_page);
              await table.replaceData(result.data);
            }
            if (!state.retryForeground) {
              state.installing = null;
              region.dataset.gridReady = "true";
              const message = `${state.displayed.last_row} of ${state.displayed.total} results`;
              if (status.textContent !== message) status.textContent = message;
            }
          } catch {
            // The shared request path retains rows and presents retry/sign-in feedback.
          }
          if (
            restoreFocus &&
            (document.activeElement === document.body ||
              document.activeElement === refresh)
          )
            element
              .querySelector(".tabulator-col")
              ?.focus({ preventScroll: true });
        };
        refresh.onclick = () => {
          if (state.available) refreshTable();
        };
        retry.onclick = () =>
          (state.retryForeground ? refreshTable() : state.check()).catch(
            () => {},
          );
        region.querySelector("[data-grid-reset]").onclick = () =>
          table.setColumns(createColumns());
        // Capture the current focus immediately before Tabulator removes old rows.
        table.on("dataProcessing", rememberFocus);
        table.on("tableBuilt", () => {
          element.setAttribute("aria-label", region.getAttribute("aria-label"));
          const selector = element.querySelector(".tabulator-page-size");
          selector.id = `${kind}-page-size`;
          selector.previousElementSibling.htmlFor = selector.id;
        });
        table.on("renderComplete", () => {
          const sort = table.getSorters()[0];
          for (const heading of element.querySelectorAll(".tabulator-col")) {
            heading.tabIndex = 0;
            heading.setAttribute(
              "aria-sort",
              heading.getAttribute("tabulator-field") === sort?.field
                ? sort.dir === "asc"
                  ? "ascending"
                  : "descending"
                : "none",
            );
          }
        });
        table.on("dataProcessed", (rows) => {
          if (state.installing?.data === rows) {
            state.displayed = state.installing;
            state.installing = null;
            status.textContent = `${state.displayed.last_row} of ${state.displayed.total} results`;
          }
          region.dataset.gridReady = "true";
          if (state.focus?.field && document.activeElement === document.body)
            element
              .querySelector(
                `.tabulator-col[tabulator-field="${CSS.escape(state.focus.field)}"]`,
              )
              ?.focus();
          if (state.focus?.record && document.activeElement === document.body) {
            element
              .querySelector(
                `[data-record="${CSS.escape(state.focus.record)}"] [tabulator-field="${CSS.escape(state.focus.column)}"] a[href]`,
              )
              ?.focus();
          }
          state.focus = null;
          if (!state.selectionRestored && table.getDataCount() > 0) {
            state.selectionRestored = true;
            const selected = new URL(location.href).searchParams.get(
              "selected",
            );
            if (selected && document.activeElement === document.body)
              element
                .querySelector(
                  `[data-record="${CSS.escape(selected)}"] a[href]`,
                )
                ?.focus({ preventScroll: true });
          }
        });
        element.addEventListener("keydown", (event) => {
          const heading = event.target.closest(".tabulator-col");
          if (!heading || heading !== event.target) return;
          const column = table.getColumn(
            heading.getAttribute("tabulator-field"),
          );
          if (event.altKey && ["ArrowLeft", "ArrowRight"].includes(event.key)) {
            event.preventDefault();
            column.setWidth(
              Math.max(
                130,
                column.getWidth() + (event.key === "ArrowRight" ? 24 : -24),
              ),
            );
          } else if (["Enter", " "].includes(event.key)) {
            event.preventDefault();
            heading.click();
          }
        });
      } catch {
        const failed = grids.get(region);
        failed?.request?.abort();
        if (failed) {
          try {
            failed.table.destroy();
          } catch {}
          grids.delete(region);
        }
        region.dataset.gridFailed = "true";
        status.textContent =
          "The table could not initialize. Reload this page to retry.";
        retry.hidden = false;
        retry.onclick = () => location.reload();
      }
    }
  };
  const refreshGrids = () => {
    if (document.hidden) return;
    for (const [region, state] of grids) {
      if (!region.isConnected) {
        state.request?.abort();
        state.table.destroy();
        grids.delete(region);
        continue;
      }
      if (
        !state.pending &&
        !state.retryForeground &&
        region.getClientRects().length
      )
        state.check().catch(() => {});
    }
  };
  enhance(document);
  document.addEventListener("sse:refresh", refreshGrids);
  document.addEventListener(
    "toggle",
    (event) => {
      if (event.target.open) enhance(event.target);
    },
    true,
  );
  document.addEventListener("submit", (event) => {
    const form = event.target.closest(
      "[data-list-filters],[data-observation-filters]",
    );
    if (!form) return;
    event.preventDefault();
    const region =
      form.closest("[data-grid]") || document.querySelector("[data-grid]");
    const state = grids.get(region);
    if (!state) return;
    const url = new URL(location.href);
    for (const [key, value] of new FormData(form))
      url.searchParams.set(key, value);
    url.searchParams.set("page", "1");
    url.searchParams.delete("selected");
    updateHistory(url, true);
    state.table.setData().catch(() => {});
  });

  window.addEventListener("popstate", (event) => {
    if (event.wakeFragmentNavigation) return;
    const params = new URL(location.href).searchParams;
    for (const [region, state] of grids) {
      const form =
        region.querySelector("[data-observation-filters]") ||
        document.querySelector("[data-list-filters]");
      for (const control of form?.querySelectorAll("[name]") || []) {
        if (["q", "kind", "state"].includes(control.name))
          control.value = params.get(control.name) || "";
      }
      const kind = region.dataset.grid;
      const field = params.get("sort") || gridDefaultSort(region);
      const dir =
        params.get("dir") || (kind === "instruments" ? "asc" : "desc");
      state.selectionRestored = false;
      state.restoring = true;
      const size =
        gridPageSize(params.get("size")) ||
        gridPageSize(state.table.getPageSize()) ||
        50;
      if (state.table.getPageSize() !== size)
        state.table.setPageSize(size).catch(() => {});
      state.table.setSort(field, dir);
      state.table.setPage(Number(params.get("page") || 1)).catch(() => {});
      state.restoring = false;
    }
  });

  const focusScrollableTables = () => {
    for (const region of document.querySelectorAll(".wake-table-scroll")) {
      if (region.scrollWidth > region.clientWidth + 1) region.tabIndex = 0;
      else region.removeAttribute("tabindex");
    }
  };
  focusScrollableTables();
  window.addEventListener("resize", focusScrollableTables);
  const dirtyForms = new WeakSet();
  for (const name of ["input", "change"])
    document.addEventListener(name, (event) => {
      const form = event.target.closest("form");
      if (form?.method.toLowerCase() === "post") dirtyForms.add(form);
    });
  const protectedContent = () => {
    const region = document.querySelector("#workspace-content");
    if (!region) return false;
    return (
      region.contains(document.activeElement) ||
      [...region.querySelectorAll("form")].some((form) =>
        dirtyForms.has(form),
      ) ||
      selectedTextWithin(region)
    );
  };
  let disclosures = new Map();
  document.addEventListener("htmx:beforeSwap", (event) => {
    if (event.detail.requestConfig?.elt?.id !== "workspace-content") return;
    if (protectedContent() || document.querySelector("[data-grid]")) {
      event.detail.shouldSwap = false;
      return;
    }
    disclosures = new Map(
      [
        ...document.querySelectorAll(
          "#workspace-content details[data-disclosure]",
        ),
      ].map((element) => [element.dataset.disclosure, element.open]),
    );
  });
  document.addEventListener("htmx:afterSwap", () => {
    for (const element of document.querySelectorAll(
      "#workspace-content details[data-disclosure]",
    ))
      if (disclosures.has(element.dataset.disclosure))
        element.open = disclosures.get(element.dataset.disclosure);
    disclosures.clear();
  });
  document.addEventListener("htmx:afterSettle", () => {
    enhance(document);
    focusScrollableTables();
  });
  let gridPoll = setInterval(refreshGrids, 10000);
  window.addEventListener("pagehide", () => {
    clearInterval(gridPoll);
    for (const state of grids.values()) state.request?.abort();
  });
  document.documentElement.dataset.wakeReady = "true";
  if (
    !document.querySelector('meta[name="wake-csrf-token"]')?.content ||
    !window.htmx
  )
    return;
  const htmx = window.htmx;
  const contact = document.querySelector("#daemon-contact");
  let stream,
    expired = false,
    checking = false,
    poll;
  const setContact = (state, message) => {
    document.body.dataset.liveState = state;
    if (contact && contact.textContent !== message)
      contact.textContent = message;
    let link = document.querySelector("#status-sign-in");
    if (state === "expired" && !link) {
      link = document.createElement("a");
      link.id = "status-sign-in";
      link.href = "/login";
      link.textContent = "Sign in again";
      document.querySelector(".wake-contact")?.append(link);
    }
  };
  const expire = () => {
    expired = true;
    clearInterval(poll);
    clearInterval(gridPoll);
    for (const state of grids.values()) state.request?.abort();
    document.body.removeAttribute("sse-connect");
    stream?.close();
    setContact("expired", "Sign-in required");
  };
  htmx.createEventSource = (url) => {
    stream = new EventSource(url, { withCredentials: true });
    stream.addEventListener("refresh", refreshGrids);
    return stream;
  };
  const checkContact = () => {
    if (checking || expired || document.hidden) return;
    checking = true;
    htmx
      .ajax("GET", "/system/contact", {
        source: document.body,
        target: document.body,
        swap: "none",
      })
      .catch(() => {})
      .finally(() => {
        checking = false;
      });
  };
  document.addEventListener("htmx:configRequest", (event) => {
    if (
      expired ||
      (event.detail.elt?.id === "workspace-content" &&
        (document.hidden || protectedContent()))
    )
      event.preventDefault();
    if (event.detail.elt?.id === "workspace-content")
      event.detail.path = location.pathname + location.search;
  });
  document.addEventListener("htmx:beforeRequest", (event) => {
    if (
      event.detail.elt?.id === "workspace-content" &&
      (document.hidden || protectedContent())
    )
      event.preventDefault();
  });
  document.addEventListener("htmx:afterRequest", (event) => {
    const detail = event.detail,
      xhr = detail.xhr;
    const path = detail.requestConfig?.path || "";
    if (
      xhr.status === 401 ||
      xhr.status === 403 ||
      new URL(xhr.responseURL || location.href, location.href).pathname ===
        "/login"
    ) {
      expire();
      return;
    }
    if (path === "/system/contact") {
      setContact(
        detail.successful ? "connected" : "stale",
        detail.successful
          ? "Daemon connected"
          : "Daemon contact lost · retrying",
      );
      return;
    }
    if (!detail.elt?.closest("#workspace-content")) return;
    let note = document.querySelector("#refresh-issue");
    if (detail.successful) {
      note?.remove();
      return;
    }
    if (!note) {
      note = document.createElement("p");
      note.id = "refresh-issue";
      note.className = "wake-notice";
      note.setAttribute("role", "status");
      note.textContent =
        "Updates delayed. Displayed data may be older; automatic retry continues.";
      document.querySelector("#workspace-content")?.before(note);
    }
  });
  document.addEventListener("htmx:sseError", checkContact);
  document.addEventListener("focusout", () => setTimeout(checkContact, 0));
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) {
      checkContact();
      htmx.trigger(document.querySelector("#workspace-content"), "sse:refresh");
    }
  });
  poll = setInterval(checkContact, 10000);
  checkContact();
  window.addEventListener("pagehide", () => {
    clearInterval(poll);
    stream?.close();
  });
  window.addEventListener("pageshow", (event) => {
    if (event.persisted && !expired) {
      clearInterval(gridPoll);
      gridPoll = setInterval(refreshGrids, 10000);
      refreshGrids();
      htmx.trigger(document.body, "htmx:afterProcessNode");
      for (const node of document.querySelectorAll("[hx-trigger]"))
        htmx.trigger(node, "htmx:afterProcessNode");
      poll = setInterval(checkContact, 10000);
      checkContact();
    }
  });
})();
