// qurator console — one vanilla JS file, no build step, no external origins.
//
// Responsibilities:
//   1. Configure htmx defensively (no eval, same-origin only) and attach the CSRF
//      header to every htmx-issued request.
//   2. A debounced, cancellable live QR preview against /v1/qr.
//   3. A custom (non-native, non-hx-confirm) confirmation dialog for destructive
//      htmx actions, driven by the htmx:confirm event.
//   4. A show-once API token secret: copy to clipboard, then remove from the DOM.
//   5. Small presentation helpers: a toast for copy confirmations and the hex readout
//      next to each colour swatch. Both are pure class/text toggles — no inline styles.
(function () {
  "use strict";

  var CSRF_HEADER = "X-Qurator-Requested-With";
  var CSRF_VALUE = "htmx";

  function configureHtmx() {
    if (typeof htmx === "undefined") return;
    // Defense in depth against the two things a strict CSP already forbids: htmx must
    // never fall back to eval(), and it must never issue a cross-origin request.
    htmx.config.allowEval = false;
    htmx.config.selfRequestsOnly = true;
    htmx.config.allowScriptTags = false;

    document.addEventListener("htmx:configRequest", function (evt) {
      evt.detail.headers[CSRF_HEADER] = CSRF_VALUE;

      // Optimistic-concurrency: a destination-edit form carries the version it was
      // rendered with in a data attribute; forward it as If-Match so a concurrent edit
      // loses the race with a 409 rather than silently clobbering.
      var form = evt.detail.elt.closest("form");
      var versioned = form && form.querySelector("[data-if-match]");
      if (versioned) {
        evt.detail.headers["If-Match"] = '"' + versioned.getAttribute("data-if-match") + '"';
      }
    });
  }

  function errorRegion(elt) {
    var form = elt && elt.closest("form");
    var region = form && form.querySelector("[data-form-error]");
    if (region) return region;
    region = document.querySelector("[data-page-error]");
    if (!region) {
      region = document.createElement("div");
      region.setAttribute("data-page-error", "");
      region.tabIndex = -1;
      (document.querySelector("main") || document.body).prepend(region);
    }
    return region;
  }

  function showRequestError(elt, message) {
    var region = errorRegion(elt);
    var banner = document.createElement("p");
    banner.className = "banner banner-danger";
    banner.setAttribute("role", "alert");
    banner.textContent = message;
    region.replaceChildren(banner);
    region.focus();
  }

  function configureResponses() {
    document.addEventListener("htmx:beforeSwap", function (evt) {
      var detail = evt.detail;
      var xhr = detail.xhr;
      if (xhr.status < 400) return;
      if (xhr.getResponseHeader("X-Qurator-Form-Error") === "true" &&
          xhr.status < 500 &&
          (xhr.getResponseHeader("Content-Type") || "").indexOf("text/html") === 0) {
        detail.target = errorRegion(detail.requestConfig.elt);
        detail.swapOverride = "innerHTML";
        detail.shouldSwap = true;
        detail.isError = false;
      } else {
        detail.shouldSwap = false;
        showRequestError(detail.requestConfig.elt,
          xhr.status === 429 ? "Too many requests. Wait before trying again." :
          "Could not complete the request. Please try again.");
      }
    });
    document.addEventListener("htmx:afterSwap", function (evt) {
      var region = evt.detail.target;
      if (region && region.hasAttribute("data-form-error") && region.querySelector('[role="alert"]')) {
        region.focus();
      }
    });
    document.addEventListener("htmx:afterRequest", function (evt) {
      var xhr = evt.detail.xhr;
      if (xhr.status < 200 || xhr.status >= 300) return;
      var form = evt.detail.elt.closest("form");
      var versioned = form && form.querySelector("[data-if-match]");
      var etag = xhr.getResponseHeader("ETag");
      if (versioned && etag && /^"[0-9]+"$/.test(etag)) {
        versioned.setAttribute("data-if-match", etag.slice(1, -1));
      }
    });
    ["htmx:sendError", "htmx:timeout"].forEach(function (name) {
      document.addEventListener(name, function (evt) {
        showRequestError(evt.detail.elt, "Could not reach the server. Please try again.");
      });
    });
  }

  // ---------------------------------------------------------------------------
  // Live styling preview
  // ---------------------------------------------------------------------------
  //
  // The preview calls the exact same ephemeral renderer the API exposes at
  // GET /v1/qr. A dynamic draft approximates the final symbol using the destination;
  // its eventual scan URL is chosen at creation. Requests are debounced 150ms,
  // in-flight stale requests are aborted, and identical parameter sets are served from
  // an in-memory memo cache.

  var PREVIEW_DEBOUNCE_MS = 150;
  var previewCache = new Map();
  var previewAbort = null;
  var previewTimer = null;

  function initPreview() {
    var form = document.querySelector("[data-preview-form]");
    var img = document.querySelector("[data-preview-image]");
    var status = document.querySelector("[data-preview-status]");
    if (!form || !img || form.dataset.previewReady) return;
    form.dataset.previewReady = "true";

    var fields = form.querySelectorAll("input, select, textarea");
    fields.forEach(function (field) {
      field.addEventListener("input", schedulePreview);
      field.addEventListener("change", schedulePreview);
    });

    function schedulePreview() {
      if (previewTimer) window.clearTimeout(previewTimer);
      previewTimer = window.setTimeout(runPreview, PREVIEW_DEBOUNCE_MS);
    }

    function buildQuery() {
      var params = new URLSearchParams();
      var data = new FormData(form);
      // A direct code's printed image encodes the destination itself, so the preview
      // for direct mode encodes exactly what will be saved. A dynamic code's printed
      // image instead encodes this instance's own scan address, which does not exist
      // until the code is saved; the preview approximates it with the destination
      // text, same placeholder behaviour as before mode existed.
      var content = data.get("destination");
      if (content) params.set("content", String(content));
      // Only styling fields feed the preview beyond that; alias and destination
      // validation happens server-side on save, not on every keystroke.
      var passthrough = [
        "format",
        "fg_color",
        "bg_color",
        "module_shape",
        "margin_modules",
        "size_px",
        "ec_level",
      ];
      passthrough.forEach(function (name) {
        var v = data.get(name);
        if (v !== null && v !== "") params.set(name, String(v));
      });
      return params;
    }

    var tile = document.querySelector("[data-preview-tile]");
    function showEmpty(empty) {
      if (tile) tile.classList.toggle("is-empty", empty);
      img.hidden = empty;
    }

    function runPreview() {
      var params = buildQuery();
      if (!params.get("content")) {
        showEmpty(true);
        setStatus("");
        return;
      }
      var key = params.toString();
      var cached = previewCache.get(key);
      if (cached) {
        img.src = cached;
        showEmpty(false);
        setStatus("");
        return;
      }

      if (previewAbort) previewAbort.abort();
      previewAbort = new AbortController();
      setStatus("Updating preview…");

      fetch("/v1/qr?" + key, {
        method: "GET",
        credentials: "same-origin",
        signal: previewAbort.signal,
      })
        .then(function (resp) {
          if (!resp.ok) throw new Error("preview request failed: " + resp.status);
          return resp.blob();
        })
        .then(function (blob) {
          return new Promise(function (resolve, reject) {
            var reader = new FileReader();
            reader.onload = function () {
              resolve(String(reader.result));
            };
            reader.onerror = reject;
            // CSP img-src allows 'self' and data: but not blob:, so the preview is
            // always assigned as a data: URL, never an object URL.
            reader.readAsDataURL(blob);
          });
        })
        .then(function (dataURL) {
          showEmpty(false);
          previewCache.set(key, dataURL);
          img.src = dataURL;
          setStatus("");
        })
        .catch(function (err) {
          if (err && err.name === "AbortError") return;
          setStatus("Preview unavailable.");
        });
    }

    function setStatus(text) {
      if (status) status.textContent = text;
    }

    // Render an initial preview for whatever the form's default values are.
    schedulePreview();
  }

  // ---------------------------------------------------------------------------
  // Custom confirmation dialog for destructive htmx actions
  // ---------------------------------------------------------------------------
  //
  // No hx-confirm and no hx-on:* attributes are used anywhere in the templates
  // (research.md §5's htmx-under-CSP constraint); the confirmation is wired entirely
  // from this file via the htmx:confirm event, which lets the message state the
  // specific consequence (already-printed codes stop working) rather than a generic
  // "Are you sure?".

  function initConfirm() {
    document.addEventListener("htmx:confirm", function (evt) {
      var source = evt.detail.elt.closest("[data-confirm-message]") ||
        evt.detail.elt.querySelector("[data-confirm-message]");
      var message = source && source.getAttribute("data-confirm-message");
      if (!message) return; // element opted out of confirmation entirely
      evt.preventDefault();

      var dialog = document.querySelector("[data-confirm-dialog]");
      if (!dialog || typeof dialog.showModal !== "function") {
        // No <dialog> support: fail safe by not proceeding rather than silently
        // skipping confirmation.
        return;
      }
      var text = dialog.querySelector("[data-confirm-text]");
      if (text) text.textContent = message;

      var onClose = function () {
        dialog.removeEventListener("close", onClose);
        if (dialog.returnValue === "confirm") {
          evt.detail.issueRequest(true);
        }
      };
      dialog.addEventListener("close", onClose);
      dialog.returnValue = "cancel";
      dialog.showModal();
    });
  }

  // ---------------------------------------------------------------------------
  // Toast
  // ---------------------------------------------------------------------------
  //
  // One [data-toast] element lives in the layout with aria-live="polite"; showing a
  // message is a text swap plus a class toggle so the transition is CSS-only.

  var toastTimer = null;

  function showToast(message) {
    var toast = document.querySelector("[data-toast]");
    if (!toast) return;
    toast.textContent = message;
    toast.classList.add("is-visible");
    if (toastTimer) window.clearTimeout(toastTimer);
    toastTimer = window.setTimeout(function () {
      toast.classList.remove("is-visible");
    }, 2200);
  }

  // ---------------------------------------------------------------------------
  // Clipboard copy helper
  // ---------------------------------------------------------------------------
  //
  // Shared by token secrets and storage URLs. Failure keeps the value visible and
  // offers manual copying; only a confirmed write invokes the success callback.

  function copyToClipboard(value, done) {
    function failed() {
      showToast("Could not copy. Select the text and copy it manually.");
    }
    if (!navigator.clipboard || !navigator.clipboard.writeText) {
      failed();
      return;
    }
    Promise.resolve().then(function () {
      return navigator.clipboard.writeText(value);
    }).then(done, failed);
  }

  // ---------------------------------------------------------------------------
  // Show-once token secret
  // ---------------------------------------------------------------------------
  //
  // The secret is present in the DOM only on the response to token creation. This
  // handler copies it to the clipboard and then removes the node — a page reload can
  // never show it again because the server never returns it a second time.

  function initTokenSecret() {
    var button = document.querySelector("[data-copy-secret]");
    if (!button || button.dataset.copyReady) return;
    button.dataset.copyReady = "true";
    var secretEl = document.querySelector("[data-secret-value]");
    if (!secretEl) return;

    button.addEventListener("click", function () {
      var value = secretEl.textContent || "";
      copyToClipboard(value, function () {
        var container = document.querySelector("[data-secret-container]");
        if (container && container.parentNode) {
          container.parentNode.removeChild(container);
        }
        button.textContent = "Copied — hidden";
        button.classList.add("is-copied");
        button.disabled = true;
        showToast("Secret copied to clipboard");
      });
    });
  }

  // ---------------------------------------------------------------------------
  // Generic "copy to clipboard" buttons
  // ---------------------------------------------------------------------------
  //
  // Unlike the token secret, the copied value (e.g. the code detail page's storage
  // URL) stays visible in its read-only input after copying — only the button's label
  // changes briefly to confirm the copy. Each button names its value element by id via
  // data-copy-target="<id>".

  function initCopyButtons() {
    var buttons = document.querySelectorAll("[data-copy-target]");
    buttons.forEach(function (button) {
      if (button.dataset.copyReady) return;
      button.dataset.copyReady = "true";
      var targetID = button.getAttribute("data-copy-target");
      var target = targetID ? document.getElementById(targetID) : null;
      if (!target) return;
      var originalLabel = button.innerHTML;

      button.addEventListener("click", function () {
        var value = "value" in target ? target.value : target.textContent;
        copyToClipboard(value || "", function () {
          button.textContent = "Copied";
          button.classList.add("is-copied");
          showToast("Copied to clipboard");
          window.setTimeout(function () {
            button.innerHTML = originalLabel;
            button.classList.remove("is-copied");
          }, 2000);
        });
      });
    });
  }

  // ---------------------------------------------------------------------------
  // Colour swatch hex readout
  // ---------------------------------------------------------------------------
  //
  // Each <input type="color"> has a sibling <output data-swatch-value="<input id>">
  // that mirrors its current hex value, so the chosen colour is readable as text.

  function initSwatches() {
    var outputs = document.querySelectorAll("[data-swatch-value]");
    outputs.forEach(function (output) {
      if (output.dataset.swatchReady) return;
      output.dataset.swatchReady = "true";
      var input = document.getElementById(output.getAttribute("data-swatch-value"));
      if (!input) return;
      var sync = function () {
        output.textContent = input.value;
      };
      input.addEventListener("input", sync);
      input.addEventListener("change", sync);
      sync();
    });
  }

  // ---------------------------------------------------------------------------
  // Clickable table rows
  // ---------------------------------------------------------------------------
  //
  // A tr.row-link navigates to its .row-anchor's href when clicked anywhere that is
  // not itself a link or button, so the whole row is a target while the real anchor
  // stays the single keyboard-focusable control.

  function initRowLinks() {
    document.querySelectorAll("tr.row-link").forEach(function (row) {
      if (row.dataset.rowReady) return;
      row.dataset.rowReady = "true";
      var anchor = row.querySelector("a.row-anchor");
      if (!anchor) return;
      row.addEventListener("click", function (evt) {
        if (evt.defaultPrevented) return;
        if (evt.target.closest("a, button, input, select, textarea, form")) return;
        if (window.getSelection && String(window.getSelection())) return;
        window.location.href = anchor.href;
      });
    });
  }

  function initContent() {
    initRowLinks();
    initPreview();
    initTokenSecret();
    initCopyButtons();
    initSwatches();
  }

  document.addEventListener("DOMContentLoaded", function () {
    configureHtmx();
    configureResponses();
    initConfirm();
    initContent();
    document.addEventListener("htmx:load", initContent);
  });
})();
