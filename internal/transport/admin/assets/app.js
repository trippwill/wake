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

  const textOrder = (left, right) =>
    left.localeCompare(right, undefined, { numeric: true });
  // Compare arbitrary-precision decimal text within the same currency or unit.
  const decimalOrder = (left, right) => {
    const parse = (text) => {
      const match = /^(-?)(\d+)(?:\.(\d+))?(?:\s+(.*))?$/.exec(text.trim());
      if (!match) return null;
      const integer = match[2].replace(/^0+(?=\d)/, "");
      const fraction = (match[3] || "").replace(/0+$/, "");
      return {
        negative: match[1] === "-" && (integer !== "0" || fraction !== ""),
        integer,
        fraction,
        unit: match[4] || "",
      };
    };
    const a = parse(left),
      b = parse(right);
    if (!a || !b) return a ? -1 : b ? 1 : textOrder(left, right);
    if (a.unit !== b.unit) return textOrder(a.unit, b.unit);
    if (a.negative !== b.negative) return a.negative ? -1 : 1;
    const places = Math.max(a.fraction.length, b.fraction.length);
    const af = a.fraction.padEnd(places, "0"),
      bf = b.fraction.padEnd(places, "0");
    const order =
      a.integer.length - b.integer.length ||
      (a.integer < b.integer ? -1 : a.integer > b.integer ? 1 : 0) ||
      (af < bf ? -1 : af > bf ? 1 : 0);
    return a.negative ? -order : order;
  };
  const gridKey = (region) =>
    `wake.grid:${location.pathname}:${new URL(location.href).searchParams.get("view") || "all"}:${region.dataset.grid}`;
  const readGridState = (region) => {
    try {
      return JSON.parse(sessionStorage.getItem(gridKey(region)));
    } catch {
      return null;
    }
  };
  const saveGridState = (region, state) => {
    const table = state.table;
    if (!table || !region.dataset.gridReady) return;
    const holder = region.querySelector(".tabulator-tableholder");
    const sort = table
      .getSorters()
      .map((s) => ({ column: s.field, dir: s.dir }));
    const value = {
      layout: table.getColumnLayout(),
      sort,
      page: region.hasAttribute("data-server-list")
        ? Number(new URL(location.href).searchParams.get("page") || 1)
        : table.getPage(),
      x: holder?.scrollLeft || 0,
      y: holder?.scrollTop || 0,
    };
    try {
      sessionStorage.setItem(gridKey(region), JSON.stringify(value));
    } catch {
      /* Per-tab storage is optional. */
    }
    if (
      location.pathname === "/activity" &&
      !region.hasAttribute("data-server-list")
    ) {
      const url = new URL(location.href);
      url.searchParams.set("page", String(value.page || 1));
      if (sort.length) {
        const index = Number(sort[0].column.slice(1));
        const field = region.querySelectorAll("thead th")[index]?.dataset.field;
        if (field) {
          url.searchParams.set("sort", field);
          url.searchParams.set("dir", sort[0].dir);
        }
      } else {
        url.searchParams.delete("sort");
        url.searchParams.delete("dir");
      }
      history.replaceState(null, "", url);
      for (const name of ["sort", "dir"]) {
        const control = document.querySelector(
          `[data-list-filters] [name="${name}"]`,
        );
        if (control) control.value = url.searchParams.get(name) || "";
      }
    }
  };
  const grids = new Map();
  const rows = (source) =>
    [...source.tBodies[0].rows].map((row) => {
      const data = { id: row.id, cells: [...row.cells] };
      data.cells.forEach((cell, index) => {
        data[`c${index}`] = cell.dataset.sortValue || cell.textContent.trim();
      });
      return data;
    });
  const enhance = (root) => {
    if (typeof window.Tabulator !== "function") return;
    for (const region of root.querySelectorAll("[data-grid]")) {
      if (
        grids.has(region) ||
        region.closest("details:not([open])") ||
        !region.getClientRects().length
      )
        continue;
      const serverList = region.hasAttribute("data-server-list");
      const serverSort = (field) => {
        const url = new URL(location.href);
        const direction =
          url.searchParams.get("sort") === field &&
          url.searchParams.get("dir") === "asc"
            ? "desc"
            : "asc";
        url.searchParams.set("sort", field);
        url.searchParams.set("dir", direction);
        url.searchParams.set("page", "1");
        try {
          sessionStorage.setItem(
            "wake.grid.heading",
            `${region.dataset.grid}|${field}`,
          );
        } catch {}
        location.assign(url);
      };
      const source = region.querySelector("table");
      if (!source?.tBodies[0]?.rows.length) continue;
      const controls = document.createElement("div");
      controls.className = "wake-grid-controls";
      const label = document.createElement("label");
      label.className = "wake-grid-filter";
      label.textContent = "Filter loaded rows";
      const input = document.createElement("input");
      input.type = "search";
      input.className = "input";
      label.append(input);
      const count = document.createElement("span");
      count.setAttribute("role", "status");
      const reset = document.createElement("button");
      reset.type = "button";
      reset.className = "btn btn-sm";
      reset.textContent = "Reset view";
      const serverFilters = !!document.querySelector("[data-list-filters]");
      if (!serverFilters) controls.append(label);
      controls.append(count, reset);
      const help = document.createElement("details");
      const helpTitle = document.createElement("summary");
      helpTitle.textContent = "Table controls";
      help.className = "wake-help";
      help.textContent = serverList
        ? "Sorting, filtering and paging apply to the whole warehouse. Enter sorts a focused heading; Alt + arrow keys resize it. 50 rows per page."
        : "Sorting and filtering apply to loaded rows. Enter sorts a focused heading; Alt + arrow keys resize it. 50 rows per page.";
      help.prepend(helpTitle);
      const element = document.createElement("div");
      element.setAttribute("aria-label", region.getAttribute("aria-label"));
      const state = {
        source,
        html: source.outerHTML,
        data: rows(source),
        table: null,
      };
      const fallback = () => {
        state.table?.destroy();
        source.hidden = false;
        controls.remove();
        help.remove();
        element.remove();
        delete region.dataset.gridReady;
        grids.delete(region);
      };
      region.append(controls, help, element);
      try {
        const columns = [...source.tHead.rows[0].cells].map(
          (header, index) => ({
            title: header.textContent,
            field: `c${index}`,
            headerSort: !serverList,
            headerClick: serverList
              ? () => serverSort(header.dataset.field)
              : undefined,
            minWidth: 130,
            headerWordWrap: true,
            variableHeight: true,
            titleFormatter: () => {
              const node = document.createElement("span");
              node.textContent = header.textContent;
              return node;
            },
            sorter:
              header.dataset.sort === "decimal" ? decimalOrder : textOrder,
            formatter: (cell) => {
              const node = document.createElement("div");
              node.className = "wake-grid-cell";
              node.append(
                ...[...cell.getRow().getData().cells[index].childNodes].map(
                  (child) => child.cloneNode(true),
                ),
              );
              return node;
            },
          }),
        );
        const table = new window.Tabulator(element, {
          data: state.data,
          columns,
          index: "id",
          layout: "fitColumns",
          maxHeight: "32rem",
          pagination: !serverList,
          paginationMode: "local",
          paginationSize: 50,
          renderVertical: "basic",
          placeholder: "No matching loaded rows.",
        });
        state.table = table;
        grids.set(region, state);
        let originalLayout;
        const update = () => {
          const summary = `${table.getDataCount("active")} of ${state.data.length} loaded rows`;
          if (count.textContent !== summary) count.textContent = summary;
          const footer = element.querySelector(".tabulator-footer");
          if (footer) footer.hidden = table.getDataCount("active") <= 50;
          for (const heading of element.querySelectorAll(".tabulator-col"))
            heading.tabIndex = 0;
        };
        table.on("tableBuilt", () => {
          source.hidden = true;
          region.dataset.gridReady = "true";
          originalLayout = table.getColumnLayout();
          const query = new URL(location.href).searchParams;
          const requestedIndex = [
            ...source.querySelectorAll("thead th"),
          ].findIndex((header) => header.dataset.field === query.get("sort"));
          const stored = readGridState(region);
          if (stored) {
            if (Array.isArray(stored.layout))
              table.setColumnLayout(stored.layout);
            if (!serverList && requestedIndex < 0 && Array.isArray(stored.sort))
              table.setSort(stored.sort);
            if (!serverList)
              table.setPage(
                Math.min(stored.page || 1, table.getPageMax()) || 1,
              );
            requestAnimationFrame(() => {
              const holder = element.querySelector(".tabulator-tableholder");
              if (holder) {
                holder.scrollTop = stored.y || 0;
                holder.scrollLeft = stored.x || 0;
              }
            });
          }
          if (!serverList && requestedIndex >= 0)
            table.setSort(
              `c${requestedIndex}`,
              query.get("dir") === "asc" ? "asc" : "desc",
            );
          const requestedPage = Number(query.get("page"));
          if (!serverList && requestedPage > 0)
            table.setPage(Math.min(requestedPage, table.getPageMax()) || 1);
          update();
          if (serverList) {
            for (const heading of element.querySelectorAll(".tabulator-col")) {
              const index = Number(
                heading.getAttribute("tabulator-field")?.slice(1),
              );
              const field =
                source.querySelectorAll("thead th")[index]?.dataset.field;
              heading.setAttribute(
                "aria-sort",
                query.get("sort") === field
                  ? query.get("dir") === "asc"
                    ? "ascending"
                    : "descending"
                  : "none",
              );
              try {
                if (
                  sessionStorage.getItem("wake.grid.heading") ===
                  `${region.dataset.grid}|${field}`
                ) {
                  heading.focus({ preventScroll: true });
                  sessionStorage.removeItem("wake.grid.heading");
                }
              } catch {}
            }
          }
          requestAnimationFrame(() => {
            const selected = new URL(location.href).searchParams.get(
              "selected",
            );
            const link =
              selected &&
              element.querySelector(`[data-record="${CSS.escape(selected)}"]`);
            if (link && document.activeElement === document.body)
              link.focus({ preventScroll: true });
          });
        });
        const save = () => saveGridState(region, state);
        table.on("dataSorted", save);
        table.on("pageLoaded", save);
        table.on("columnResized", save);
        table.on("dataFiltered", update);
        table.on("renderComplete", update);
        input.addEventListener("input", () => {
          const query = input.value.toLocaleLowerCase();
          if (query)
            table.setFilter((data) =>
              data.cells.some((cell) =>
                cell.textContent.toLocaleLowerCase().includes(query),
              ),
            );
          else table.clearFilter();
        });
        reset.addEventListener("click", () => {
          input.value = "";
          table.clearFilter();
          if (!serverList) table.clearSort();
          if (originalLayout) table.setColumnLayout(originalLayout);
          if (serverList) {
            const url = new URL(location.href);
            for (const key of ["sort", "dir", "page"])
              url.searchParams.delete(key);
            location.assign(url);
          }
        });
        element.addEventListener("keydown", (event) => {
          const header = event.target.closest(".tabulator-col");
          if (!header || header !== event.target) return;
          const column = table.getColumn(
            header.getAttribute("tabulator-field"),
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
            header.dispatchEvent(
              new MouseEvent("click", {
                bubbles: true,
                shiftKey: event.shiftKey,
              }),
            );
          }
        });
        state.refresh = async (next) => {
          const incoming = next.querySelector("table");
          if (!incoming || incoming.outerHTML === state.html) return;
          const holder = element.querySelector(".tabulator-tableholder");
          const x = holder?.scrollLeft || 0,
            y = holder?.scrollTop || 0,
            page = serverList ? 1 : table.getPage();
          state.data = rows(incoming);
          state.html = incoming.outerHTML;
          source.replaceChildren(
            ...[...incoming.childNodes].map((child) => child.cloneNode(true)),
          );
          try {
            await table.replaceData(state.data);
            if (!serverList)
              await table.setPage(Math.min(page, table.getPageMax()) || 1);
            if (holder) {
              holder.scrollLeft = x;
              holder.scrollTop = y;
            }
            update();
          } catch {
            fallback();
          }
        };
      } catch {
        fallback();
      }
    }
  };
  const scrollable = (root) => {
    for (const region of root.querySelectorAll(".wake-table-scroll")) {
      if (region.scrollWidth > region.clientWidth + 1) region.tabIndex = 0;
      else region.removeAttribute("tabindex");
    }
  };
  enhance(document);
  scrollable(document);
  window.addEventListener("resize", () => scrollable(document));
  document.addEventListener(
    "toggle",
    (event) => {
      if (event.target.open) {
        enhance(event.target);
        scrollable(event.target);
      }
    },
    true,
  );

  if (window.Idiomorph) {
    window.Idiomorph.defaults.ignoreActiveValue = true;
    window.Idiomorph.defaults.callbacks.beforeAttributeUpdated = (name, node) =>
      name !== "open" || !(node instanceof HTMLDetailsElement);
    window.Idiomorph.defaults.callbacks.beforeNodeMorphed = (old) =>
      !grids.has(old);
    window.Idiomorph.defaults.callbacks.beforeNodeRemoved = (old) =>
      !grids.has(old);
  }
  document.addEventListener("click", (event) => {
    const link = event.target.closest("a[href]");
    if (
      !link ||
      location.pathname !== "/activity" ||
      !link.closest("[data-grid]")
    )
      return;
    for (const [region, state] of grids) saveGridState(region, state);
    const target = new URL(link.href, location.href);
    if (target.origin !== location.origin) return;
    const source = new URL(location.href);
    const row = link.closest(".tabulator-row");
    const record = row?.querySelector("[data-record]")?.dataset.record;
    if (record) source.searchParams.set("selected", record);
    target.searchParams.set("origin", source.pathname + source.search);
    link.href = target.pathname + target.search + target.hash;
  });
  const dirtyForms = new WeakSet();
  document.addEventListener("change", (event) => {
    const form = event.target.closest("form");
    if (form?.method.toLowerCase() === "post") dirtyForms.add(form);
  });
  document.addEventListener("input", (event) => {
    const form = event.target.closest("form");
    if (form?.method.toLowerCase() === "post") dirtyForms.add(form);
  });
  const protectedContent = () => {
    const region = document.querySelector("#workspace-content");
    if (!region) return false;
    const selection = document.getSelection();
    return (
      region.contains(document.activeElement) ||
      [...region.querySelectorAll("form")].some((form) =>
        dirtyForms.has(form),
      ) ||
      (selection &&
        !selection.isCollapsed &&
        region.contains(selection.anchorNode))
    );
  };
  const incomingGrids = new Map();
  document.addEventListener("htmx:beforeSwap", (event) => {
    if (
      event.detail.requestConfig?.elt?.id === "workspace-content" &&
      protectedContent()
    ) {
      event.detail.shouldSwap = false;
      return;
    }
    if (event.detail.xhr.status !== 200) return;
    const next = new DOMParser().parseFromString(
      event.detail.serverResponse,
      "text/html",
    );
    for (const node of next.querySelectorAll("[data-grid]"))
      incomingGrids.set(node.dataset.grid, node);
    if (event.detail.requestConfig?.elt?.matches("[data-list-filters]")) {
      const url = new URL(event.detail.xhr.responseURL, location.href);
      if (url.origin === location.origin) {
        url.searchParams.delete("page");
        url.searchParams.delete("selected");
        history.replaceState(null, "", url);
      }
    }
  });
  document.addEventListener("htmx:afterSettle", async () => {
    for (const [region, state] of grids) {
      const next = incomingGrids.get(region.dataset.grid);
      if (next) await state.refresh(next);
    }
    incomingGrids.clear();
    enhance(document);
    scrollable(document);
  });
  window.addEventListener("pagehide", () => {
    for (const [region, state] of grids) saveGridState(region, state);
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
    document.body.removeAttribute("sse-connect");
    stream?.close();
    setContact("expired", "Sign-in required");
  };
  htmx.createEventSource = (url) => {
    stream = new EventSource(url, { withCredentials: true });
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
      htmx.trigger(document.body, "htmx:afterProcessNode");
      for (const node of document.querySelectorAll("[hx-trigger]"))
        htmx.trigger(node, "htmx:afterProcessNode");
      poll = setInterval(checkContact, 10000);
      checkContact();
    }
  });
})();
