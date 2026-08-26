/* Analytics charts. Options lifted from the design's prototype; the
   values here are deliberate and several of them are load-bearing.

   window.ATCharts holds live instances so app.js can destroy them
   before HTMX swaps their container — otherwise every swap leaks a
   canvas and leaves stacked charts behind. */
(function () {
  "use strict";
  window.ATCharts = window.ATCharts || {};

  var payloadEl = document.getElementById("at-chart-data");
  if (!payloadEl || typeof ApexCharts === "undefined") return;

  var series;
  try { series = JSON.parse(payloadEl.textContent); } catch (e) { return; }
  if (!series || !series.down || !series.down.length) return;

  function isDark() {
    var explicit = document.documentElement.getAttribute("data-theme");
    if (explicit) return explicit === "dark";
    return window.matchMedia("(prefers-color-scheme: dark)").matches;
  }

  /* Rates arrive as bytes/sec; limits were KB/s and are converted
     server-side. Both read out in KB/s or MB/s. */
  function rate(b) {
    if (b == null) return "—";
    return b >= 1024 * 900
      ? (b / 1048576).toFixed(1) + " MB/s"
      : Math.round(b / 1024).toLocaleString() + " KB/s";
  }

  // Axis labels follow the span actually drawn, not the range that was
  // asked for. Now that a range only sets a ceiling and the axis fits
  // the history that exists, a 30d view of an install four hours old
  // draws four hours — and labelling that "26 Aug, 26 Aug, 26 Aug" says
  // nothing. Under two days the clock is the useful part; past that the
  // date is.
  function axisFormat(withTime) {
    var span = series.to - series.from;
    if (span <= 48 * 3600 * 1000) return "HH:mm";
    return withTime ? "d MMM HH:mm" : "d MMM";
  }

  function theme(dark) {
    return {
      chart: {
        toolbar: { show: false },
        zoom: { enabled: false },
        fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
        background: "transparent",
        animations: { enabled: true, easing: "easeout", speed: 320 }
      },
      grid: { borderColor: dark ? "#232f45" : "#d7dee7", strokeDashArray: 0, padding: { left: 4, right: 4 } },
      tooltip: { theme: dark ? "dark" : "light" },
      legend: { show: false },
      dataLabels: { enabled: false }
    };
  }
  function axis(dark) { return { colors: dark ? "#5c6a80" : "#8593a5", fontSize: "12px" }; }

  /* Sentinel fill: these rects are recoloured to a diagonal hatch after
     render, so absence of data never reads as a muted variant of the
     throttle band. They mean opposite things. */
  var GAPFILL = "#00fe01";

  function throughput(el, dark) {
    var t = theme(dark);
    var hasLimit = (series.limit || []).some(function (p) { return p.y != null; });

    var bands = (series.bands || []).map(function (b) {
      return { x: b.from, x2: b.to, fillColor: dark ? "#f0b132" : "#c98410", opacity: 0.1, borderColor: "transparent" };
    });

    var gaps = (series.gaps || []).map(function (g) {
      // A 54-minute outage is under a pixel wide on a 30-day axis. Floor
      // the band to the plotting stride so the break the line already
      // shows has something legible behind it.
      var mid = (g.from + g.to) / 2;
      var half = Math.max((g.to - g.from) / 2, series.bucket * 0.6);
      return {
        x: Math.max(mid - half, series.from),
        x2: Math.min(mid + half, series.to),
        fillColor: GAPFILL, opacity: 1, borderColor: "transparent"
      };
    });

    // No label key at all. An xaxis annotation carrying `label:
    // undefined` makes ApexCharts silently drop the chart's entire
    // tooltip — no console error, nothing.
    var nowLine = { x: series.to, strokeDashArray: 3, borderColor: dark ? "#5c6a80" : "#8593a5" };

    var data = [{ name: "Download", type: "line", data: series.down }];
    var widths = [2.2], dashes = [0], colors = ["#38bdf8"];
    data.push({ name: "Upload", type: "line", data: series.up });
    // #4ade80 at 1.4px reads as a highlighter smudge on a light ground.
    widths.push(1.4); dashes.push(0); colors.push(dark ? "#4ade80" : "#1f9d55");
    if (hasLimit) {
      data.push({ name: "Limit in effect", type: "line", data: series.limit });
      widths.push(1.5); dashes.push(5); colors.push(dark ? "#f0b132" : "#c98410");
    }
    var limitIdx = hasLimit ? data.length - 1 : -1;

    var chart = new ApexCharts(el, Object.assign({}, t, {
      series: data,
      chart: Object.assign({}, t.chart, { type: "line", height: 250 }),
      colors: colors,
      stroke: {
        curve: hasLimit ? ["smooth", "smooth", "stepline"] : ["smooth", "smooth"],
        width: widths, dashArray: dashes
      },
      annotations: { xaxis: bands.concat(gaps).concat([nowLine]) },
      xaxis: {
        type: "datetime", min: series.from, max: series.to, tickAmount: 6,
        labels: { datetimeUTC: false, format: axisFormat(false), style: axis(dark) },
        axisBorder: { show: false }, axisTicks: { show: false }
      },
      yaxis: { min: 0, labels: { formatter: rate, style: axis(dark) } },
      tooltip: Object.assign({}, t.tooltip, {
        shared: true,
        x: { format: axisFormat(true) },
        y: {
          formatter: function (v, o) {
            if (v != null) return rate(v);
            // A null limit means no ceiling was in force, which is not
            // the same as having no sample. Only a real gap — where
            // download is null too — reads "no sample".
            var p = o && o.w && o.w.config.series[0].data[o.dataPointIndex];
            var missing = !p || p.y == null;
            return o && o.seriesIndex === limitIdx && !missing ? "Unlimited" : "no sample";
          }
        }
      }),
      markers: { size: 0 }
    }));

    /* The gap bands are drawn in a sentinel colour and re-painted as a
       diagonal hatch once the SVG exists. ApexCharts redraws its
       annotations on every resize — and the sidebar now animates its
       width, so that happens constantly — which restores the sentinel.
       Re-applying on each redraw is what keeps the band hatched instead
       of a flat block of #00fe01. */
    function applyHatch() {
      var svg = el.querySelector(".apexcharts-svg");
      if (!svg) return;
      var NS = "http://www.w3.org/2000/svg";
      var id = "at-gap-hatch-" + (dark ? "d" : "l");
      var ink = dark ? "#7d8ba1" : "#8593a5";
      var defs = svg.querySelector("defs");
      if (!defs) { defs = document.createElementNS(NS, "defs"); svg.insertBefore(defs, svg.firstChild); }
      if (!defs.querySelector("#" + id)) {
        var pat = document.createElementNS(NS, "pattern");
        pat.setAttribute("id", id);
        pat.setAttribute("width", "6"); pat.setAttribute("height", "6");
        pat.setAttribute("patternUnits", "userSpaceOnUse");
        pat.setAttribute("patternTransform", "rotate(45)");
        var line = document.createElementNS(NS, "line");
        line.setAttribute("x1", "0"); line.setAttribute("y1", "0");
        line.setAttribute("x2", "0"); line.setAttribute("y2", "6");
        line.setAttribute("stroke", ink);
        line.setAttribute("stroke-width", "1.4");
        line.setAttribute("stroke-opacity", "0.6");
        pat.appendChild(line);
        defs.appendChild(pat);
      }
      svg.querySelectorAll('.apexcharts-xaxis-annotations rect[fill="' + GAPFILL + '"]').forEach(function (r) {
        r.setAttribute("fill", "url(#" + id + ")");
        r.setAttribute("stroke", ink);
        r.setAttribute("stroke-width", "1");
        r.setAttribute("stroke-dasharray", "3 3");
        r.setAttribute("stroke-opacity", "0.75");
      });
    }

    chart.render().then(function () {
      applyHatch();
      // Belt and braces: the chart's own redraw hooks do not fire for
      // every path that repaints an annotation, so watch the box too.
      if (window.ResizeObserver) {
        var ro = new ResizeObserver(function () {
          requestAnimationFrame(applyHatch);
        });
        ro.observe(el);
      }
    });
    chart.addEventListener("updated", applyHatch);
    chart.addEventListener("mounted", applyHatch);
    return chart;
  }

  function sessions(el, dark) {
    var rows = series.sessions || [];
    if (!rows.length) return null;
    var names = [];
    rows.forEach(function (s) { if (names.indexOf(s.server) < 0) names.push(s.server); });

    var t = theme(dark);

    // A short session inside a long window is sub-pixel — a ten-minute
    // episode across 30 days is about a thousandth of the width, which
    // draws as nothing at all. Bars are widened to a visible floor so
    // the shape stays readable.
    //
    // The widening is a drawing decision, not a claim about time, so
    // the real start and end travel with each point and the tooltip
    // reads from those. Nothing the reader can see a number for is ever
    // the padded value.
    var minVisible = (series.to - series.from) * 0.004;

    var chart = new ApexCharts(el, Object.assign({}, t, {
      series: [{
        data: rows.map(function (s) {
          var start = s.start, end = s.end;
          if (end - start < minVisible) {
            end = start + minVisible;
            if (end > series.to) {           // ran up against the right edge
              end = series.to;
              start = Math.max(series.from, end - minVisible);
            }
          }
          return {
            x: s.server,
            y: [start, end],
            trueStart: s.start,
            trueEnd: s.end,
            ongoing: s.ongoing,
            // The ongoing session runs to the right edge in full accent
            // so it reads as still happening, not as merely recent.
            fillColor: s.ongoing ? "#38bdf8" : (dark ? "#2b6c8a" : "#8fd3f2")
          };
        })
      }],
      chart: Object.assign({}, t.chart, { type: "rangeBar", height: Math.max(96, names.length * 46 + 40) }),
      plotOptions: { bar: { horizontal: true, barHeight: "44%", rangeBarGroupRows: true, borderRadius: 3 } },
      annotations: { xaxis: [{ x: series.to, strokeDashArray: 3, borderColor: dark ? "#5c6a80" : "#8593a5" }] },
      xaxis: {
        type: "datetime", min: series.from, max: series.to, tickAmount: 6,
        labels: { datetimeUTC: false, format: axisFormat(false), style: axis(dark) },
        axisBorder: { show: false }, axisTicks: { show: false }
      },
      yaxis: { labels: { style: { colors: dark ? "#9aa8bc" : "#4a5768", fontSize: "13px" } } },
      tooltip: Object.assign({}, t.tooltip, {
        // Built by hand because the default reads the drawn y values,
        // which are padded. The markup reuses ApexCharts' own title
        // class so the themed box, border and shadow still apply.
        custom: function (o) {
          var d = o.w.config.series[o.seriesIndex].data[o.dataPointIndex];
          var body = clockTime(d.trueStart) + " – " + (d.ongoing ? "now" : clockTime(d.trueEnd));
          return '<div class="apexcharts-tooltip-title">' + escapeHTML(d.x) + "</div>" +
            '<div class="at-charttip">' + body +
            '<span class="at-charttip-dur">' + humanDuration(d.trueEnd - d.trueStart) + "</span></div>";
        }
      })
    }));
    chart.render();
    return chart;
  }

  function clockTime(ms) {
    var d = new Date(ms);
    return d.toLocaleDateString(undefined, { day: "numeric", month: "short" }) + " " +
      d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  }

  // Reads as "1h 05m" / "8m" — the length of the session, which is the
  // thing a padded bar can no longer be measured for by eye.
  function humanDuration(ms) {
    var mins = Math.max(0, Math.round(ms / 60000));
    if (mins < 60) return mins + "m";
    var h = Math.floor(mins / 60), m = mins % 60;
    return h + "h " + (m < 10 ? "0" : "") + m + "m";
  }

  function escapeHTML(v) {
    return String(v).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function build() {
    var dark = isDark();
    ["at-chart-throughput", "at-chart-sessions"].forEach(function (id) {
      if (window.ATCharts[id]) { try { window.ATCharts[id].destroy(); } catch (e) {} delete window.ATCharts[id]; }
    });
    var tp = document.getElementById("at-chart-throughput");
    if (tp) window.ATCharts["at-chart-throughput"] = throughput(tp, dark);
    var se = document.getElementById("at-chart-sessions");
    if (se) window.ATCharts["at-chart-sessions"] = sessions(se, dark);
  }

  build();

  // Both themes are first-class, and the charts carry their own colours
  // rather than inheriting them, so a theme change has to rebuild them.
  new MutationObserver(build).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", function () {
    if (!document.documentElement.getAttribute("data-theme")) build();
  });
})();
