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
     many theme libraries do, would make System impossible to express. */
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
    // Per tab, not per browser: a freshly opened app starts expanded.
    try { sessionStorage.setItem("at-rail", collapsed ? "0" : "1"); } catch (e2) {}
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
    if (e.key !== "Escape") return;
    setDrawer(false);
    // A delete confirmation over the drawer takes the first Esc itself.
    if (!document.querySelector("dialog[open]")) setSheet(false);
  });

  /* ── Connections drawer ──────────────────────────────────────────
     The add and edit forms load into it; it opens once one has landed
     and keeps it while sliding shut, so nothing empties mid-slide. */
  var sheetReturn = null;
  function setSheet(open) {
    var sheet = document.getElementById("sheet");
    if (!sheet) return;
    sheet.classList.toggle("open", open);
    sheet.inert = !open;
    var scrim = document.querySelector(".at-sheet-scrim");
    if (scrim) scrim.classList.toggle("open", open);
    if (open) {
      sheet.scrollTop = 0;
      var first = sheet.querySelector("input:not([type=hidden]), select");
      if (first) first.focus({ preventScroll: true });
    } else if (sheetReturn && document.contains(sheetReturn)) {
      sheetReturn.focus();
    }
  }
  document.addEventListener("click", function (e) {
    var opener = e.target.closest("[data-sheet-open]");
    if (opener) sheetReturn = opener;
    if (e.target.closest("[data-sheet-close]")) setSheet(false);
  });
  document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e.detail.target && e.detail.target.id === "sheet-body") setSheet(true);
  });
  document.body.addEventListener("at-sheet-close", function () { setSheet(false); });

  /* ── Status dots ────────────────────────────────────────────────
     One fixed hover card for every dot, so a table's horizontal scroll
     can't clip it. */
  function showTip(dot) {
    var tip = document.querySelector(".at-st-tip");
    if (!tip) return;
    var r = dot.getBoundingClientRect();
    tip.dataset.tone = dot.dataset.tone;
    tip.firstElementChild.textContent = dot.dataset.label;
    tip.lastElementChild.textContent = dot.dataset.detail;
    tip.style.left = (r.left - 2) + "px";
    tip.style.top = (r.top - 6) + "px";
    tip.classList.add("on");
  }
  function hideTip() {
    var tip = document.querySelector(".at-st-tip");
    if (tip) tip.classList.remove("on");
  }
  ["mouseover", "focusin"].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var dot = e.target.closest && e.target.closest(".at-st");
      if (dot) showTip(dot);
    });
  });
  ["mouseout", "focusout"].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var dot = e.target.closest && e.target.closest(".at-st");
      if (dot && !dot.contains(e.relatedTarget)) hideTip();
    });
  });
  window.addEventListener("scroll", hideTip, true);

  // The live refresh of the Connections tables: each dot and session
  // count is updated in place, never replaced, so one under the pointer
  // or holding keyboard focus keeps both, and its open card follows.
  document.body.addEventListener("htmx:afterRequest", function (e) {
    if (!e.detail.elt || e.detail.elt.id !== "conn-live" || !e.detail.successful) return;
    var parsed = document.createElement("div");
    parsed.innerHTML = e.detail.xhr.responseText;
    [].forEach.call(parsed.children, function (fresh) {
      var el = fresh.id && document.getElementById(fresh.id);
      if (!el) return;
      if (fresh.classList.contains("at-st")) {
        ["data-tone", "data-label", "data-detail", "aria-label"].forEach(function (a) {
          if (el.getAttribute(a) !== fresh.getAttribute(a)) el.setAttribute(a, fresh.getAttribute(a));
        });
        var tip = document.querySelector(".at-st-tip.on");
        if (tip && (el.matches(":hover") || el === document.activeElement)) showTip(el);
      } else if (el.textContent !== fresh.textContent) {
        el.textContent = fresh.textContent;
      }
    });
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

    // How much room the card is actually giving this list. The two cards
    // share a row height, so a list can be handed far more space than
    // two of its rows need — which is the common case when the other
    // card's rows wrap onto more lines.
    var available = list.parentElement ? list.parentElement.clientHeight : 0;
    var natural = heights.reduce(function (sum, h) { return sum + h; }, 0);

    // Everything fits: no cap at all. The grid then stretches the rows to
    // fill the card, so there is no scrollbar and — the point — no band
    // of dead space under the last row, which is what capping at two
    // rows regardless of available height produced.
    if (available > 0 && natural <= available + 1) {
      list.style.maxHeight = "";
      list.style.overflowY = "";
      list.dataset.capFor = signature;
      return;
    }

    // Otherwise cap, on a row boundary: a row is either fully visible or
    // fully scrolled out, never sliced through the middle. Rows differ a
    // lot in height — a server with three sessions is several times a
    // quiet one — so cutting at exactly the height of the first two left
    // the next row's name half-showing along the bottom edge. Two rows is
    // the floor; past that fill whatever the card gives.
    var target = Math.max(heights[0] + heights[1], available);
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

     So the element that is already there stays, and the incoming rows
     are merged into it node by node rather than swapped in wholesale.
     That distinction matters: replacing the children empties the
     element and refills it, which drops scrollHeight to zero and back
     on every refresh. Firefox reads that size change as a reason to
     animate its overlay scrollbar in again, so the bar flashed every
     five seconds. Chrome, and a headless Firefox drawing classic
     always-visible scrollbars, show nothing — which is why this took a
     real browser to see.

     Merging in place means scrollHeight only changes when the rows
     genuinely change, the scroll offset is never lost, and the measured
     cap survives so capList finds its signature unchanged. */
  var liveScrollers = [];

  /* Makes target's children match source's, reusing every node it can.
     Only what actually differs is touched, so a row whose timecode moved
     keeps its element and changes one string. Attributes on target
     itself are deliberately not synced — that is where the measured cap
     lives, and the incoming copy has none. */
  function mergeChildren(target, source) {
    var t = target.firstChild, s = source.firstChild;
    while (t && s) {
      var nextT = t.nextSibling, nextS = s.nextSibling;
      if (t.nodeType !== s.nodeType || t.nodeName !== s.nodeName) {
        target.replaceChild(s, t);
      } else if (t.nodeType === 3 || t.nodeType === 8) {
        if (t.nodeValue !== s.nodeValue) t.nodeValue = s.nodeValue;
      } else if (t.nodeType === 1) {
        mergeAttributes(t, s);
        mergeChildren(t, s);
      }
      t = nextT;
      s = nextS;
    }
    while (s) {                       // rows the refresh added
      var addS = s.nextSibling;
      target.appendChild(s);
      s = addS;
    }
    while (t) {                       // rows the refresh dropped
      var dropT = t.nextSibling;
      target.removeChild(t);
      t = dropT;
    }
  }

  function mergeAttributes(target, source) {
    for (var i = source.attributes.length - 1; i >= 0; i--) {
      var attr = source.attributes[i];
      if (target.getAttribute(attr.name) !== attr.value) {
        target.setAttribute(attr.name, attr.value);
      }
    }
    // A cap list carries state the server knows nothing about: the
    // measured max-height and the signature it was measured for. The
    // incoming copy has neither, so removing what the source lacks
    // would strip the cap on every refresh and force a remeasure five
    // times a minute — which is visible as the scrollbar coming and
    // going.
    var keepsOwnState = target.hasAttribute("data-cap-rows");
    for (var j = target.attributes.length - 1; j >= 0; j--) {
      var own = target.attributes[j];
      if (source.hasAttribute(own.name)) continue;
      if (keepsOwnState && (own.name === "style" || own.name === "data-cap-for")) continue;
      target.removeAttribute(own.name);
    }
  }

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
      mergeChildren(kept, fresh);
      fresh.replaceWith(kept);
      if (entry.offset) kept.scrollTop = entry.offset;
    });
    liveScrollers = [];
  }

  /* The Dashboard's live block is merged rather than swapped.

     Keeping the scroll container alive was not enough on its own: the
     card around it is rebuilt by every swap, and moving the surviving
     list into that new card re-parents it. Firefox rebuilds the layout
     frame of anything re-parented, and a scrollable element with a new
     frame gets a new scrollbar — which animates in. That is a flash
     every five seconds that no amount of preserving the list itself
     could stop.

     Cancelling the swap and merging in place means nothing on the
     Dashboard is ever detached, re-parented or recreated. Only the text
     and attributes that actually differ change. */
  function mergeLiveBlock(e) {
    var target = e.detail.target;
    if (!target || target.id !== "dash-live") return false;
    var body = e.detail.serverResponse;
    if (typeof body !== "string" || !body) return false;

    var parsed = document.createElement("div");
    parsed.innerHTML = body;
    var incoming = parsed.firstElementChild;
    if (!incoming || incoming.id !== "dash-live") return false;

    mergeAttributes(target, incoming);
    mergeChildren(target, incoming);
    return true;
  }

  document.body.addEventListener("htmx:beforeSwap", function (e) {
    // A rejected form comes back as 422 with the page re-rendered and the
    // reason at the top. htmx drops non-2xx responses by default, so
    // without this the button does nothing at all and the reason is
    // never seen — which is worse than the mistake it is reporting.
    if (e.detail.xhr && e.detail.xhr.status === 422) {
      e.detail.shouldSwap = true;
      e.detail.isError = false;
      return;
    }
    if (mergeLiveBlock(e)) {
      // Handled here; htmx must not then replace the element we just
      // updated, or every gain above is undone.
      e.detail.shouldSwap = false;
      // Cancelling the swap also cancels htmx:afterSwap, so the work
      // that follows new content is announced here instead.
      bindCaps();
      document.body.dispatchEvent(new CustomEvent("at:merged"));
      return;
    }
    stashScrollers();
  });

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

  /* Playback clock: counts forward between server updates, the way a
     player does, and re-syncs on each one. Every tick reads its state
     from the element itself — the refresh merges values in place, and
     rows reorder — so nothing holds a copy that can go stale. */
  function pad(n) { return String(n).padStart(2, "0"); }

  function timecode(ms) {
    var t = Math.max(0, Math.floor(ms / 1000));
    var h = Math.floor(t / 3600), m = Math.floor((t % 3600) / 60), sec = t % 60;
    return h > 0 ? h + ":" + pad(m) + ":" + pad(sec) : m + ":" + pad(sec);
  }

  function paintPlayback() {
    document.querySelectorAll(".at-session-progress[data-position-ms]").forEach(function (el) {
      var total = Number(el.dataset.durationMs) || 0;
      if (!total) return;
      var server = Number(el.dataset.positionMs) || 0;
      var paused = el.dataset.paused === "1";

      // Re-baseline whenever the server reports something new, or the
      // stream pauses or resumes. Between reports the clock runs on from
      // the last figure, which is what makes a 5s refresh read as a
      // per-second clock.
      var state = el.__atPlay;
      if (!state || state.server !== server || state.paused !== paused) {
        state = el.__atPlay = { server: server, paused: paused, since: Date.now() };
      }

      var at = paused ? server : Math.min(total, server + (Date.now() - state.since));
      var time = el.querySelector(".at-session-time");
      var fill = el.querySelector(".at-session-fill");
      if (time) time.textContent = timecode(at) + " / " + timecode(total);
      if (fill) fill.style.width = Math.max(0, Math.min(100, Math.round((at / total) * 100))) + "%";
    });
  }

  function startPlaybackTick() {
    // One interval for the life of the page. It walks whatever is on
    // screen at the time, so it needs no restarting when content changes.
    if (!window.__atPlayTick) {
      window.__atPlayTick = setInterval(paintPlayback, 1000);
    }
    paintPlayback();
  }

  // Uptime counts forward in the page. The server sends the boot
  // timestamp and the first frame; recomputing from the timestamp keeps
  // it right across a tab that was asleep, which incrementing would not.
  var uptimeTimer = null;
  function startUptime() {
    clearInterval(uptimeTimer);
    var el = document.querySelector("[data-uptime-since]");
    if (!el) return;
    var boot = Number(el.dataset.uptimeSince);
    if (!boot) return;
    var tick = function () {
      if (!el.isConnected) { clearInterval(uptimeTimer); return; }
      var secs = Math.max(0, Math.floor((Date.now() - boot) / 1000));
      el.textContent =
        Math.floor(secs / 86400) + "d : " + pad(Math.floor((secs % 86400) / 3600)) +
        "h : " + pad(Math.floor((secs % 3600) / 60)) + "m : " + pad(secs % 60) + "s";
    };
    uptimeTimer = setInterval(tick, 1000);
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
    // A passing connection test is required before a new connection can
    // be saved: filled-in fields say nothing about whether the address
    // answers or the credential works. When editing, the saved
    // connection needs no new test until one of its own fields changes,
    // and changing it back counts as unchanged.
    if (form.hasAttribute("data-editing") && form.__savedValues === undefined) {
      form.__savedValues = formValues(form);
      form.__savedUrl = val("url");
      form.__savedUser = val("username");
    }
    // A saved credential is only ever sent to the address, and with the
    // username, it was saved for; change either and it must be typed
    // again. The server holds to the same rule.
    var stored = form.getAttribute("data-hascredential") === "1";
    var moved = stored && form.hasAttribute("data-editing") &&
      (!sameOrigin(form.__savedUrl, val("url")) || (form.__savedUser !== undefined && form.__savedUser !== val("username")));
    form.querySelectorAll("[data-keep-hint]").forEach(function (hint) {
      hint.textContent = moved ? "Enter it again for the new address or username." : "Leave blank to keep the existing value.";
    });
    var ok = !!form.querySelector('.at-testresult[data-state="connected"]') ||
      (form.hasAttribute("data-editing") && formValues(form) === form.__savedValues);
    require.split(",").forEach(function (need) {
      if (need === "credential") {
        // A stored credential counts, and so does a token Plex just
        // handed over — neither is typed into a visible field.
        ok = ok && (!!val("token") || !!val("api_token") || !!val("plex_token") || (stored && !moved));
      } else {
        ok = ok && !!val(need);
      }
    });
    // A downloader's credentials are optional, but saved ones cannot
    // follow it to a new address unannounced.
    if (moved && form.querySelector('[name="password"]') && !val("password")) ok = false;
    submit.disabled = !ok;
  }

  // Same scheme, host and port: what decides whether a saved credential
  // may be used with an edited address.
  function sameOrigin(a, b) {
    try {
      var x = new URL(a), y = new URL(b);
      return x.protocol === y.protocol && x.host.toLowerCase() === y.host.toLowerCase();
    } catch (e) {
      return a === b;
    }
  }

  function gateAllSubmits() {
    document.querySelectorAll("form[data-require]").forEach(gateSubmit);
  }

  /* A pass covers the values that were tested, so changing any of them
     retires it (a failure stays, to explain the outlined field).
     "Changed" means the values differ, not that an event fired: leaving
     a field fires change with nothing new typed. */
  // Only what the test actually connects with. Renaming the entry, its
  // rates or its checkboxes leaves a passing test standing.
  var TESTED_FIELDS = ["type", "url", "token", "api_token", "plex_token", "username", "password"];
  function formValues(form) {
    var pairs = [];
    new FormData(form).forEach(function (value, name) {
      if (TESTED_FIELDS.indexOf(name) !== -1) pairs.push(name + "=" + value);
    });
    return pairs.join("&");
  }

  // A new entry is named after its type until someone types a name.
  document.addEventListener("change", function (e) {
    var select = e.target;
    if (!select.matches || !select.matches('select[name="type"]')) return;
    var name = select.form && select.form.querySelector("[data-autoname]");
    if (!name || (name.value.trim() !== name.dataset.autoname && name.value.trim() !== "")) return;
    name.value = name.dataset.autoname = select.options[select.selectedIndex].text;
  });

  function retireTestResult(form) {
    var passed = form.querySelector('.at-testresult[data-state="connected"]');
    if (passed && form.__testedValues !== formValues(form)) passed.remove();
  }

  function fieldEdited(e) {
    var form = e.target.closest("form[data-require]");
    if (!form) return;
    retireTestResult(form);
    gateSubmit(form);
  }

  document.addEventListener("input", fieldEdited);
  document.addEventListener("change", fieldEdited);

  // Remember exactly what passed, at the moment it passes.
  document.body.addEventListener("htmx:afterSwap", function (e) {
    var form = e.detail.target && e.detail.target.closest && e.detail.target.closest("form[data-require]");
    if (form && form.querySelector('.at-testresult[data-state="connected"]')) {
      form.__testedValues = formValues(form);
    }
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
    testRequestFailed(e, "Could not reach AutoThrottle to run the test. It may be restarting, so reload the page and try again.");
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
  // The Dashboard's refresh merges in place and cancels the swap, so it
  // announces itself this way instead.
  document.body.addEventListener("at:merged", syncAfterRender);
  document.addEventListener("DOMContentLoaded", syncAfterRender);
})();

