/* The app's only front-end code: what server-rendered HTMX can't do. */
(function () {
  "use strict";

  var root = document.documentElement;

  /* ── Theme ──────────────────────────────────────────────────────
     "System" is the absence of data-theme, so prefers-color-scheme applies. */
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
     The head script applies the stored state before first paint; this
     only handles the toggle. */
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
     Always mounted, so it has something to transition from. */
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
    // Choosing a destination closes the menu.
    if (e.target.closest(".at-rail a")) setDrawer(false);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    setDrawer(false);
    // A delete confirmation over the drawer takes the first Esc itself.
    if (!document.querySelector("dialog[open]")) setSheet(false);
  });

  /* ── Connections drawer ──────────────────────────────────────────
     Opens once a form has loaded into it, and keeps the form while
     sliding shut. */
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

  // Connections' live refresh updates each dot and session count in
  // place, so a hovered or focused dot keeps its hover card.
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
     The Dashboard's list cards show two rows and scroll past that. Row
     height varies with wrapping, so the cap is measured, not CSS. */
  function capList(list) {
    var rows = list.querySelectorAll(":scope > *");
    if (rows.length < 3) {
      list.style.maxHeight = "";
      list.style.overflowY = "";
      delete list.dataset.capFor;
      return;
    }

    // Re-measured only when the shape changes. offsetWidth, not
    // clientWidth, which shrinks by the scrollbar this cap adds and would
    // flip the signature forever. Session lines count: playback grows a
    // row without adding one.
    var signature = rows.length + ":" + Math.round(list.offsetWidth) +
      ":" + list.querySelectorAll(".at-session").length;
    if (list.dataset.capFor === signature && list.style.maxHeight) return;

    // Measure with the cap lifted, or the compressed rows feed the old
    // answer back in. The signature check keeps this from looping with
    // the observer (a flickering scrollbar).
    var prevMax = list.style.maxHeight, prevOverflow = list.style.overflowY;
    var prevRows = list.style.gridAutoRows, prevAlign = list.style.alignContent;
    list.style.maxHeight = "";
    list.style.overflowY = "";
    // At natural heights: grid-auto-rows: 1fr makes every row report the
    // tallest, which lands the cap mid-row.
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

    // The two cards share a row height, so this list may be given more
    // room than two rows need.
    var available = list.parentElement ? list.parentElement.clientHeight : 0;
    var natural = heights.reduce(function (sum, h) { return sum + h; }, 0);

    // Everything fits: no cap, so the rows stretch to fill the card with
    // no dead band under the last one.
    if (available > 0 && natural <= available + 1) {
      list.style.maxHeight = "";
      list.style.overflowY = "";
      list.dataset.capFor = signature;
      return;
    }

    // Otherwise cap on a row boundary, never slicing a row: at least two
    // rows, and as many more as the card has room for.
    var target = Math.max(heights[0] + heights[1], available);
    var total = 0, shown = 0;
    for (var i = 0; i < heights.length; i++) {
      if (shown && total + heights[i] > target) break;
      total += heights[i];
      shown++;
    }
    if (shown >= rows.length) {
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

  // Measured against the live document, never a swap's target: after an
  // outerHTML swap the target is the detached old element, which
  // measures 0. Disconnecting first drops references to detached rows.
  function bindCaps() {
    if (observer) observer.disconnect();
    document.querySelectorAll("[data-cap-rows]").forEach(function (list) {
      capList(list);
      if (!observer) return;
      observer.observe(list);
      // Each row too: a capped list no longer resizes when a row grows.
      list.querySelectorAll(":scope > *").forEach(function (row) { observer.observe(row); });
    });
  }

  document.addEventListener("DOMContentLoaded", function () { bindCaps(); });

  /* ── Keeping the scroll containers alive ───────────────────────────
     Recreating a list's scroll container on refresh resets its scroll
     offset, and Firefox flashes its overlay scrollbar whenever
     scrollHeight changes — even when children are replaced in place. So
     incoming rows are merged node by node into the existing element.
     Headless browsers draw classic scrollbars and can't show the flash. */
  var liveScrollers = [];

  /* Makes target's children match source's, touching only what differs.
     target's own attributes are left alone: the measured cap lives there. */
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
    // A cap list's measured style and signature are client state the
    // server copy lacks; stripping them would remeasure on every refresh.
    var keepsOwnState = target.hasAttribute("data-cap-rows");
    for (var j = target.attributes.length - 1; j >= 0; j--) {
      var own = target.attributes[j];
      if (source.hasAttribute(own.name)) continue;
      if (keepsOwnState && (own.name === "style" || own.name === "data-cap-for")) continue;
      target.removeAttribute(own.name);
    }
  }

  function stashScrollers() {
    // Read now: detached by the swap, they report zero.
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

  /* The Dashboard's live block is merged, never swapped: re-parenting a
     kept list into a rebuilt card also makes Firefox redraw (and flash)
     its scrollbar. Nothing is detached, re-parented or recreated. */
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
    swapOutOfBand(parsed, incoming);
    return true;
  }

  /* htmx applies a response's hx-swap-oob elements only in a swap it makes
     itself, and the merge cancels that one: the header's status pill and
     actions would keep the state of the last full page load. Each replaces
     its namesake only when it changed, so the pill's animation doesn't
     restart on every refresh. */
  function swapOutOfBand(parsed, merged) {
    [].slice.call(parsed.children).forEach(function (el) {
      if (el === merged || !el.id || !el.hasAttribute("hx-swap-oob")) return;
      var current = document.getElementById(el.id);
      if (!current) return;
      el.removeAttribute("hx-swap-oob");
      if (current.outerHTML === el.outerHTML) return;
      current.replaceWith(el);
      htmx.process(el);
    });
  }

  document.body.addEventListener("htmx:beforeSwap", function (e) {
    // A whole page aimed anywhere but the body would nest inside the app:
    // a second app (an inherited hx-target does it), or the sign-in page
    // reached through a redirect. Load it instead.
    var body = e.detail.serverResponse;
    if (e.detail.target !== document.body && typeof body === "string" && /^\s*<!doctype html/i.test(body)) {
      e.detail.shouldSwap = false;
      console.warn("AutoThrottle: a full page was aimed at #" + (e.detail.target && e.detail.target.id) + "; loading it instead");
      window.location.assign(e.detail.xhr.responseURL || window.location.href);
      return;
    }
    // A rejected form comes back 422 with the reason; htmx drops non-2xx
    // by default, which would leave the button looking dead.
    if (e.detail.xhr && e.detail.xhr.status === 422) {
      e.detail.shouldSwap = true;
      e.detail.isError = false;
      return;
    }
    if (mergeLiveBlock(e)) {
      e.detail.shouldSwap = false;
      // No afterSwap follows a cancelled swap, so announce it instead.
      bindCaps();
      document.body.dispatchEvent(new CustomEvent("at:merged"));
      return;
    }
    stashScrollers();
  });

  document.body.addEventListener("htmx:afterSwap", function (e) {
    restoreScrollers();
    // A boosted navigation brings a fresh, hidden scrim; re-sync it.
    setDrawer(document.body.getAttribute("data-drawer") === "1");
    bindCaps();

    // A swap that replaced the focused element drops focus to <body>;
    // put it back on what was pressed.
    var trigger = e.detail.requestConfig && e.detail.requestConfig.elt;
    if (trigger && document.activeElement === document.body && document.contains(trigger)) {
      trigger.focus({ preventScroll: true });
    }
  });
})();

