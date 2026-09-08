// Docs search (canvas 19d) — prose and endpoints in one list, opened with "/"
// from any page, plus the theme toggle and the copy buttons.
//
// It is a static site, so this is a prebuilt index and about eighty lines of
// ranking rather than a search service. Honest about being that: title matches
// first, then path, then body, and nothing pretends to be relevance scoring.
// The whole client-side surface of this site is this file and one stylesheet —
// no bundler, no npm dependency (documentation-site.md §7).
(function () {
  "use strict";

  // ── theme ────────────────────────────────────────────────────────────────
  var root = document.documentElement;
  function currentTheme() {
    if (root.dataset.theme === "dark" || root.dataset.theme === "light") return root.dataset.theme;
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  var toggle = document.querySelector("[data-theme-toggle]");
  if (toggle) {
    toggle.addEventListener("click", function () {
      var next = currentTheme() === "dark" ? "light" : "dark";
      root.dataset.theme = next;
      try { localStorage.setItem("cp-docs-theme", next); } catch (e) { /* private mode */ }
    });
  }

  // ── copy ─────────────────────────────────────────────────────────────────
  document.addEventListener("click", function (ev) {
    var btn = ev.target.closest(".copy");
    if (!btn) return;
    var host = btn.closest("[data-copy]");
    if (!host || !navigator.clipboard) return;
    navigator.clipboard.writeText(host.getAttribute("data-copy")).then(function () {
      var was = btn.textContent;
      btn.textContent = "copied";
      setTimeout(function () { btn.textContent = was; }, 1400);
    }, function () { /* denied — the text is on screen either way */ });
  });

  // ── on this page ─────────────────────────────────────────────────────────
  // Marks the heading currently under the top bar. Cheap and honest: the last
  // heading whose top has passed the bar wins, which is what a reader means.
  var links = Array.prototype.slice.call(document.querySelectorAll(".onthispage a"));
  if (links.length) {
    var targets = links.map(function (a) {
      return { li: a.parentElement, el: document.getElementById(decodeURIComponent(a.hash.slice(1))) };
    }).filter(function (t) { return t.el; });
    var mark = function () {
      var active = null;
      for (var i = 0; i < targets.length; i++) {
        if (targets[i].el.getBoundingClientRect().top <= 80) active = targets[i];
      }
      targets.forEach(function (t) { t.li.classList.toggle("on", t === active); });
    };
    var ticking = false;
    window.addEventListener("scroll", function () {
      if (ticking) return;
      ticking = true;
      requestAnimationFrame(function () { mark(); ticking = false; });
    }, { passive: true });
    mark();
  }

  // ── search ───────────────────────────────────────────────────────────────
  var veil = document.querySelector("[data-search-veil]");
  var input = document.querySelector("[data-search-input]");
  var list = document.querySelector("[data-search-results]");
  var countEl = document.querySelector("[data-search-count]");
  if (!veil || !input || !list) return;

  var index = null;
  var loading = false;
  var results = [];
  var cursor = 0;

  function load() {
    if (index || loading) return;
    loading = true;
    fetch("/search-index.json")
      .then(function (r) { return r.json(); })
      .then(function (data) { index = data; loading = false; run(); })
      .catch(function () { loading = false; list.innerHTML = '<div class="searchempty">The search index could not be loaded. Every page is still reachable from the contents.</div>'; });
  }

  function open() {
    veil.hidden = false;
    load();
    input.focus();
    input.select();
  }
  function close() {
    veil.hidden = true;
    input.blur();
  }

  document.addEventListener("keydown", function (ev) {
    if (ev.key === "/" && veil.hidden && !isTyping(ev.target)) {
      ev.preventDefault();
      open();
      return;
    }
    if (ev.key === "Escape" && !veil.hidden) { close(); return; }
    if (veil.hidden) return;
    if (ev.key === "ArrowDown") { ev.preventDefault(); move(1); }
    else if (ev.key === "ArrowUp") { ev.preventDefault(); move(-1); }
    else if (ev.key === "Enter") {
      var hit = results[cursor];
      if (hit) { ev.preventDefault(); window.location.href = hit.u; }
    }
  });
  function isTyping(el) {
    if (!el) return false;
    var tag = el.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable;
  }

  Array.prototype.forEach.call(document.querySelectorAll("[data-search-open]"), function (b) {
    b.addEventListener("click", open);
  });
  veil.addEventListener("mousedown", function (ev) { if (ev.target === veil) close(); });
  input.addEventListener("input", run);

  function move(delta) {
    if (!results.length) return;
    cursor = (cursor + delta + results.length) % results.length;
    draw();
    var sel = list.querySelector('[aria-selected="true"]');
    if (sel && sel.scrollIntoView) sel.scrollIntoView({ block: "nearest" });
  }

  // score: title prefix beats title substring beats body. Endpoints match on
  // their path, which is what somebody typing "rollback" is looking for.
  function score(entry, q) {
    var t = entry.t.toLowerCase();
    if (t === q) return 100;
    if (t.indexOf(q) === 0) return 80;
    if (t.indexOf(q) >= 0) return 60;
    if ((entry.s || "").toLowerCase().indexOf(q) >= 0) return 30;
    if ((entry.b || "").toLowerCase().indexOf(q) >= 0) return 20;
    return 0;
  }

  function run() {
    var q = input.value.trim().toLowerCase();
    cursor = 0;
    if (!index || q.length < 2) { results = []; draw(); return; }
    results = index
      .map(function (e) { return { e: e, s: score(e, q) }; })
      .filter(function (r) { return r.s > 0; })
      .sort(function (a, b) { return b.s - a.s || a.e.t.localeCompare(b.e.t); })
      .slice(0, 24)
      .map(function (r) { return r.e; });
    draw();
  }

  function esc(s) {
    return String(s).replace(/[&<>"]/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c];
    });
  }

  function draw() {
    if (countEl) countEl.textContent = results.length ? results.length + " results" : "";
    if (!results.length) {
      list.innerHTML = input.value.trim().length < 2
        ? '<div class="searchempty">Type at least two characters. Guides, decisions and API endpoints are all in here.</div>'
        : '<div class="searchempty">Nothing matched. Try a word from the panel — “rollback”, “quota”, “drain”.</div>';
      return;
    }
    var html = "";
    var group = null;
    results.forEach(function (e, i) {
      var label = e.k === "api" ? "API" : e.s;
      if (label !== group) {
        group = label;
        html += '<div class="sgroup">' + esc(label) + "</div>";
      }
      var selected = i === cursor ? ' aria-selected="true"' : "";
      if (e.k === "api") {
        html += '<a class="sresult sapi" href="' + esc(e.u) + '"' + selected + '>' +
          '<span class="chip m-' + esc(String(e.m).toLowerCase()) + '">' + esc(e.m) + "</span>" +
          '<span class="p">' + esc(e.t) + "</span></a>";
        return;
      }
      html += '<a class="sresult" href="' + esc(e.u) + '"' + selected + '>' +
        '<div class="stitle">' + esc(e.t) + "</div>" +
        (e.b ? '<div class="sbody">' + esc(e.b) + "</div>" : "") + "</a>";
    });
    list.innerHTML = html;
  }
})();
