/* SPDX-FileCopyrightText: 2026 trippwill
 * SPDX-License-Identifier: MPL-2.0
 */
(() => {
  const credentialAPI = window.PublicKeyCredential;
  const supported =
    window.isSecureContext &&
    typeof credentialAPI?.parseCreationOptionsFromJSON === "function" &&
    typeof credentialAPI?.parseRequestOptionsFromJSON === "function" &&
    typeof credentialAPI?.prototype.toJSON === "function" &&
    typeof navigator.credentials?.get === "function" &&
    typeof navigator.credentials?.create === "function";
  const prompts = new WeakMap();
  const post = async (form, step, value) => {
    const headers = { "Content-Type": "application/json" };
    if (form.dataset.passkeyCsrf)
      headers["X-Wake-CSRF"] = form.dataset.passkeyCsrf;
    const response = await fetch(`/passkeys/${step}`, {
      method: "POST",
      headers,
      body: JSON.stringify(value),
      redirect: "error",
      signal: AbortSignal.timeout(8000),
    });
    const result = await response.json().catch(() => null);
    if (!response.ok || !result)
      throw new Error(
        result?.error || "Wake could not complete the request. Try again.",
      );
    return result;
  };
  const finish = async (form, next, status) => {
    const create = next.kind === "create";
    const controller = new AbortController();
    const cancel = form.querySelector("#passkey-cancel");
    prompts.set(form, controller);
    if (cancel) cancel.disabled = false;
    status.textContent = create
      ? "Follow your browser’s prompt to save the passkey."
      : "Follow your browser’s prompt to use a passkey.";
    let credential;
    try {
      credential = await (create
        ? navigator.credentials.create({
            publicKey: credentialAPI.parseCreationOptionsFromJSON(next.options),
            signal: controller.signal,
          })
        : navigator.credentials.get({
            publicKey: credentialAPI.parseRequestOptionsFromJSON(next.options),
            signal: controller.signal,
          }));
    } catch (error) {
      if (error.name === "NotAllowedError" || error.name === "AbortError")
        throw new Error(
          "The passkey prompt was canceled or timed out. Try again.",
        );
      if (error.name === "InvalidStateError")
        throw new Error(
          "This passkey is already enrolled. Choose another passkey.",
        );
      throw new Error("Your browser could not use a passkey at this address.");
    } finally {
      prompts.delete(form);
      if (cancel) cancel.disabled = true;
    }
    if (!credential) throw new Error("No passkey was selected. Try again.");
    status.textContent = "Verifying with Wake…";
    return post(form, "finish", {
      id: next.id,
      credential: credential.toJSON(),
    });
  };
  const run = async (form, button, action) => {
    if (form.dataset.passkeyBusy) return;
    const status = form.querySelector("[data-passkey-status]");
    status.textContent = "Contacting Wake…";
    status.classList.remove("wake-error");
    form.dataset.passkeyBusy = "true";
    button.disabled = true;
    const cancel = form.querySelector("#passkey-cancel");
    if (cancel) cancel.disabled = true;
    try {
      await action(status);
    } catch (error) {
      status.textContent =
        error.name === "TypeError" || error.name === "TimeoutError"
          ? "Wake could not be reached. Try again."
          : error.message;
      status.classList.add("wake-error");
      const retry = button.hidden
        ? form.querySelector("#passkey-submit")
        : button;
      retry.disabled = false;
      retry.focus();
    } finally {
      delete form.dataset.passkeyBusy;
      button.disabled = false;
      if (cancel) cancel.disabled = false;
    }
  };
  const begin = async (form, body, status) => {
    const next = await post(form, "begin", body);
    return next.kind === "get" ? finish(form, next, status) : next;
  };
  const requireDone = (next) => {
    if (next.kind !== "done")
      throw new Error("Wake could not complete the request. Try again.");
  };
  const enhanceLogin = (form) => {
    if (!supported) return;
    const button = form.querySelector("#passkey-login");
    button.hidden = false;
    form.querySelector("[data-passkey-browser-help]").hidden = true;
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      run(form, button, async (status) => {
        requireDone(
          await begin(form, { operation: "login", method: "passkey" }, status),
        );
        location.assign("/");
      });
    });
  };
  const enhanceSettings = (section) => {
    const dialog = section.querySelector("#browser-access-dialog");
    const form = dialog.querySelector("form");
    const fields = form.querySelector("[data-passkey-fields]");
    const status = form.querySelector("[data-passkey-status]");
    const start = form.querySelector("#passkey-submit");
    const create = form.querySelector("#passkey-create");
    const cancel = form.querySelector("#passkey-cancel");
    const { label, password, newPassword, confirm } = form.elements;
    const method = form.querySelector("#passkey-method");
    const canAssert = supported && !!method.querySelector('[value="passkey"]');
    if (!canAssert) method.querySelector('[value="passkey"]')?.remove();
    for (const help of section.querySelectorAll("[data-passkey-browser-help]"))
      help.hidden = supported;
    const updateCurrentPassword = () => {
      password.value = "";
      password.disabled = password.closest("label").hidden =
        method.value !== "password";
    };
    const resetSecrets = () => {
      password.value = newPassword.value = confirm.value = "";
    };
    let operation, target, creation;
    const setCreation = (next) => {
      creation = next;
      fields.disabled = fields.hidden = start.hidden = !!creation;
      create.hidden = !creation;
    };
    for (const button of section.querySelectorAll("[data-passkey-open]")) {
      if (!supported && button.hasAttribute("data-passkey-required")) continue;
      button.hidden = false;
      button.addEventListener("click", () => {
        operation = button.dataset.passkeyOpen;
        target = button.dataset.passkeyTarget || "";
        const adding = operation === "add";
        const renaming = operation === "rename";
        const removing = operation === "remove";
        const disabling = operation === "disable";
        const resetting = operation === "set-password";
        const passkeyOnly = disabling || resetting;
        const title = {
          add: button.textContent,
          rename: "Rename passkey",
          remove: "Remove passkey",
          disable: button.textContent,
          "set-password": button.textContent,
        }[operation];
        const description = {
          add: "Give this passkey an optional label to identify it later.",
          rename: "Leave the label blank to use the default name.",
          remove: `You’ll no longer be able to sign in using ${button.dataset.passkeyName}.`,
          disable:
            "You’ll sign in with a passkey. Disabling your password signs out your other browser sessions.",
          "set-password":
            "Confirm with a passkey. Setting your password signs out your other browser sessions.",
        }[operation];
        dialog.querySelector("h2").textContent = title;
        dialog.querySelector("#browser-access-description").textContent =
          description;
        start.textContent = adding
          ? "Continue"
          : passkeyOnly
            ? "Confirm with a passkey"
            : title;
        status.textContent = "";
        status.classList.remove("wake-error");
        setCreation(null);
        resetSecrets();
        label.disabled = label.closest("label").hidden = !adding && !renaming;
        label.value = renaming ? button.dataset.passkeyLabel : "";
        form.querySelector("[data-passkey-new-fields]").hidden = !resetting;
        newPassword.disabled = confirm.disabled = !resetting;
        method.closest("label").hidden =
          passkeyOnly || method.options.length < 2;
        method.value = canAssert ? "passkey" : "password";
        updateCurrentPassword();
        dialog.showModal();
        if (removing || disabling) cancel.focus();
      });
    }
    method.addEventListener("change", updateCurrentPassword);
    cancel.addEventListener("click", () => {
      if (form.dataset.passkeyBusy) prompts.get(form)?.abort();
      else dialog.close();
    });
    dialog.addEventListener("cancel", (event) => {
      if (!form.dataset.passkeyBusy) return;
      event.preventDefault();
      prompts.get(form)?.abort();
    });
    dialog.addEventListener("close", () => {
      resetSecrets();
      setCreation(null);
    });
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      if (creation) return;
      run(form, start, async (message) => {
        let next;
        try {
          const body = Object.fromEntries(new FormData(form));
          next = await begin(
            form,
            { ...body, operation, target, method: method.value },
            message,
          );
        } finally {
          resetSecrets();
        }
        if (next.kind === "create" && operation === "add") {
          setCreation(next);
          message.textContent =
            "Choose where to save your new passkey in the next prompt.";
          create.focus();
          return;
        }
        requireDone(next);
        location.reload();
      });
    });
    create.addEventListener("click", () => {
      run(form, create, async (message) => {
        let next;
        try {
          next = await finish(form, creation, message);
        } finally {
          setCreation(null);
        }
        requireDone(next);
        location.reload();
      });
    });
  };
  const enhance = () => {
    for (const node of document.querySelectorAll(
      "[data-passkey-login],[data-browser-access]",
    )) {
      if (node.dataset.passkeyReady) continue;
      node.dataset.passkeyReady = "true";
      if (node.hasAttribute("data-passkey-login")) enhanceLogin(node);
      else enhanceSettings(node);
    }
  };
  enhance();
  document.addEventListener("htmx:afterSettle", enhance);
})();

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
  const sourceFragments = [
    "#source-entry",
    "#report-entries",
    "#record-sources",
    "#activity-price",
    "#accepted-activity",
  ];
  const trailAddress = (value) => {
    const address = localAddress(value);
    if (!address) return null;
    const fragment = new URL(value, location.href).hash;
    return address + (sourceFragments.includes(fragment) ? fragment : "");
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
    const trail = value.trail.map(trailAddress);
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
    const previous = next.trail.indexOf(trailAddress(target));
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
      next.trail.push(trailAddress(location.href));
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
  const restoreSourcePosition = () => {
    const { hash } = location;
    if (!sourceFragments.includes(hash)) return;
    const anchor = document.getElementById(hash.slice(1));
    // Grid anchors hand focus to the selected row when its data arrives.
    const remembered =
      hash === "#source-entry" || hash === "#activity-price"
        ? document.getElementById(history.state?.wakeSourceFocus || "")
        : null;
    if (hash === "#record-sources") {
      const grid = document.querySelector('[data-grid="observations"]');
      const disclosure = grid?.closest("details");
      if (disclosure) disclosure.open = true;
    }
    if ([document.body, anchor].includes(document.activeElement))
      (remembered || anchor)?.focus({ preventScroll: true });
  };
  restoreNavigation();
  window.addEventListener("pageshow", (event) => {
    if (event.persisted) restoreNavigation();
    else updateHistory();
    restoreSourcePosition();
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
    const observationGrid = link.closest('[data-grid="observations"]');
    const record = link.closest("[data-record]")?.dataset.record;
    if (
      record &&
      (observationGrid ||
        ["/activity", "/positions"].includes(location.pathname))
    ) {
      const current = new URL(location.href);
      current.searchParams.set("selected", record);
      if (observationGrid) {
        current.hash =
          observationGrid.dataset.gridVariant === "artifact"
            ? "#report-entries"
            : "#record-sources";
      }
      const column = link
        .closest("[tabulator-field]")
        ?.getAttribute("tabulator-field");
      history.replaceState(
        { ...navigationState(), wakeGridColumn: column },
        "",
        current,
      );
      navigationAddress = location.href;
    }
    if (link.hasAttribute("data-source-return")) {
      const current = new URL(location.href);
      current.hash = link.closest("#activity-price")
        ? "#activity-price"
        : "#source-entry";
      history.replaceState(
        { ...navigationState(), wakeSourceFocus: link.id },
        "",
        current,
      );
      navigationAddress = location.href;
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
  contactDetails?.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" || !contactDetails.open) return;
    event.stopPropagation();
    contactDetails.open = false;
    contactDetails.querySelector("summary").focus();
  });
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
        button.textContent = button.dataset.submitLabel || "Saving…";
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
        message(invalid === currency ? "Choose a currency." : hint);
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
    positions: [
      ["name", "Instrument"],
      ["side", "Side"],
      ["quantity", "Quantity"],
      ["cost", "Unit cost basis"],
      ["terms", "Reported terms"],
    ],
    instruments: [
      ["ticker", "Ticker"],
      ["kind", "Kind"],
      ["name", "Description"],
      ["state", "Status"],
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
        ? "ticker"
        : region.dataset.grid === "positions"
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
    if (value.href?.startsWith("/") && !value.href.startsWith("//")) {
      const target = new URL(value.href, location.href);
      if (
        target.pathname === location.pathname &&
        target.searchParams.has("entry")
      ) {
        const current = new URL(location.href);
        for (const name of ["q", "sort", "dir", "page", "size", "receipt"]) {
          const value = current.searchParams.get(name);
          if (value) target.searchParams.set(name, value);
        }
        target.searchParams.set("selected", cell.getRow().getData().id);
      }
      text.href = target.pathname + target.search + target.hash;
    }
    if (value.tone) {
      text.classList.add("wake-state");
      text.dataset.state = value.tone;
    }
    node.append(text);
    if (!value.text && !value.note) node.hidden = true;
    if (value.note) {
      const note = document.createElement("span");
      note.className = "wake-help wake-cell-note";
      note.textContent = value.note;
      node.append(note);
    }
    if (value.details?.length) {
      const details = document.createElement("details");
      const summary = document.createElement("summary");
      summary.textContent = value.text;
      details.append(summary);
      const facts = document.createElement("dl");
      for (const fact of value.details) {
        const label = document.createElement("dt");
        label.textContent = fact.label;
        const content = document.createElement("dd");
        content.textContent = fact.value;
        facts.append(label, content);
      }
      details.append(facts);
      details.addEventListener("toggle", () => cell.getRow().normalizeHeight());
      text.replaceWith(details);
    }
    return node;
  };
  const positionSummary = (region, result) => {
    const meta = result.positions;
    if (!meta) return;
    for (const field of ["account", "state", "as_of", "message"])
      region.querySelector(
        `[data-positions-${field.replaceAll("_", "-")}]`,
      ).textContent = meta[field];
    const source = region.querySelector("[data-positions-source]");
    source.href = meta.source_href;
    source.textContent = meta.source_label;
    region.querySelector("[data-positions-message]").hidden = !meta.message;
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
        const variant = region.dataset.gridVariant;
        const artifact = variant === "artifact";
        const appearances = variant === "appearances";
        const preferenceKey = (id, type) =>
          `${id}${variant && type === "columns" ? `-${variant}` : ""}-${type}`;
        const emptyState = document.createElement("span");
        emptyState.textContent = "No matching records.";
        const params = new URL(location.href).searchParams;
        const initialSort = params.get("sort") || gridDefaultSort(region);
        const initialDirection =
          params.get("dir") ||
          (["instruments", "positions"].includes(kind) ? "asc" : "desc");
        const element = region.querySelector("[data-grid-table]");
        const createColumns = () => {
          const definitions = artifact
            ? [
                ["location", "Reported entry"],
                ["date", "Reported date"],
                ["terms", "Reported terms"],
                ["outcome", "Effect on record"],
                ["activity", "Related activity"],
                ["rule", "Acceptance rule / version"],
              ]
            : appearances
              ? [
                  ["report", "Report"],
                  ["terms", "Reported terms"],
                  ["outcome", "Effect on record"],
                ]
              : gridColumns[kind];
          const columns = definitions.map(([field, title]) => ({
            field,
            title,
            minWidth: 130,
            widthGrow: ["location", "terms"].includes(field) ? 2 : 1,
            visible: !artifact || field !== "rule",
            headerWordWrap: true,
            headerSort:
              field !== "terms" && !(artifact && field === "location"),
            variableHeight: true,
            // Header handles avoid inserting resize handles in each row
            // between Tabulator's height reads.
            resizable:
              kind === "activity" || artifact || appearances ? "header" : true,
            formatter: gridCell,
          }));
          if (artifact)
            columns.push({
              field: "report",
              title: "Report",
              visible: false,
              headerSort: false,
            });
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
          filterChange: null,
          focus: null,
          selectionRestored: false,
          restoring: false,
        };
        const rememberFocus = () => {
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
        const restoreFocus = () => {
          const focus = state.focus;
          state.focus = null;
          if (!focus || document.activeElement !== document.body) return;
          const selector = focus.field
            ? `.tabulator-col[tabulator-field="${CSS.escape(focus.field)}"]`
            : `[data-record="${CSS.escape(focus.record)}"] [tabulator-field="${CSS.escape(focus.column)}"] a[href]`;
          element.querySelector(selector)?.focus({ preventScroll: true });
        };
        const requestData = async (
          url,
          _config,
          parameters,
          background = false,
        ) => {
          const filterChange = background ? null : state.filterChange;
          if (!background) {
            state.retryForeground = false;
            state.installing = null;
            delete region.dataset.gridReady;
            state.available = false;
            refresh.hidden = true;
            status.toggleAttribute("data-filter-pending", !!filterChange);
            status.textContent = filterChange
              ? "Updating filters…"
              : "Loading table…";
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
          if (filterChange) {
            current.search = filterChange.toString();
            current.searchParams.delete("selected");
          }
          for (const key of ["q", "kind", "state", "account"])
            if (current.searchParams.has(key))
              target.searchParams.set(key, current.searchParams.get(key));
          const sort = parameters.sort?.[0] || {
            field: gridDefaultSort(region),
            dir: kind === "instruments" ? "asc" : "desc",
          };
          target.searchParams.set("sort", sort.field);
          target.searchParams.set("dir", sort.dir);
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
            if (sequence !== state.sequence)
              throw new Error("Superseded table request");
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
            if (!background) {
              state.installing = result;
              state.filterChange = null;
              updateHistory(
                current,
                !!filterChange && current.href !== location.href,
              );
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
              status.removeAttribute("data-filter-pending");
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
          // CSS sizes Activity rows. The estimate skips Tabulator's
          // measurement of every cell, including those below the viewport.
          rowHeight: kind === "activity" ? 48 : null,
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
          persistence: {
            columns: ["width", "visible"],
            page: { size: true, page: false },
          },
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
          placeholder: kind === "positions" ? false : emptyState,
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
            ? kind === "positions"
              ? "Displayed report is out of date. Refresh the table to see the latest positions and availability."
              : "Updated results are available. Refresh the table to see them."
            : `${state.displayed.last_row} of ${state.displayed.total} results`;
          if (status.textContent !== message) status.textContent = message;
        };
        const refreshTable = async () => {
          const restoreFocus =
            document.activeElement === refresh ||
            document.activeElement === retry;
          const displayed = state.displayed;
          try {
            if (state.filterChange) {
              await table.setData();
            } else {
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
                if (status.textContent !== message)
                  status.textContent = message;
              }
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
        // Replacing data and resizing both detach the displayed rows.
        table.on("dataProcessing", rememberFocus);
        table.on("renderStarted", rememberFocus);
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
              heading.getAttribute("tabulator-field") === sort?.field &&
                sort.column.getDefinition().headerSort !== false
                ? sort.dir === "asc"
                  ? "ascending"
                  : "descending"
                : "none",
            );
          }
          restoreFocus();
        });
        table.on("dataProcessed", (rows) => {
          if (state.installing?.data === rows) {
            state.displayed = state.installing;
            state.installing = null;
            if (artifact)
              emptyState.textContent =
                state.displayed.report_empty ?? "No matching report entries.";
            positionSummary(region, state.displayed);
            status.textContent = `${state.displayed.last_row} of ${state.displayed.total} results`;
          }
          region.dataset.gridReady = "true";
          restoreFocus();
          if (!state.selectionRestored && table.getDataCount() > 0) {
            state.selectionRestored = true;
            const selected = new URL(location.href).searchParams.get(
              "selected",
            );
            const observationAnchor =
              kind === "observations" &&
              ((artifact && location.hash === "#report-entries") ||
                (!artifact && location.hash === "#record-sources"))
                ? document.getElementById(location.hash.slice(1))
                : null;
            if (
              selected &&
              ((document.activeElement === document.body && !location.hash) ||
                (observationAnchor &&
                  [document.body, observationAnchor].includes(
                    document.activeElement,
                  )))
            ) {
              const row = element.querySelector(
                `[data-record="${CSS.escape(selected)}"]`,
              );
              const column = history.state?.wakeGridColumn;
              const link =
                column &&
                row?.querySelector(
                  `[tabulator-field="${CSS.escape(column)}"] a[href]`,
                );
              (link || row?.querySelector("a[href]"))?.focus({
                preventScroll: !observationAnchor,
              });
            }
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
          } else if (
            ["Enter", " "].includes(event.key) &&
            column.getDefinition().headerSort !== false
          ) {
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
  const applyGridFilter = (control) => {
    const state = grids.get(control.closest("[data-grid]"));
    if (!state) return;
    const params = new URLSearchParams(state.filterChange ?? location.search);
    if (control.value) params.set(control.name, control.value);
    else params.delete(control.name);
    state.filterChange = params;
    state.table.setData().catch(() => {});
  };
  document.addEventListener("change", (event) => {
    if (event.target.matches("[data-list-filters] select[name]"))
      applyGridFilter(event.target);
  });
  document.addEventListener("submit", (event) => {
    const form = event.target.closest(
      "[data-list-filters],[data-observation-filters]",
    );
    if (!form) return;
    event.preventDefault();
    applyGridFilter(form.elements.namedItem("q"));
  });

  window.addEventListener("popstate", (event) => {
    if (event.wakeFragmentNavigation) return;
    const params = new URL(location.href).searchParams;
    for (const [region, state] of grids) {
      state.filterChange = null;
      const form =
        region.querySelector("[data-observation-filters]") ||
        document.querySelector("[data-list-filters]");
      for (const control of form?.querySelectorAll("[name]") || []) {
        if (["q", "kind", "state", "account"].includes(control.name))
          control.value =
            params.get(control.name) || control.options?.[0]?.value || "";
      }
      const kind = region.dataset.grid;
      const field = params.get("sort") || gridDefaultSort(region);
      const dir =
        params.get("dir") ||
        (["instruments", "positions"].includes(kind) ? "asc" : "desc");
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
      if (
        form?.method.toLowerCase() === "post" &&
        !form.hasAttribute("data-passkey-csrf")
      )
        dirtyForms.add(form);
    });
  const protectedContent = () => {
    const region = document.querySelector("#workspace-content");
    if (!region) return false;
    return (
      region.contains(document.activeElement) ||
      !!region.querySelector("#browser-access-dialog[open]") ||
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
