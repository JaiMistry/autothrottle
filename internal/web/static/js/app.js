/* The app's only front-end code. Everything else is server-rendered and
   swapped by HTMX; this covers the handful of behaviours that genuinely
   need a client: theme, the rail, the drawer, and the measured row cap.
*/
(function () {
  "use strict";

  var root = document.documentElement;

  /* ── Theme ──────────────────────────────────────────────────────
     Three modes, cycled. "System" is the absence of data-theme so
     prefers-color-scheme applies — setting it unconditionally, the way
     daisyUI usually does, would make System impossible to express. */
  var MODES = ["system", "light", "dark"];

  function applyTheme(mode) {
    if (mode === "light" || mode === "dark") root.setAttribute("data-theme", mode);
    else root.removeAttribute("data-theme");
    root.setAttribute("data-theme-mode", mode);
    try { localStorage.setItem("at-theme", mode); } catch (e) {}
  }

  document.addEventListener("click", function (e) {
    var toggle = e.target.closest("[data-theme-cycle]");
    if (!toggle) return;
    var current = root.getAttribute("data-theme-mode") || "system";
    applyTheme(MODES[(MODES.indexOf(current) + 1) % MODES.length]);
  });

  /* ── Sidebar rail ───────────────────────────────────────────────── */
  var shell = document.getElementById("at-shell");
  if (shell) {
    var collapsed = false;
    try { collapsed = localStorage.getItem("at-rail") === "1"; } catch (e) {}
    shell.setAttribute("data-collapsed", collapsed ? "1" : "0");
  }
  document.addEventListener("click", function (e) {
    if (!e.target.closest("[data-rail-toggle]") || !shell) return;
    var next = shell.getAttribute("data-collapsed") === "1" ? "0" : "1";
    shell.setAttribute("data-collapsed", next);
    try { localStorage.setItem("at-rail", next); } catch (e2) {}
  });

  /* ── Mobile drawer ──────────────────────────────────────────────
     Mounted always and translated off-canvas: a conditionally rendered
     drawer has nothing to transition from and snaps open. */
  function setDrawer(open) {
    document.body.setAttribute("data-drawer", open ? "1" : "0");
    var scrim = document.querySelector(".at-drawer-scrim");
    if (scrim) scrim.hidden = !open;
    var opener = document.querySelector("[data-drawer-open]");
    if (opener) opener.setAttribute("aria-expanded", open ? "true" : "false");
  }
  document.addEventListener("click", function (e) {
    if (e.target.closest("[data-drawer-open]")) { setDrawer(true); return; }
    if (e.target.closest("[data-drawer-close]")) setDrawer(false);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") setDrawer(false);
  });

  /* ── Two-row cap ────────────────────────────────────────────────
     The Dashboard's list cards show two rows and scroll past that, so a
     third downloader never pushes the activity log off the fold. Row
     height varies with how the reason line and rate group wrap, so this
     has to be measured rather than set as a CSS max-height. */
  function capList(list) {
    var rows = list.querySelectorAll(":scope > *");
    if (rows.length <= 2) { list.style.maxHeight = ""; list.style.overflowY = ""; return; }
    var h = rows[0].offsetHeight + rows[1].offsetHeight;
    if (!h) return;
    list.style.maxHeight = h + "px";
    list.style.overflowY = "auto";
  }

  var observer = window.ResizeObserver ? new ResizeObserver(function (entries) {
    entries.forEach(function (entry) { capList(entry.target); });
  }) : null;

  function bindCaps(scope) {
    (scope || document).querySelectorAll("[data-cap-rows]").forEach(function (list) {
      capList(list);
      if (observer) observer.observe(list);
    });
  }

  /* ── HTMX wiring ────────────────────────────────────────────────── */
  document.addEventListener("DOMContentLoaded", function () { bindCaps(document); });

  document.body.addEventListener("htmx:afterSwap", function (e) {
    // Caps have to be re-measured: the swapped rows are new elements the
    // old observer is no longer watching.
    bindCaps(e.detail.target);

    // A swap that replaces the focused element drops focus to <body>,
    // which strands a keyboard user mid-page. Put it back on whatever
    // they pressed.
    var trigger = e.detail.requestConfig && e.detail.requestConfig.elt;
    if (trigger && document.activeElement === document.body && document.contains(trigger)) {
      trigger.focus({ preventScroll: true });
    }
  });

  // ApexCharts instances hold a canvas and their own resize listeners.
  // Swapping their container without destroying them first leaks both
  // and leaves stacked charts behind.
  document.body.addEventListener("htmx:beforeSwap", function (e) {
    if (!window.ATCharts) return;
    Object.keys(window.ATCharts).forEach(function (id) {
      var el = document.getElementById(id);
      if (el && e.detail.target.contains(el)) {
        try { window.ATCharts[id].destroy(); } catch (err) {}
        delete window.ATCharts[id];
      }
    });
  });
})();
