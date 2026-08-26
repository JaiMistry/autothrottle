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
    if (e.target.closest("[data-drawer-close]")) { setDrawer(false); return; }
    // Choosing a destination is the end of using the menu, so it closes
    // with the choice rather than staying open over the page arriving
    // behind it.
    if (e.target.closest(".at-rail a")) setDrawer(false);
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
    if (rows.length < 3) {
      list.style.maxHeight = "";
      list.style.overflowY = "";
      delete list.dataset.capFor;
      return;
    }

    // Measuring is only redone when the shape that determines the answer
    // changes. offsetWidth rather than clientWidth because clientWidth
    // shrinks by the width of the scrollbar this very function causes to
    // appear — which would make the signature flip between the capped
    // and uncapped states forever.
    // Session lines are counted too: a server that starts playing grows
    // its row without changing how many rows there are, and a signature
    // blind to that would keep serving a cap measured for the quiet
    // version. Timecodes ticking inside an existing line do not move it.
    var signature = rows.length + ":" + Math.round(list.offsetWidth) +
      ":" + list.querySelectorAll(".at-session").length;
    if (list.dataset.capFor === signature && list.style.maxHeight) return;

    // Measure with the cap lifted. The list lays its rows out with
    // grid-auto-rows: 1fr, so once a cap is applied the rows compress to
    // share it, and measuring them in that state feeds the previous
    // answer back in.
    //
    // This must not run on every observer callback: clearing the cap
    // resizes the rows, which notifies the observer, which clears it
    // again — a loop that repaints every frame and shows up as a
    // flickering scrollbar. The signature check above is what keeps it
    // to once per shape.
    var prevMax = list.style.maxHeight, prevOverflow = list.style.overflowY;
    var prevRows = list.style.gridAutoRows, prevAlign = list.style.alignContent;
    list.style.maxHeight = "";
    list.style.overflowY = "";
    // Rows normally share the card's height evenly (grid-auto-rows: 1fr),
    // which is right for display and wrong for measuring: uncapped, three
    // rows of 96/73/96 all report 96, so the target comes out a whole row
    // too tall and the cap then lands in the middle of the third row.
    // Measure them at their natural heights instead.
    list.style.gridAutoRows = "min-content";
    list.style.alignContent = "start";

    var heights = [].map.call(rows, function (row) { return row.getBoundingClientRect().height; });

    list.style.gridAutoRows = prevRows;
    list.style.alignContent = prevAlign;

    if (!heights[0] || !heights[1]) {
      list.style.maxHeight = prevMax;
      list.style.overflowY = prevOverflow;
      return;
    }

    // Two rows is the target, but the cap lands on a row boundary: a row
    // is either fully visible or fully scrolled out, never sliced through
    // the middle. Rows differ a lot in height — a server with three
    // sessions is several times a quiet one — so cutting at exactly the
    // height of the first two left the next row's name half-showing
    // along the bottom edge.
    var target = heights[0] + heights[1];
    var total = 0, shown = 0;
    for (var i = 0; i < heights.length; i++) {
      if (shown && total + heights[i] > target) break;
      total += heights[i];
      shown++;
    }
    if (shown >= rows.length) {
      // Everything fits inside two rows' worth: nothing to cap.
      list.style.maxHeight = "";
      list.style.overflowY = "";
      list.dataset.capFor = signature;
      return;
    }

    list.style.maxHeight = Math.ceil(total) + "px";
    list.style.overflowY = "auto";
    list.dataset.capFor = signature;
  }

  var observer = window.ResizeObserver ? new ResizeObserver(function (entries) {
    entries.forEach(function (entry) { capList(entry.target); });
  }) : null;

  // Always measured against the live document, never against a swap's
  // target. The Dashboard's polled fragment swaps with outerHTML, so by
  // the time htmx:afterSwap fires the target is the *old* element,
  // already detached — every offsetHeight in it reads 0, capList bails
  // on its !h guard, and the new list is left uncapped. That is why the
  // card sat correct for one render and then expanded on the first poll.
  //
  // Disconnecting first also stops the observer accumulating references
  // to the detached rows of every poll that has ever run.
  function bindCaps() {
    if (observer) observer.disconnect();
    document.querySelectorAll("[data-cap-rows]").forEach(function (list) {
      capList(list);
      if (!observer) return;
      observer.observe(list);
      // Each row too: a capped list stops changing size itself, so a
      // row growing taller (a session line wrapping, a progress bar
      // appearing) would otherwise never re-trigger the measurement.
      list.querySelectorAll(":scope > *").forEach(function (row) { observer.observe(row); });
    });
  }

  /* ── HTMX wiring ────────────────────────────────────────────────── */
  document.addEventListener("DOMContentLoaded", function () { bindCaps(); });

  /* ── Keeping the scroll containers alive ───────────────────────────
     The Dashboard's live fragment is replaced wholesale every few
     seconds, which recreates the list cards' scroll containers along
     with it. Two things go wrong when that happens: a list you had
     scrolled snaps back to the top on the next refresh, and a browser
     that draws overlay scrollbars flashes one every time a scrollable
     element is newly created.

     So the rows move into the element that is already there, and that
     element is put back where the new one would have gone. Nothing the
     user is interacting with is recreated — only the contents change,
     which is all that actually differs. It also means the measured cap
     survives, so capList finds its signature unchanged and does no work. */
  var liveScrollers = [];

  function stashScrollers() {
    // The offset has to be read now, while these are still in the
    // document. Once the swap detaches them they all report zero.
    liveScrollers = [].slice.call(document.querySelectorAll("[data-cap-rows]"))
      .map(function (el) { return { el: el, offset: el.scrollTop }; });
  }

  function restoreScrollers() {
    var incoming = [].slice.call(document.querySelectorAll("[data-cap-rows]"));
    // Positional matching, so only attempt it when the shape is identical.
    if (!liveScrollers.length || incoming.length !== liveScrollers.length) return;
    incoming.forEach(function (fresh, i) {
      var entry = liveScrollers[i];
      var kept = entry && entry.el;
      if (!kept || kept === fresh || !fresh.parentNode) return;
      kept.replaceChildren.apply(kept, [].slice.call(fresh.childNodes));
      fresh.replaceWith(kept);
      // Replacing the contents drops the scroll offset even though the
      // element itself is the same one, so put it back.
      if (entry.offset) kept.scrollTop = entry.offset;
    });
    liveScrollers = [];
  }

  document.body.addEventListener("htmx:beforeSwap", stashScrollers);

  document.body.addEventListener("htmx:afterSwap", function (e) {
    restoreScrollers();
    // A boosted navigation swaps in a fresh scrim, which arrives with its
    // `hidden` attribute back while <body> still says the drawer is open.
    // Re-apply the state so the scrim cannot go deaf.
    setDrawer(document.body.getAttribute("data-drawer") === "1");
    // Caps have to be re-measured: the swapped rows are new elements the
    // old observer is no longer watching.
    bindCaps();

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

  // Playback counts forward in the page, the way a player does. The
  // server says where a session had reached and whether it is running;
  // between updates the page advances it itself, and every server
  // update re-syncs it. Without this the timecode moves in whatever
  // steps the upstream socket happens to push, which reads as a stutter
  // rather than as playback.
  function startPlaybackTick() {
    if (window.__atPlayTick) clearInterval(window.__atPlayTick);
    var prior = window.__atPlayAt || {};
    window.__atPlayAt = {};
    var pad = function (n) { return String(n).padStart(2, "0"); };
    var code = function (ms) {
      var t = Math.max(0, Math.floor(ms / 1000));
      var h = Math.floor(t / 3600), m = Math.floor((t % 3600) / 60), sec = t % 60;
      return h > 0 ? h + ":" + pad(m) + ":" + pad(sec) : m + ":" + pad(sec);
    };
    var started = Date.now();
    var rows = [].slice.call(document.querySelectorAll(".at-session-progress[data-position-ms]"))
      .map(function (el, i) {
        var server = Number(el.dataset.positionMs) || 0;
        var from = server;
        // Only take the server's figure when it disagrees by more than a
        // couple of seconds. Snapping to every small difference makes the
        // clock visibly step backwards, which reads as a bug rather than
        // as the correction it is.
        var shown = prior["s" + i];
        if (typeof shown === "number" && Math.abs(shown - server) < 2500) from = shown;
        return {
          key: "s" + i,
          el: el,
          from: from,
          total: Number(el.dataset.durationMs) || 0,
          paused: el.dataset.paused === "1",
          time: el.querySelector(".at-session-time"),
          fill: el.querySelector(".at-session-fill"),
        };
      })
      .filter(function (r) { return r.total > 0 && r.time && r.fill; });
    if (!rows.length) return;
    window.__atPlayTick = setInterval(function () {
      if (!rows[0].el.isConnected) { clearInterval(window.__atPlayTick); return; }
      rows.forEach(function (r) {
        var at = r.paused ? r.from : Math.min(r.total, r.from + (Date.now() - started));
        window.__atPlayAt[r.key] = at;
        r.time.textContent = code(at) + " / " + code(r.total);
        r.fill.style.width = Math.max(0, Math.min(100, Math.round((at / r.total) * 100))) + "%";
      });
    }, 1000);
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

  // Nothing can be submitted until the form can actually succeed. The
  // rules differ per form, so each declares its own required set rather
  // than this guessing from the markup.
  function gateSubmit(form) {
    var require = form.getAttribute("data-require");
    var submit = form.querySelector("[data-submit]");
    if (!require || !submit) return;
    var val = function (name) {
      var el = form.querySelector('[name="' + name + '"]');
      return el && !el.disabled && !el.closest("[hidden]") ? el.value.trim() : "";
    };
    var ok = true;
    require.split(",").forEach(function (need) {
      if (need === "credential") {
        // A stored credential counts, and so does a token Plex just
        // handed over — neither is typed into a visible field.
        ok = ok && (!!val("token") || !!val("api_token") || !!val("plex_token") ||
                    form.getAttribute("data-hascredential") === "1");
      } else {
        ok = ok && !!val(need);
      }
    });
    submit.disabled = !ok;
  }

  function gateAllSubmits() {
    document.querySelectorAll("form[data-require]").forEach(gateSubmit);
  }

  document.addEventListener("input", function (e) {
    var form = e.target.closest("form[data-require]");
    if (form) gateSubmit(form);
  });
  document.addEventListener("change", function (e) {
    var form = e.target.closest("form[data-require]");
    if (form) gateSubmit(form);
  });

  // A failed test names the field that caused it, so the card can outline
  // it. The names differ per form, and a downloader's credential is two
  // controls rather than one.
  var testFieldSelectors = {
    name: '[name="name"]',
    url: '[name="url"]',
    credential: '[name="token"], [name="api_token"], [name="password"], [name="username"]',
  };

  // A failure can blame more than one field at once — an empty form
  // blames all of them — so the attribute holds a space-separated list.
  function testFieldSelector(box) {
    var parts = (box.dataset.field || "").split(/\s+/).filter(Boolean)
      .map(function (f) { return testFieldSelectors[f]; }).filter(Boolean);
    return parts.length ? parts.join(", ") : null;
  }

  function markTestField(box) {
    var form = box && box.closest("form");
    if (!form) return;
    form.querySelectorAll(".at-input--invalid").forEach(function (el) {
      el.classList.remove("at-input--invalid");
    });
    var sel = testFieldSelector(box);
    if (!sel) return;
    form.querySelectorAll(sel).forEach(function (el) {
      if (!el.closest("[hidden]")) el.classList.add("at-input--invalid");
    });
  }

  function syncTestFields() {
    document.querySelectorAll("form .at-testresult").forEach(markTestField);
  }

  // Editing the field the failure blamed retires the result outright —
  // it is describing a state that no longer exists.
  document.addEventListener("input", function (e) {
    var form = e.target.closest("form");
    if (!form) return;
    var box = form.querySelector(".at-testresult[data-field]");
    if (!box) return;
    var sel = testFieldSelector(box);
    if (sel && e.target.matches(sel)) {
      form.querySelectorAll(".at-input--invalid").forEach(function (el) {
        el.classList.remove("at-input--invalid");
      });
      box.remove();
    }
  });

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
    if (plexWatch) return;
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
    // No "noopener" here, deliberately: the token is honoured whatever
    // its value, and it makes window.open return null — which loses the
    // handle this flow needs to point the window at Plex and to notice
    // it being closed.
    plexWindow = window.open("", "at-plex-signin", "width=620,height=760,menubar=no,toolbar=no,location=no");
  });

  var plexSentTo = "";

  function syncPlexPanel() {
    var panel = document.querySelector(".at-plexsignin");
    if (!panel) return;
    var url = panel.dataset.authurl;
    if (url && plexWindow && !plexWindow.closed) {
      // Only on a URL we have not sent yet. The panel re-renders on
      // every poll, and re-navigating each time would throw away
      // whatever the user had already done in that window.
      if (url !== plexSentTo) {
        plexWindow.location = url;
        plexSentTo = url;
      }
      watchPlexWindow();
    }
    if (panel.dataset.state !== "waiting") {
      stopPlexWatch();
      plexSentTo = "";
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

  // A test that never answers must not leave the spinner turning. htmx
  // reports a transport failure and a non-2xx separately, and neither
  // swaps anything in — so without this the box says "Testing…" for as
  // long as the page stays open. Restarting the server mid-test is the
  // easy way to see it.
  function testRequestFailed(e, why) {
    var elt = e.detail && e.detail.elt;
    if (!elt || !elt.matches("[data-testbtn]")) return;
    var sel = elt.getAttribute("hx-target");
    var box = sel && document.querySelector(sel);
    if (!box) return;
    box.innerHTML =
      '<div class="at-testresult" data-state="failed" role="status">' +
      '<div class="at-testresult-head">' +
      '<span class="at-flag at-flag--sm" data-tone="critical">Failed</span>' +
      '<button type="button" class="at-testresult-close" data-testdismiss ' +
      'aria-label="Dismiss test result" title="Dismiss">\u2715</button></div>' +
      '<span class="at-testresult-hint">' + why + "</span></div>";
  }
  document.body.addEventListener("htmx:sendError", function (e) {
    testRequestFailed(e, "Could not reach AutoThrottle to run the test. It may be restarting — reload the page and try again.");
  });
  document.body.addEventListener("htmx:responseError", function (e) {
    var s = (e.detail && e.detail.xhr && e.detail.xhr.status) || 0;
    testRequestFailed(e, "AutoThrottle answered " + s + " when asked to run the test. Reload the page and try again.");
  });
  document.body.addEventListener("htmx:timeout", function (e) {
    testRequestFailed(e, "The test did not finish in time. Reload the page and try again.");
  });

  document.body.addEventListener("htmx:beforeRequest", function (e) {
    var elt = e.detail.elt;
    if (!elt) return;
    var path = (e.detail.requestConfig && e.detail.requestConfig.path) || "";
    if (elt.closest("[data-testtarget], form") && /\/test$/.test(path)) {
      showTesting(elt);
      return;
    }
    // The update check is the same shape of problem: the answer only
    // comes back when GitHub has replied, so the checking state has to
    // be put on screen before the request leaves.
    if (elt.matches("[data-updatebtn]")) {
      var box = document.querySelector("#update-check");
      var tpl = document.getElementById("at-tpl-checking");
      if (box && tpl) box.replaceChildren(tpl.content.firstElementChild.cloneNode(true));
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

  // Everything that has to be re-applied after the server replaces part
  // of the page: the uptime counter, the sign-in panel's popup wiring,
  // and which credential inputs are live.
  function syncAfterRender() {
    startUptime();
    startPlaybackTick();
    syncTestFields();
    gateAllSubmits();
    syncPlexPanel();
    document.querySelectorAll("form").forEach(syncCredentialUI);
  }

  document.body.addEventListener("htmx:afterSwap", syncAfterRender);
  document.addEventListener("DOMContentLoaded", syncAfterRender);
})();