/* ── Create account ─────────────────────────────────────────────
   Show/Hide lets the owner check what they typed, since there is no
   confirm field; Create stays off until both fields have something. */
(function () {
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-reveal]");
    if (!btn) return;
    var input = btn.parentNode.querySelector("input");
    var show = input.type === "password";
    input.type = show ? "text" : "password";
    btn.textContent = show ? "Hide" : "Show";
    btn.setAttribute("aria-pressed", show ? "true" : "false");
    btn.setAttribute("aria-label", show ? "Hide password" : "Show password");
  });
  function gate(form) {
    var submit = form.querySelector("[data-submit]");
    if (!submit) return;
    submit.disabled = form.getAttribute("data-fill-required").split(",").some(function (name) {
      var el = form.querySelector('[name="' + name + '"]');
      return !el || el.value === "";
    });
  }
  document.addEventListener("input", function (e) {
    var form = e.target.closest && e.target.closest("form[data-fill-required]");
    if (form) gate(form);
  });
  document.addEventListener("DOMContentLoaded", function () {
    document.querySelectorAll("form[data-fill-required]").forEach(gate);
  });
})();

/* ── Live updates ───────────────────────────────────────────────
   One event stream per tab names what changed — "dashboard", "logs",
   "history" — and the parts of the page showing it fetch their own
   fragment, the same request their timed refresh makes. The stream is
   open only while the tab is visible and the page has something live
   on it; without it, the old five-second poll takes over, so a proxy
   that will not pass a stream costs a few seconds and nothing else. */
