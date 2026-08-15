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
// the vertical line, horizontal line, dots and label always sit on a real
// snapshot — nothing is interpolated client-side. The graph itself still
// lerps between adjacent snapshots (polyline segments), except across time
// gaps that are too large, where the server renders discrete dots instead
// (see gapBreaks in cmd/server/charts.go). DOM-safe (textContent, no HTML
// from data).
(function () {
  "use strict";
  var NS = "http://www.w3.org/2000/svg";
  var PAD = 10, W = 1200, H = 200;

  var svg = document.querySelector("svg.balance");
  var dataEl = document.getElementById("balance-data");
  if (!svg || !dataEl) return;

  var data;
  try { data = JSON.parse(dataEl.textContent); } catch (e) { return; }
  if (!data.series || data.series.length === 0) return;

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
  function nearest(si, xb) {
    var pts = data.series[si].points;
    var n = pts.length;
    if (n === 0) return null;
    var i = Math.round(((xb - PAD) / (W - 2 * PAD)) * (n - 1));
    i = Math.min(Math.max(0, i), n - 1);
    var p = pts[i];
    return { cx: xFor(i, n), cy: yFor(p.minor), minor: p.minor, date: p.date };
  }

  // Crosshair group: reuse the template's placeholder <g class="crosshair"
  // hidden> — it starts hidden so nothing renders at (0,0) on load; the
  // children (vertical line, horizontal line at the primary series' snapped
  // point, one dot per series) appear only once the mouse enters the svg.
  var g = svg.querySelector(".crosshair");
  if (!g) return;
  var vline = document.createElementNS(NS, "line");
  g.appendChild(vline);
  var hline = document.createElementNS(NS, "line");
  g.appendChild(hline);
  var dots = data.series.map(function () {
    var c = document.createElementNS(NS, "circle");
    c.setAttribute("r", "3.5");
    g.appendChild(c);
    return c;
  });

  var wrap = svg.parentElement;
  var label = wrap.querySelector(".cursor-label");
  if (!label) return;

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

  function show(xRatio, px, py) {
    var xb = PAD + xRatio * (W - 2 * PAD); // viewBox x (same mapping as svgCoord)
    var primary = nearest(0, xb);
    if (!primary) { hide(); return; }
    var cx = primary.cx; // snap the whole cursor to the primary data point
    vline.setAttribute("x1", cx);
    vline.setAttribute("y1", PAD);
    vline.setAttribute("x2", cx);
    vline.setAttribute("y2", H - PAD);

    var entries = data.series.map(function (_, si) { return nearest(si, cx); });
    entries.forEach(function (e, si) {
      dots[si].setAttribute("cx", String(e ? e.cx : cx));
      dots[si].setAttribute("cy", String(e ? e.cy : PAD));
      dots[si].setAttribute("data-minor", String(e ? e.minor : 0));
    });
    hline.removeAttribute("hidden");
    hline.setAttribute("x1", String(PAD));
    hline.setAttribute("y1", String(primary.cy));
    hline.setAttribute("x2", String(W - PAD));
    hline.setAttribute("y2", String(primary.cy));
    var date = "";
    for (var i = entries.length - 1; i >= 0; i--) {
      if (entries[i] && entries[i].date) { date = entries[i].date; break; }
    }
    buildLabel(entries, date);
    label.removeAttribute("hidden");
    // Keep the label inside the panel.
    var lw = label.offsetWidth || 160, lh = label.offsetHeight || 60;
    var lx = Math.min(px + 12, wrap.clientWidth - lw - 6);
    var ly = Math.min(py + 14, wrap.clientHeight - lh - 6);
    label.style.left = Math.max(6, lx) + "px";
    label.style.top = Math.max(6, ly) + "px";
  }

  function hide() {
    g.setAttribute("hidden", "");
    label.setAttribute("hidden", "");
  }

  svg.addEventListener("mousemove", function (e) {
    var rect = svg.getBoundingClientRect();
    var x = e.clientX - rect.left;
    if (x < 0 || x > rect.width) { hide(); return; }
    g.removeAttribute("hidden");
    show(Math.min(1, Math.max(0, x / rect.width)), e.clientX - wrap.getBoundingClientRect().left, e.clientY - wrap.getBoundingClientRect().top);
  });
  svg.addEventListener("mouseleave", hide);
})();
