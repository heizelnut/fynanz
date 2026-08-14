// Click-to-copy for elements marked .copyable (mono identifier/text fields:
// merchant names, IBANs, descriptions, errors). Full-page delegation so
// HTMX-swapped rows get the behavior automatically. Prefers the async
// clipboard API (secure contexts incl. localhost/Tailscale-TLS) and falls
// back to execCommand for plain-HTTP LAN access.
(function () {
  "use strict";

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    return new Promise(function (resolve, reject) {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      try {
        document.execCommand("copy");
        resolve();
      } catch (e) {
        reject(e);
      }
      document.body.removeChild(ta);
    });
  }

  document.addEventListener("click", function (e) {
    var el = e.target.closest(".copyable");
    if (!el) return;
    // Prefer an explicit data-copy target, else the cell's own text.
    var text = (el.dataset.copy || el.textContent || "").trim();
    if (!text) return;
    copyText(text).then(function () {
      el.classList.add("copied");
      setTimeout(function () { el.classList.remove("copied"); }, 1200);
    }).catch(function () { /* clipboard unavailable — nothing sensible to do */ });
  });
})();
