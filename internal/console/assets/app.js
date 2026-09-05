// qurator console — one vanilla JS file, no build step, no external origins.
//
// Responsibilities:
//   1. Configure htmx defensively (no eval, same-origin only) and attach the CSRF
//      header to every htmx-issued request.
//   2. A debounced, cancellable live QR preview against /v1/qr.
//   3. A custom (non-native, non-hx-confirm) confirmation dialog for destructive
//      htmx actions, driven by the htmx:confirm event.
//   4. A show-once API token secret: copy to clipboard, then remove from the DOM.
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

    document.body.addEventListener("htmx:configRequest", function (evt) {
      evt.detail.headers[CSRF_HEADER] = CSRF_VALUE;

      // Optimistic-concurrency: a destination-edit form carries the version it was
      // rendered with in a data attribute; forward it as If-Match so a concurrent edit
      // loses the race with a 409 rather than silently clobbering.
      var versioned = evt.detail.elt.querySelector("[data-if-match]");
      if (versioned) {
        evt.detail.headers["If-Match"] = '"' + versioned.getAttribute("data-if-match") + '"';
      }
    });
  }

  // ---------------------------------------------------------------------------
  // Live styling preview
  // ---------------------------------------------------------------------------
  //
  // The preview calls the exact same ephemeral renderer the API exposes at
  // GET /v1/qr, so what the user sees while adjusting controls is guaranteed to match
  // the image a save would produce (research.md §5). Requests are debounced 150ms,
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
    if (!form || !img) return;

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

    function runPreview() {
      var params = buildQuery();
      if (!params.get("content")) return;
      var key = params.toString();
      var cached = previewCache.get(key);
      if (cached) {
        img.src = cached;
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
    document.body.addEventListener("htmx:confirm", function (evt) {
      var message = evt.detail.elt.getAttribute("data-confirm-message");
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
      dialog.showModal();
    });
  }

  // ---------------------------------------------------------------------------
  // Clipboard copy helper
  // ---------------------------------------------------------------------------
  //
  // Shared by the show-once token secret and the code detail page's storage URL. Falls
  // back to just running the completion callback when the Clipboard API is unavailable
  // (e.g. an insecure context), same as before this was factored out.

  function copyToClipboard(value, done) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(value).then(done, done);
    } else {
      done();
    }
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
    if (!button) return;
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
        button.disabled = true;
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
      var targetID = button.getAttribute("data-copy-target");
      var target = targetID ? document.getElementById(targetID) : null;
      if (!target) return;
      var originalLabel = button.textContent;

      button.addEventListener("click", function () {
        var value = "value" in target ? target.value : target.textContent;
        copyToClipboard(value || "", function () {
          button.textContent = "Copied";
          window.setTimeout(function () {
            button.textContent = originalLabel;
          }, 2000);
        });
      });
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    configureHtmx();
    initPreview();
    initConfirm();
    initTokenSecret();
    initCopyButtons();
  });
})();
