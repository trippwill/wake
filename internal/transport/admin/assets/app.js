/* SPDX-FileCopyrightText: 2026 trippwill
 * SPDX-License-Identifier: MPL-2.0
 */
(() => {
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
        history.replaceState(null, "", url);
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
      history.replaceState(null, "", url);
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
  const gridColumns = {
    activity: [
      ["label", "Activity"],
      ["date", "Date"],
      ["quantity", "Quantity / currency roles"],
      ["price", "Price / rate"],
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
  const gridParameter = (region, name) =>
    region.dataset.grid === "observations" ? `observations_${name}` : name;
  const selectedTextWithin = (region) => {
    const selection = document.getSelection();
    if (!selection || selection.isCollapsed) return false;
    for (let i = 0; i < selection.rangeCount; i++)
      if (selection.getRangeAt(i).intersectsNode(region)) return true;
    return false;
  };
  const selectionMessage = "Table refresh paused while text is selected.";
  const waitForGridSelection = (region, signal) => {
    if (!selectedTextWithin(region)) return Promise.resolve();
    return new Promise((resolve, reject) => {
      const cleanup = () => {
        document.removeEventListener("selectionchange", changed);
        signal.removeEventListener("abort", aborted);
      };
      const changed = () => {
        if (!selectedTextWithin(region)) {
          cleanup();
          resolve();
        }
      };
      const aborted = () => {
        cleanup();
        reject(signal.reason);
      };
      document.addEventListener("selectionchange", changed);
      signal.addEventListener("abort", aborted, { once: true });
      if (signal.aborted) aborted();
      else changed();
    });
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
        const params = new URL(location.href).searchParams;
        const initialSort =
          params.get(gridParameter(region, "sort")) ||
          (kind === "activity"
            ? "date"
            : kind === "instruments"
              ? "name"
              : "report");
        const initialDirection =
          params.get(gridParameter(region, "dir")) ||
          (kind === "instruments" ? "asc" : "desc");
        const element = region.querySelector("[data-grid-table]");
        const createColumns = () =>
          gridColumns[kind].map(([field, title]) => ({
            field,
            title,
            minWidth: 130,
            headerWordWrap: true,
            variableHeight: true,
            formatter: gridCell,
          }));
        const state = {
          table: null,
          request: null,
          sequence: 0,
          pending: false,
          focus: null,
          selectionRestored: false,
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
        const table = new window.Tabulator(element, {
          index: "id",
          layout: "fitColumns",
          maxHeight: "32rem",
          dataLoader: false,
          renderVertical: "basic",
          columns: createColumns(),
          pagination: true,
          paginationMode: "remote",
          paginationSize: 50,
          paginationInitialPage: Number(
            params.get(gridParameter(region, "page")) || 1,
          ),
          sortMode: "remote",
          filterMode: "remote",
          initialSort: [{ column: initialSort, dir: initialDirection }],
          columnHeaderSortMulti: false,
          persistence: { columns: true },
          persistenceID: `wake-${kind}`,
          persistenceMode: "local",
          persistenceReaderFunc: (id, type) => {
            try {
              const saved = JSON.parse(localStorage.getItem(`${id}-${type}`));
              return Array.isArray(saved) ? saved : false;
            } catch {
              return false;
            }
          },
          persistenceWriterFunc: (id, type, data) => {
            try {
              localStorage.setItem(`${id}-${type}`, JSON.stringify(data));
            } catch {
              // Layout preferences must not block table interactions.
            }
          },
          placeholder: "No matching records.",
          ajaxURL: region.dataset.gridUrl,
          ajaxRequestFunc: async (url, _config, parameters) => {
            delete region.dataset.gridReady;
            state.request?.abort();
            const sequence = ++state.sequence;
            const controller = new AbortController();
            state.request = controller;
            state.pending = true;
            region.setAttribute("aria-busy", "true");
            status.textContent = "Loading table…";
            retry.hidden = true;
            signIn.hidden = true;
            const target = new URL(url, location.href);
            const current = new URL(location.href);
            for (const key of ["q", "kind", "state"])
              if (current.searchParams.has(gridParameter(region, key)))
                target.searchParams.set(
                  key,
                  current.searchParams.get(gridParameter(region, key)),
                );
            const sort = parameters.sort?.[0];
            if (sort) {
              target.searchParams.set("sort", sort.field);
              target.searchParams.set("dir", sort.dir);
            }
            target.searchParams.set("page", String(parameters.page || 1));
            target.searchParams.set("size", String(parameters.size || 50));
            for (const key of ["sort", "dir", "page"])
              current.searchParams.set(
                gridParameter(region, key),
                target.searchParams.get(key),
              );
            if (current.searchParams.has("origin"))
              target.searchParams.set(
                "origin",
                current.searchParams.get("origin"),
              );
            target.searchParams.set(
              "page_url",
              current.pathname + current.search,
            );
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
              if (selectedTextWithin(region)) {
                status.textContent = selectionMessage;
                clearTimeout(timeout);
                await waitForGridSelection(region, controller.signal);
                if (sequence !== state.sequence)
                  throw new Error("Superseded table request");
              }
              status.textContent = `${result.last_row} of ${result.total} results`;
              history.replaceState(null, "", current);
              return result;
            } catch (error) {
              if (sequence === state.sequence) {
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
          },
          rowFormatter: (row) => {
            row.getElement().dataset.record = row.getData().id;
          },
        });
        state.table = table;
        grids.set(region, state);
        retry.onclick = () => table.setPage(table.getPage()).catch(() => {});
        region.querySelector("[data-grid-reset]").onclick = () =>
          table.setColumns(createColumns());
        // Capture the current focus immediately before Tabulator removes old rows.
        table.on("dataProcessing", rememberFocus);
        table.on("tableBuilt", () => {
          element.setAttribute("aria-label", region.getAttribute("aria-label"));
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
        table.on("dataProcessed", () => {
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
      if (selectedTextWithin(region)) {
        region.querySelector("[data-grid-status]").textContent =
          selectionMessage;
        continue;
      }
      if (!state.pending && region.getClientRects().length)
        state.table.setPage(state.table.getPage()).catch(() => {});
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
      url.searchParams.set(gridParameter(region, key), value);
    url.searchParams.set(gridParameter(region, "page"), "1");
    url.searchParams.delete("selected");
    history.pushState(null, "", url);
    state.table.setData().catch(() => {});
  });

  window.addEventListener("popstate", () => {
    const params = new URL(location.href).searchParams;
    for (const [region, state] of grids) {
      const form =
        region.querySelector("[data-observation-filters]") ||
        document.querySelector("[data-list-filters]");
      for (const control of form?.querySelectorAll("[name]") || []) {
        if (["q", "kind", "state"].includes(control.name))
          control.value = params.get(gridParameter(region, control.name)) || "";
      }
      const kind = region.dataset.grid;
      const field =
        params.get(gridParameter(region, "sort")) ||
        (kind === "activity"
          ? "date"
          : kind === "instruments"
            ? "name"
            : "report");
      const dir =
        params.get(gridParameter(region, "dir")) ||
        (kind === "instruments" ? "asc" : "desc");
      state.selectionRestored = false;
      state.table.setSort(field, dir);
      state.table
        .setPage(Number(params.get(gridParameter(region, "page")) || 1))
        .catch(() => {});
    }
  });
  document.addEventListener("click", (event) => {
    const link = event.target.closest("[data-grid] a[href]");
    if (!link || location.pathname !== "/activity") return;
    const target = new URL(link.href, location.href);
    if (target.origin !== location.origin) return;
    const source = new URL(location.href);
    const record = link.closest("[data-record]")?.dataset.record;
    if (record) source.searchParams.set("selected", record);
    target.searchParams.set("origin", source.pathname + source.search);
    link.href = target.pathname + target.search;
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
      .ajax("GET", "/status/contact", {
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
    if (path === "/status/contact") {
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
