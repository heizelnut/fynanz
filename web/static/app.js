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

// Grafana-style crosshair for the balance chart: reads the series data the
// server embedded next to the svg (balance-data), draws a dashed cursor line
// + per-series dots at the nearest point, and shows a floating label with
// date and each account's balance. DOM-safe (textContent, no HTML from data).
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
  function yFor(minor) {
    if (data.max > data.min) {
      return PAD + (data.max - minor) * ((H - 2 * PAD) / (data.max - data.min));
    }
    return H / 2;
  }

  // Crosshair group + one dot per series.
  var g = document.createElementNS(NS, "g");
  g.setAttribute("class", "crosshair");
  var line = document.createElementNS(NS, "line");
  g.appendChild(line);
  var dots = data.series.map(function () {
    var c = document.createElementNS(NS, "circle");
    c.setAttribute("r", "3.5");
    g.appendChild(c);
    return c;
  });
  svg.appendChild(g);

  var wrap = svg.parentElement;
  var label = wrap.querySelector(".cursor-label");
  if (!label) return;

  function pick(si, ratio) {
    var pts = data.series[si].points;
    if (pts.length === 0) return null;
    var i = Math.round(ratio * (pts.length - 1));
    i = Math.min(Math.max(0, i), pts.length - 1);
    return pts[i];
  }

  function buildLabel(date) {
    function row(si) {
      var p = pick(si, lastRatio);
      if (!p) return null;
      var rowEl = document.createElement("div");
      rowEl.className = "cl-row";
      var sw = document.createElement("span");
      sw.className = "cl-sw";
      if (data.series[si].cls) sw.style.background = data.series[si].cls;
      var name = document.createElement("span");
      name.textContent = data.series[si].label;
      var amt = document.createElement("span");
      amt.className = "cl-amt";
      amt.textContent = fmtAmt(p.minor);
      rowEl.appendChild(sw); rowEl.appendChild(name); rowEl.appendChild(amt);
      return rowEl;
    }
    var d = document.createElement("div");
    d.className = "cl-date";
    d.textContent = date;
    label.textContent = "";
    label.appendChild(d);
    data.series.forEach(function (_, si) {
      var r = row(si);
      if (r) label.appendChild(r);
    });
  }

  var lastRatio = 0;
  function show(xRatio, px, py) {
    lastRatio = xRatio;
    var xb = PAD + xRatio * (W - 2 * PAD);
    line.setAttribute("x1", xb);
    line.setAttribute("y1", PAD);
    line.setAttribute("x2", xb);
    line.setAttribute("y2", H - PAD);
    var dates = [];
    data.series.forEach(function (s, si) {
      var p = pick(si, xRatio);
      dots[si].setAttribute("cx", xb);
      dots[si].setAttribute("cy", yFor(p.minor));
      dots[si].setAttribute("data-minor", String(p.minor));
      if (p && dates.indexOf(p.date) < 0) dates.push(p.date);
    });
    buildLabel(dates.length ? dates[dates.length - 1] : "");
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