(function () {
  var source = null;
  var up = false;
  var everOpened = false;
  var retry = 0;
  var retryTimer = null;
  // When each live block last refreshed, by id. Blocks that swap
  // wholesale come back as new elements, so it cannot live on them.
  var refreshedAt = {};
  // Even with the stream up, refresh now and then in case a signal was
  // lost, and whenever the stream is down, as often as before.
  var SAFETY = 30000;

  function blocks() { return document.querySelectorAll("[data-live]"); }
  // Whether the stream is up, on the root element where anyone checking
  // why a page is or is not updating can see it.
  function mark(state) { document.documentElement.setAttribute("data-live-stream", state); }
  function visible() { return document.visibilityState === "visible"; }

  // The filter on each live block's own poll.
  window.atLivePoll = function (el) {
    if (!visible()) return false;
    if (!up) return true;
    return Date.now() - (refreshedAt[el.id] || 0) >= SAFETY;
  };

  function refresh(topics) {
    blocks().forEach(function (el) {
      var mine = el.getAttribute("data-live").split(" ");
      if (!topics || mine.some(function (t) { return topics[t]; })) htmx.trigger(el, "live");
    });
    if ((!topics || topics.history) && window.atRefreshHistory) window.atRefreshHistory();
  }

  // A burst of changes (a session starting, then each downloader
  // throttling) becomes one refresh.
  var pending = {};
  var flushTimer = null;
  function queue(topic) {
    pending[topic] = true;
    if (!flushTimer) flushTimer = setTimeout(function () {
      flushTimer = null;
      var topics = pending;
      pending = {};
      refresh(topics);
    }, 150);
  }

  function open() {
    if (source || retryTimer || !visible() || !blocks().length || !window.EventSource) return;
    source = new EventSource("/events");
    source.onopen = function () {
      up = true;
      mark("open");
      retry = 0;
      // Anything that changed while the stream was down was missed.
      if (everOpened) refresh(null);
      everOpened = true;
    };
    source.onmessage = function (e) { queue(e.data); };
    source.onerror = function () {
      up = false;
      mark("down");
      // The browser retries a dropped stream by itself. One it has
      // given up on (a sign-in redirect, a proxy refusing the type) is
      // retried here, backing off, while the poll covers the gap.
      if (source && source.readyState === EventSource.CLOSED) {
        source = null;
        retry = Math.min(retry + 1, 6);
        retryTimer = setTimeout(function () { retryTimer = null; open(); }, 2500 * Math.pow(2, retry - 1));
      }
    };
  }

  function close() {
    if (source) source.close();
    source = null;
    up = false;
    everOpened = false;
    mark("closed");
  }

  function sync() {
    if (visible() && blocks().length) open();
    else close();
  }

  document.addEventListener("visibilitychange", function () {
    var wasOpen = !!source;
    sync();
    // Coming back to the tab: catch up straight away.
    if (visible() && !wasOpen && blocks().length) refresh(null);
  });
  document.body.addEventListener("htmx:afterRequest", function (e) {
    var el = e.detail.elt;
    if (el && el.id && el.hasAttribute && el.hasAttribute("data-live")) refreshedAt[el.id] = Date.now();
  });
  // Boosted navigation swaps the page without reloading the script.
  document.body.addEventListener("htmx:afterSettle", sync);
  document.addEventListener("DOMContentLoaded", function () {
    blocks().forEach(function (el) { refreshedAt[el.id] = Date.now(); });
    sync();
  });
  window.addEventListener("pagehide", close);
})();

