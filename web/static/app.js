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

// Crosshair for the balance chart. It sticks to EXISTING data points only:
// the vertical/horizontal lines and per-series markers always sit on a real
// snapshot — no interpolation. Geometry is computed in the chart's 1200x200
// viewBox space (same mapping as the server), then converted to CSS pixels
// and drawn in the OVERLAY svg.cursor, which has no viewBox of its own — so
// its stroke-dasharray dashes and marker dots stay constant on screen even
// though the chart svg is stretched by height:100% + preserveAspectRatio
// "none". DOM-safe (textContent, no HTML from data).
(function () {
  "use strict";
  var PAD = 10, W = 1200, H = 200;

  var svg = document.querySelector("svg.balance");
  var overlay = document.querySelector("svg.cursor");
  var dataEl = document.getElementById("balance-data");
  if (!svg || !overlay || !dataEl) return;

  var data;
  try { data = JSON.parse(dataEl.textContent); } catch (e) { return; }
  if (!data.series || data.series.length === 0) return;

  var vline = overlay.querySelector(".cur-v");
  var hline = overlay.querySelector(".cur-h");
  var dots = [].slice.call(overlay.querySelectorAll(".cur-dots"));
  if (!vline || !hline || dots.length !== data.series.length) return;

  var wrap = svg.parentElement;
  var label = wrap.querySelector(".cursor-label");
  if (!label) return;

  var scale = 2; // minor units per whole unit (EUR etc.); fine for v1
  function fmtAmt(minor) {
    var neg = minor < 0, s = String(Math.abs(minor));
    while (s.length <= scale) s = "0" + s;
    var out = s.slice(0, -scale) + "." + s.slice(-scale);
    return (neg ? "-" : "") + out + " " + data.currency;
  }
  // Same mappings the server uses to place vertices (svgCoord in charts.go)
  // in the fixed 1200x200 viewBox — no client-side geometry adaptation.
  function xFor(i, n) {
    return PAD + i * ((W - 2 * PAD) / Math.max(1, n - 1));
  }
  function yFor(minor) {
    if (data.max > data.min) {
      return PAD + (data.max - minor) * ((H - 2 * PAD) / (data.max - data.min));
    }
    return H / 2;
  }

  // nearest returns the actual data point on series si closest to xb: the
  // nearest vertex by index. No interpolation — values are always real.
  // Returned coordinates are viewBox units (0..1200 x 0..200).
  function nearest(si, xb) {
    var pts = data.series[si].points;
    var n = pts.length;
    if (n === 0) return null;
    var i = Math.round(((xb - PAD) / (W - 2 * PAD)) * (n - 1));
    i = Math.min(Math.max(0, i), n - 1);
    var p = pts[i];
    return { cx: xFor(i, n), cy: yFor(p.minor), minor: p.minor, date: p.date };
  }

  // v2p maps viewBox units to CSS pixels on the overlay: the overlay fills
  // the same box as the (stretched) chart svg, so sx/sy are its rect/1200,
  // rect/200. Kept fresh per event so resize never desyncs the cursor.
  var sx = 1, sy = 1;
  function frame() {
    var r = svg.getBoundingClientRect();
    sx = r.width / W; sy = r.height / H;
  }
  function px(v, axis) { return (axis === "x" ? v * sx : v * sy).toFixed(1); }

  function placeDot(si, vx, vy) {
    var ex = px(vx, "x"), ey = px(vy, "y");
    // Invisible 24px anchor segment ending exactly at the point: marker-end
    // renders the series' dot there in real px (Chromium skips markers on
    // short segments; .cur-dots has no stroke/fill so it stays invisible).
    dots[si].setAttribute("points", (parseFloat(ex) - 24).toFixed(2) + "," + ey + " " + ex + "," + ey);
  }

  function buildLabel(entries, date) {
    label.textContent = "";
    var d = document.createElement("div");
    d.className = "cl-date";
    d.textContent = date;
    label.appendChild(d);
    entries.forEach(function (e, si) {
      if (!e) return;
      var rowEl = document.createElement("div");
      rowEl.className = "cl-row";
      var sw = document.createElement("span");
      sw.className = "cl-sw";
      if (data.series[si].cls) sw.style.background = data.series[si].cls;
      var name = document.createElement("span");
      name.textContent = data.series[si].label;
      var amt = document.createElement("span");
      amt.className = "cl-amt";
      amt.textContent = fmtAmt(e.minor);
      rowEl.appendChild(sw); rowEl.appendChild(name); rowEl.appendChild(amt);
      label.appendChild(rowEl);
    });
  }

  // Vertical/horizontal end coordinates in px: y ranges over the chart
  // area (PAD..H-PAD in viewBox space) converted by sy; x over PAD..W-PAD.
  var vx1 = 0, vx2 = 0, vy1 = 0, vy2 = 0;
  var hx1 = 0, hx2 = 0, hy = 0;

  function show(xRatio, pxPos, pyPos) {
    var xb = PAD + xRatio * (W - 2 * PAD); // viewBox x (same mapping as svgCoord)
    var primary = nearest(0, xb);
    if (!primary) { hide(); return; }
    var cx = primary.cx; // snap the whole cursor to the primary data point
    frame();

    vx1 = vx2 = px(cx, "x");
    vy1 = px(PAD, "y"); vy2 = px(H - PAD, "y");
    vline.setAttribute("x1", vx1); vline.setAttribute("y1", vy1);
    vline.setAttribute("x2", vx2); vline.setAttribute("y2", vy2);

    var entries = data.series.map(function (_, si) { return nearest(si, cx); });
    entries.forEach(function (e, si) {
      placeDot(si, e ? e.cx : primary.cx, e ? e.cy : PAD);
    });
    hx1 = px(PAD, "x"); hx2 = px(W - PAD, "x"); hy = px(primary.cy, "y");
    hline.setAttribute("x1", hx1); hline.setAttribute("y1", hy);
    hline.setAttribute("x2", hx2); hline.setAttribute("y2", hy);

    var date = "";
    for (var i = entries.length - 1; i >= 0; i--) {
      if (entries[i] && entries[i].date) { date = entries[i].date; break; }
    }
    buildLabel(entries, date);
    label.removeAttribute("hidden");
    // Keep the label inside the panel.
    var lw = label.offsetWidth || 160, lh = label.offsetHeight || 60;
    var lx = Math.min(pxPos + 12, wrap.clientWidth - lw - 6);
    var ly = Math.min(pyPos + 14, wrap.clientHeight - lh - 6);
    label.style.left = Math.max(6, lx) + "px";
    label.style.top = Math.max(6, ly) + "px";
  }

  function hide() {
    overlay.setAttribute("hidden", "");
    label.setAttribute("hidden", "");
  }

  svg.addEventListener("mousemove", function (e) {
    var rect = svg.getBoundingClientRect();
    var x = e.clientX - rect.left;
    if (x < 0 || x > rect.width) { hide(); return; }
    overlay.removeAttribute("hidden");
    show(Math.min(1, Math.max(0, x / rect.width)), e.clientX - wrap.getBoundingClientRect().left, e.clientY - wrap.getBoundingClientRect().top);
  });
  svg.addEventListener("mouseleave", hide);
})();