/* ── Credential method sub-tabs ───────────────────────────────────
   Plex takes a token or a sign-in; Emby and Jellyfin only a key. */
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
      // Sign-in is Plex-only, so fall back to the token pane.
      form.querySelectorAll("[data-credtab]").forEach(function (t) {
        t.setAttribute("aria-selected", String(t.dataset.credtab === "token"));
      });
      showPane(form, "token");
    }
    syncDisabled(form);
  }

  // Hidden inputs still submit, so whatever can't be seen is disabled.
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

  /* Playback clock: counts forward between server updates and re-syncs
     on each, reading its state from the element so nothing goes stale. */
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

      // Re-baseline on each new report, pause or resume.
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
    // One interval for the page's life; it walks whatever is on screen.
    if (!window.__atPlayTick) {
      window.__atPlayTick = setInterval(paintPlayback, 1000);
    }
    paintPlayback();
  }

  // Uptime is recomputed from the boot timestamp, so a tab that slept
  // stays right.
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

  // Nothing can be submitted until it can succeed; each form declares
  // its own requirements.
  function gateSubmit(form) {
    var require = form.getAttribute("data-require");
    var submit = form.querySelector("[data-submit]");
    if (!require || !submit) return;
    var val = function (name) {
      var el = form.querySelector('[name="' + name + '"]');
      return el && !el.disabled && !el.closest("[hidden]") ? el.value.trim() : "";
    };
    // A new connection needs a passing test. An edit needs one only once
    // a tested field changes; changing it back counts as unchanged.
    if (form.hasAttribute("data-editing") && form.__savedValues === undefined) {
      form.__savedValues = formValues(form);
      form.__savedUrl = val("url");
      form.__savedUser = val("username");
    }
    // A saved credential only goes to its saved address and username;
    // change either and it must be retyped (the server agrees).
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
        // A stored credential or a Plex sign-in token counts too.
        ok = ok && (!!val("token") || !!val("api_token") || !!val("plex_token") || (stored && !moved));
      } else {
        ok = ok && !!val(need);
      }
    });
    if (moved && form.querySelector('[name="password"]') && !val("password")) ok = false;
    submit.disabled = !ok;
  }

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

  /* A pass covers the values tested: changing one retires it (a failure
     stays, to explain the outlined field). "Changed" compares values, not
     events. Name, rates and checkboxes aren't tested, so don't count. */
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

  // A failed test names the field(s) to outline.
  var testFieldSelectors = {
    name: '[name="name"]',
    url: '[name="url"]',
    credential: '[name="token"], [name="api_token"], [name="password"], [name="username"]',
  };

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

  // Editing a blamed field retires the failure.
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

  // Browsers only allow the Plex popup inside the click, before the auth
  // URL is known: open it blank, then point it at Plex.
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
      // Grace for one poll: approving closes the window just before the
      // poll that collects the token.
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
    // No "noopener": it makes window.open return null, losing the handle
    // needed to navigate the window and notice it closing.
    plexWindow = window.open("", "at-plex-signin", "width=620,height=760,menubar=no,toolbar=no,location=no");
  });

  var plexSentTo = "";

  function syncPlexPanel() {
    var panel = document.querySelector(".at-plexsignin");
    if (!panel) return;
    var url = panel.dataset.authurl;
    if (url && plexWindow && !plexWindow.closed) {
      // Once per URL: the panel re-renders on every poll.
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

  // A test shows its pending state at once; the server only answers when
  // the probe finishes.
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

  // A test that never answers must not leave "Testing…" up: htmx swaps
  // nothing in on a transport failure or a non-2xx.
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
    // Likewise the update check, which waits on GitHub.
    if (elt.matches("[data-updatebtn]")) {
      var box = document.querySelector("#update-check");
      var tpl = document.getElementById("at-tpl-checking");
      if (box && tpl) box.replaceChildren(tpl.content.firstElementChild.cloneNode(true));
    }
  });

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

  // Re-applied whenever the server replaces part of the page.
  function syncAfterRender() {
    startUptime();
    startPlaybackTick();
    syncTestFields();
    gateAllSubmits();
    syncPlexPanel();
    document.querySelectorAll("form").forEach(syncCredentialUI);
  }

  document.body.addEventListener("htmx:afterSwap", syncAfterRender);
  document.body.addEventListener("at:merged", syncAfterRender);
  document.addEventListener("DOMContentLoaded", syncAfterRender);
})();

