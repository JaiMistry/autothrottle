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

  /* ── Sidebar rail ───────────────────────────────────────────────
     The blocking head script has already applied the stored state to
     <html>, so the rail is the right width on the first paint and
     never animates itself into position on load. This only handles
     the toggle. */
  document.addEventListener("click", function (e) {
    if (!e.target.closest("[data-rail-toggle]")) return;
    var collapsed = root.getAttribute("data-rail") === "collapsed";
    if (collapsed) root.removeAttribute("data-rail");
    else root.setAttribute("data-rail", "collapsed");
    try { localStorage.setItem("at-rail", collapsed ? "0" : "1"); } catch (e2) {}
  });

  /* Release the transition suppression once a frame has been painted. */
  requestAnimationFrame(function () {
    requestAnimationFrame(function () { root.classList.remove("at-boot"); });
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
    startUptime();

  document.body.addEventListener("htmx:afterSwap", function (e) {
    startUptime();
    syncPlexPanel();
    // A swapped-in pane brings its own inputs; re-apply which of them
    // are live before anything can submit a stale blank.
    document.querySelectorAll("form").forEach(syncCredentialUI);
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

/* ── Credential method sub-tabs ───────────────────────────────────
   Plex accepts either a pasted token or an account sign-in; Emby and
   Jellyfin only take a key, so the choice is hidden for them rather
   than offered and then rejected. */
(function () {
  "use strict";

  function syncCredentialUI(form) {
    if (!form) return;
    var type = form.querySelector('select[name="type"]');
    if (!type || !form.querySelector("[data-credgroup]")) return;
    var isPlex = type.value === "plex";
    form.querySelectorAll("[data-credgroup]").forEach(function (group) {
      group.hidden = group.dataset.credgroup !== (isPlex ? "plex" : "apikey");
    });
    if (isPlex) {
      var active = form.querySelector('[data-credtab][aria-selected="true"]');
      showPane(form, active ? active.dataset.credtab : "token");
    } else {
      // Put the token pane back. The sign-in flow is Plex-only, so
      // leaving it selected would show a control that cannot work.
      form.querySelectorAll("[data-credtab]").forEach(function (t) {
        t.setAttribute("aria-selected", String(t.dataset.credtab === "token"));
      });
      showPane(form, "token");
    }
    syncDisabled(form);
  }

  // A hidden input still submits, so anything the user cannot see is
  // disabled outright. Without this a blank field from the pane they are
  // not using rides along with the one they filled in.
  function syncDisabled(form) {
    form.querySelectorAll("[data-credgroup], [data-credpane]").forEach(function (box) {
      var off = box.hidden || !!box.closest("[data-credgroup][hidden]");
      box.querySelectorAll("input, select, textarea").forEach(function (el) {
        el.disabled = off;
      });
    });
  }

  function showPane(form, name) {
    form.querySelectorAll("[data-credpane]").forEach(function (pane) {
      pane.hidden = pane.dataset.credpane !== name;
    });
    syncDisabled(form);
  }

  // Uptime counts forward in the page. The server sends the boot
  // timestamp and the first frame; recomputing from the timestamp keeps
  // it right across a tab that was asleep, which incrementing would not.
  function startUptime() {
    var el = document.querySelector("[data-uptime-since]");
    if (!el) return;
    var boot = Number(el.dataset.uptimeSince);
    if (!boot) return;
    var pad = function (n) { return String(n).padStart(2, "0"); };
    var tick = function () {
      if (!el.isConnected) { clearInterval(timer); return; }
      var secs = Math.max(0, Math.floor((Date.now() - boot) / 1000));
      el.textContent =
        Math.floor(secs / 86400) + "d : " + pad(Math.floor((secs % 86400) / 3600)) +
        "h : " + pad(Math.floor((secs % 3600) / 60)) + "m : " + pad(secs % 60) + "s";
    };
    var timer = setInterval(tick, 1000);
    tick();
  }

  // Plex sign-in runs in a popup the browser will only allow if it is
  // opened inside the click itself. The auth URL is not known until the
  // server answers, so the window is opened blank and pointed at Plex
  // once the panel comes back carrying the URL.
  var plexWindow = null;
  var plexWatch = null;

  function stopPlexWatch() {
    if (plexWatch) { clearInterval(plexWatch); plexWatch = null; }
  }

  function watchPlexWindow() {
    stopPlexWatch();
    plexWatch = setInterval(function () {
      if (plexWindow && !plexWindow.closed) return;
      stopPlexWatch();
      // One poll cycle of grace: approving can close the window a moment
      // before the poll that collects the token comes back, and calling
      // that a failure would throw away a sign-in that worked.
      setTimeout(function () {
        var panel = document.querySelector('.at-plexsignin[data-state="waiting"]');
        if (!panel) return;
        var form = panel.closest("form");
        var csrf = form && form.querySelector('[name="csrf_token"]');
        window.htmx.ajax("POST", "/configuration/plex-signin/cancel", {
          target: "#plex-signin",
          swap: "innerHTML",
          values: csrf ? { csrf_token: csrf.value } : {},
        });
      }, 3000);
    }, 700);
  }

  document.addEventListener("click", function (e) {
    if (!e.target.closest("[data-plexstart]")) return;
    plexWindow = window.open("", "at-plex-signin", "width=620,height=760,noopener=no");
  });

  function syncPlexPanel() {
    var panel = document.querySelector(".at-plexsignin");
    if (!panel) return;
    var url = panel.dataset.authurl;
    if (url && plexWindow && !plexWindow.closed) {
      plexWindow.location = url;
      watchPlexWindow();
    }
    if (panel.dataset.state !== "waiting") {
      stopPlexWatch();
      if (panel.dataset.state === "done" && plexWindow && !plexWindow.closed) plexWindow.close();
    }
  }

  // A connection test shows its pending state the instant it starts.
  // The server only answers when the probe finishes, so without this the
  // box would appear already-resolved and the test would look instant.
  function showTesting(elt) {
    var form = elt.closest("form");
    var sel = elt.getAttribute("hx-target") || (form && form.getAttribute("hx-target"));
    if (!sel) return;
    var box = document.querySelector(sel);
    var tpl = document.getElementById("at-tpl-testing");
    if (!box || !tpl) return;
    var node = tpl.content.firstElementChild.cloneNode(true);
    var target = (form && form.dataset.testtarget) || "";
    if (!target && form) {
      var url = form.querySelector('[name="url"]');
      target = url ? url.value : "";
    }
    if (target) {
      var slot = node.querySelector(".at-testresult-target");
      if (slot) slot.textContent = target;
      else {
        slot = document.createElement("span");
        slot.className = "at-testresult-target";
        slot.textContent = target;
        node.querySelector(".at-testresult-head").appendChild(slot);
      }
    }
    box.replaceChildren(node);
  }

  document.body.addEventListener("htmx:beforeRequest", function (e) {
    var elt = e.detail.elt;
    if (elt && elt.closest("[data-testtarget], form") && /\/test$/.test(e.detail.requestConfig.path || "")) {
      showTesting(elt);
    }
  });

  // Dismissing leaves nothing behind — no spacer, no residue.
  document.addEventListener("click", function (e) {
    var close = e.target.closest("[data-testdismiss]");
    if (!close) return;
    var box = close.closest(".at-testresult");
    if (box) box.remove();
  });

  document.addEventListener("click", function (e) {
    var tab = e.target.closest("[data-credtab]");
    if (!tab) return;
    var form = tab.closest("form");
    form.querySelectorAll("[data-credtab]").forEach(function (t) {
      t.setAttribute("aria-selected", String(t === tab));
    });
    showPane(form, tab.dataset.credtab);
  });

  document.addEventListener("change", function (e) {
    if (e.target.name === "type") syncCredentialUI(e.target.closest("form"));
  });

  document.addEventListener("DOMContentLoaded", function () {
    document.querySelectorAll("form").forEach(syncCredentialUI);
  });
})();