// Flash alerts (server-rendered from ?msg=/?err=): dismiss on × click or
// automatically after a few seconds — removed immediately, no animation.
// Delegated so any flash swapped in later by HTMX behaves identically.
// If the user INTERACTS with the alert (press before selecting text for
// copy-paste, keyboard focus), the auto-dismiss is cancelled — removal
// while selecting would kill the selection. The × button still dismisses.
(function () {
  "use strict";
  var AUTO_MS = 6000;

  function dismiss(flash) {
    if (!flash || flash.dataset.dismissing) return;
    flash.dataset.dismissing = "1";
    flash.remove();
  }

  function cancelAuto(flash) {
    if (flash._autoTimer) {
      clearTimeout(flash._autoTimer);
      flash._autoTimer = null;
    }
  }

  document.addEventListener("click", function (e) {
    var btn = e.target.closest(".flash-close");
    if (btn) dismiss(btn.parentElement);
  });

  function arm(flash) {
    if (flash.dataset.armed) return;
    flash.dataset.armed = "1";
    flash._autoTimer = setTimeout(function () { dismiss(flash); }, AUTO_MS);
    // Any interaction cancels the auto timer (mouse/touch press starts
    // selection; focus is keyboard interaction).
    flash.addEventListener("pointerdown", function () { cancelAuto(flash); });
    flash.addEventListener("touchstart", function () { cancelAuto(flash); });
    flash.addEventListener("focusin", function () { cancelAuto(flash); });
  }

  document.querySelectorAll(".flash").forEach(arm);

  // Flashes that arrive after load (HTMX partial swaps) get armed on insert.
  if (window.MutationObserver) {
    var mo = new MutationObserver(function (muts) {
      for (var m = 0; m < muts.length; m++) {
        var added = muts[m].addedNodes;
        for (var i = 0; i < added.length; i++) {
          var n = added[i];
          if (n.nodeType !== 1) continue;
          if (n.classList && n.classList.contains("flash")) arm(n);
          var inner = n.querySelectorAll ? n.querySelectorAll(".flash") : [];
          for (var j = 0; j < inner.length; j++) arm(inner[j]);
        }
      }
    });
    mo.observe(document.body, { childList: true, subtree: true });
  }
})();