/* ── Create account ─────────────────────────────────────────────
   Show/Hide stands in for a confirm field. */
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
   One event stream per visible tab names what changed ("dashboard",
   "logs", "history"); each live block then fetches its own fragment.
   Without the stream, the five-second poll takes over. */
(function () {
  var source = null;
  var up = false;
  var everOpened = false;
  var retry = 0;
  var retryTimer = null;
  // Keyed by id: blocks that swap wholesale come back as new elements.
  var refreshedAt = {};
  // A safety refresh in case a signal was lost.
  var SAFETY = 30000;

  function blocks() { return document.querySelectorAll("[data-live]"); }
  // Shown on <html> for debugging (and the E2E).
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

  // A burst of changes becomes one refresh.
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
      // The browser retries a dropped stream itself; one it gave up on
      // (sign-in redirect, a proxy refusing it) is retried here, backing off.
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
   The timer re-reads data-throttled-since every tick, so a refresh can
   move it. The history card holds all 30 days; switching range animates
   the total and the grid's height. */
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

  // The current hour keeps filling, so refresh — but not under a hovering
  // pointer or mid-animation. The live stream refreshes it on changes.
  function refreshHistory() {
    var card = document.getElementById("throttle-history");
    if (!card || animating || document.visibilityState !== "visible" || card.matches(":hover") || !window.htmx) return;
    htmx.ajax("GET", "/dashboard/history?range=" + card.dataset.range, { target: "#throttle-history", swap: "outerHTML" });
  }
  window.atRefreshHistory = refreshHistory;
  setInterval(refreshHistory, 60000);

  // Hovering an hour highlights every hour of its stretch, so how long the
  // throttling ran shows in the grid rather than in the tooltip.
  var hovered = null;
  document.addEventListener("mouseover", function (e) {
    var cell = e.target.closest && e.target.closest("#throttle-history [data-stretch]");
    if (cell === hovered) return;
    document.querySelectorAll(".at-hm-linked").forEach(function (el) { el.classList.remove("at-hm-linked"); });
    hovered = cell;
    if (!cell) return;
    cell.getAttribute("data-stretch").split(" ").forEach(function (id) {
      cell.parentNode.querySelectorAll('[data-stretch~="' + id + '"]').forEach(function (el) {
        el.classList.add("at-hm-linked");
      });
    });
  });
})();