/* ── Session timer and Throttle History ─────────────────────────────
   The timer counts up from data-throttled-since, reading it on every
   tick so the Dashboard's refresh can move it. The history card holds
   all 30 days; switching range counts the total from the old figure to
   the new one and animates the grid's height, both over 700ms. */
(function () {
  "use strict";

  var reduceMotion = function () {
    return window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  };
  function pad(n) { return String(n).padStart(2, "0"); }

  setInterval(function () {
    document.querySelectorAll("[data-throttled-since]").forEach(function (el) {
      var since = Date.parse(el.dataset.throttledSince);
      if (isNaN(since)) return;
      var t = Math.max(0, Math.floor((Date.now() - since) / 1000));
      var n = el.querySelectorAll(".at-timer-n");
      if (n.length !== 3) return;
      n[0].textContent = pad(Math.floor(t / 3600));
      n[1].textContent = pad(Math.floor((t % 3600) / 60));
      n[2].textContent = pad(t % 60);
    });
  }, 1000);

  var DURATION = 700;
  var EASE = "cubic-bezier(0.33, 1, 0.68, 1)";
  var animating = false;

  function label(min) { return Math.floor(min / 60) + "hr " + (min % 60) + "m"; }

  // Hours and minutes sit in right-aligned slots sized for the widest
  // value the count passes through, so nothing after them moves; once
  // it settles they ease to the final value's natural width.
  function countTotal(el, from, to) {
    if (from === to) return;
    if (reduceMotion()) { el.innerHTML = ""; settle(build(el), to); return; }
    var parts = build(el);
    var widest = String(Math.floor(Math.max(from, to) / 60)).length;
    parts.h.style.minWidth = widest + "ch";
    parts.m.style.minWidth = "2ch";
    parts.h.style.transition = parts.m.style.transition = "none";
    var t0 = performance.now();
    (function frame(now) {
      var p = Math.min(1, (now - t0) / DURATION);
      var v = Math.round(from + (to - from) * (1 - Math.pow(1 - p, 3)));
      parts.h.textContent = Math.floor(v / 60);
      parts.m.textContent = v % 60;
      if (p < 1) { requestAnimationFrame(frame); return; }
      parts.h.style.transition = parts.m.style.transition = "";
      settle(parts, to);
    })(t0);
    el.setAttribute("aria-label", label(to));
  }

  function build(el) {
    el.innerHTML = '<span class="at-hm-slot"></span>hr <span class="at-hm-slot"></span>m';
    return { el: el, h: el.children[0], m: el.children[1] };
  }

  function settle(parts, total) {
    var h = Math.floor(total / 60), m = total % 60;
    parts.h.textContent = h;
    parts.m.textContent = m;
    parts.h.style.minWidth = String(h).length + "ch";
    parts.m.style.minWidth = String(m).length + "ch";
    parts.el.setAttribute("aria-label", label(total));
  }

  function setRange(card, range) {
    var from = card.dataset.range;
    if (from === range || animating) return;
    var days = range === "30d" ? 30 : 7;
    card.querySelectorAll("[data-history-range]").forEach(function (tab) {
      tab.setAttribute("aria-selected", tab.dataset.historyRange === range ? "true" : "false");
    });
    var empty = card.getAttribute("data-empty-" + days) === "true";
    card.querySelector(".at-hm-caption-text").textContent = empty ? "No throttled history" : "throttled in the last " + days + " days";
    card.querySelector(".at-hm").setAttribute("aria-label", "Minutes throttled per hour, last " + days + " days");
    try { history.replaceState(history.state, "", "?range=" + range); } catch (e) {}

    countTotal(card.querySelector(".at-hm-total"),
      Number(card.getAttribute("data-total-" + (from === "30d" ? 30 : 7))),
      Number(card.getAttribute("data-total-" + days)));

    var wrap = card.querySelector(".at-hm-wrap");
    var before = wrap.offsetHeight;
    if (reduceMotion() || !wrap.animate) { card.dataset.range = range; return; }
    // Clipped only while it moves, so tooltips on the top rows are not.
    animating = true;
    wrap.style.overflow = "hidden";
    if (range === "30d") {
      // Expanding: draw the new rows, then reveal them.
      card.dataset.range = range;
      var after = wrap.offsetHeight;
      wrap.animate([{ height: before + "px" }, { height: after + "px" }], { duration: DURATION, easing: EASE })
        .onfinish = function () { wrap.style.overflow = ""; animating = false; };
      return;
    }
    // Collapsing: animate down to the top of the 8th row while the old
    // rows are still drawn, then drop them and release the height in
    // the same frame, so nothing snaps.
    var grid = wrap.firstElementChild;
    var cut = grid.querySelector('[data-row="7"]');
    var gap = parseFloat(getComputedStyle(grid).rowGap) || 0;
    var target = cut ? cut.getBoundingClientRect().top - wrap.getBoundingClientRect().top - gap : before;
    wrap.style.height = target + "px";
    wrap.animate([{ height: before + "px" }, { height: target + "px" }], { duration: DURATION, easing: EASE })
      .onfinish = function () {
        card.dataset.range = range;
        wrap.style.height = "";
        wrap.style.overflow = "";
        animating = false;
      };
  }

  document.addEventListener("click", function (e) {
    var tab = e.target.closest && e.target.closest("[data-history-range]");
    if (!tab) return;
    var card = tab.closest("#throttle-history");
    if (!card) return;
    e.preventDefault();
    setRange(card, tab.dataset.historyRange);
  });

  // The hour in progress keeps filling, so the card refreshes itself —
  // but not while the pointer is reading a tooltip, or mid-animation.
  // A period starting or ending refreshes it at once, through the live
  // stream.
  function refreshHistory() {
    var card = document.getElementById("throttle-history");
    if (!card || animating || document.visibilityState !== "visible" || card.matches(":hover") || !window.htmx) return;
    htmx.ajax("GET", "/dashboard/history?range=" + card.dataset.range, { target: "#throttle-history", swap: "outerHTML" });
  }
  window.atRefreshHistory = refreshHistory;
  setInterval(refreshHistory, 60000);
})();
