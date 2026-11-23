/*
 * Element tree panel (Automation tab, single-device view). Opens while the Element Picker is on, left of
 * the rail panel. Shows the device UI hierarchy (GET /devices/{id}/ui-tree) with search, two-way sync with
 * the mirror picker (PF.picker, see automation.js), row actions and auto-refresh after gestures.
 * Node shape: {role, text?, label?, resourceId?, bounds{x,y,w,h}, clickable, enabled, focused, children?}.
 * There is no `visible` field, so "Only visible" hides nodes whose bounds are empty or entirely outside the
 * root node's bounds (a node stays if any descendant is on screen).
 */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$, area = $('singleArea');
  if (!area || !PF.picker) return;

  var K_W = 'pf.elementTreeW', K_C = 'pf.elementTreeCollapsed', MIN_W = 220, MAX_W = 560, DEF_W = 300, DRAWER_BP = 1100;
  function lsGet(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
  function lsSet(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* ignore */ } }
  function notify() { setTimeout(function () { window.dispatchEvent(new Event('resize')); }, 0); }

  var width = parseInt(lsGet(K_W), 10); if (!(width >= MIN_W && width <= MAX_W)) width = DEF_W;
  var collapsedPref = lsGet(K_C); // '1' | '0' | null (null: decided by layout mode on first open)
  var collapsed = collapsedPref === '1';
  var decided = collapsedPref !== null;

  /* ---------- DOM ---------- */
  var root = PF.el('div', 'et'); root.id = 'elTree'; root.hidden = true;
  var card = PF.el('div', 'et-card');
  var strip = PF.el('button', 'et-strip', 'Elements'); strip.type = 'button'; strip.title = 'Show element tree';
  var head = PF.el('div', 'et-head');
  var title = PF.el('h2', '', 'Elements'), count = PF.el('span', 'et-count muted', '');
  var hideBtn = PF.el('button', 'et-ib et-hide', '‹'); hideBtn.type = 'button'; hideBtn.title = 'Hide element tree'; hideBtn.setAttribute('aria-label', 'Hide element tree');
  head.appendChild(title); head.appendChild(count); head.appendChild(PF.el('span', 'grow')); head.appendChild(hideBtn);
  var tools = PF.el('div', 'et-tools');
  function tbtn(t, tip) { var b = PF.el('button', 'btn sm', t); b.type = 'button'; b.title = tip; return b; }
  var refBtn = tbtn('Refresh', 'Reload the UI tree'), expBtn = tbtn('Expand', 'Expand all'), colBtn = tbtn('Collapse', 'Collapse all');
  var visLbl = PF.el('label', 'et-vis'), visCb = document.createElement('input'); visCb.type = 'checkbox';
  visLbl.appendChild(visCb); visLbl.appendChild(document.createTextNode(' Only visible'));
  visLbl.title = 'Hide nodes with empty bounds or entirely outside the screen';
  tools.appendChild(refBtn); tools.appendChild(expBtn); tools.appendChild(colBtn); tools.appendChild(visLbl);
  var sbox = PF.el('div', 'et-search');
  var q = document.createElement('input'); q.type = 'text'; q.placeholder = 'Find element'; q.setAttribute('aria-label', 'Find element');
  q.spellcheck = false; q.autocomplete = 'off';
  var reBtn = PF.el('button', 'et-ib et-re', '.*'); reBtn.type = 'button'; reBtn.title = 'Regular expression'; reBtn.setAttribute('aria-pressed', 'false');
  var mcount = PF.el('span', 'et-mcount muted', '');
  var prevBtn = PF.el('button', 'et-ib', '↑'); prevBtn.type = 'button'; prevBtn.title = 'Previous match (Shift+Enter)';
  var nextBtn = PF.el('button', 'et-ib', '↓'); nextBtn.type = 'button'; nextBtn.title = 'Next match (Enter)';
  sbox.appendChild(q); sbox.appendChild(reBtn); sbox.appendChild(mcount); sbox.appendChild(prevBtn); sbox.appendChild(nextBtn);
  var status = PF.el('div', 'et-status muted'); status.hidden = true;
  var list = PF.el('div', 'et-list'); list.tabIndex = 0; list.setAttribute('role', 'tree');
  var grip = PF.el('div', 'et-grip'); grip.title = 'Drag to resize';
  [head, tools, sbox, status, list, grip].forEach(function (n) { card.appendChild(n); });
  root.appendChild(card); root.appendChild(strip);
  area.insertBefore(root, area.firstChild);
  var menu = PF.el('div', 'et-menu'); menu.hidden = true; document.body.appendChild(menu);

  /* ---------- helpers ---------- */
  function shortRole(r) {
    r = String(r || 'node');
    r = r.replace(/^XCUIElementType/, '');
    var i = r.lastIndexOf('.'); if (i >= 0) r = r.slice(i + 1);
    return r || 'node';
  }
  function shortId(id) { id = String(id || ''); var i = id.indexOf(':id/'); return i >= 0 ? id.slice(i + 4) : id; }
  function selectorFor(n) { return PF.picker.selectorFor(n); }
  // Predicate for the DSL (internal/dsl Selector, `predicate` of execute_dsl steps). Fields are exact matches;
  // `index` is the 0-based position among matches in depth-first (pre-order) order, same as rows[] order.
  function predicateFor(n) {
    var fields = [['resourceId', n.resourceId], ['text', n.text], ['label', n.label]].filter(function (f) { return f[1]; });
    var cands = [];
    fields.forEach(function (f) { var o = {}; o[f[0]] = f[1]; cands.push(o); });
    if (n.resourceId) fields.slice(1).forEach(function (f) { var o = { resourceId: n.resourceId }; o[f[0]] = f[1]; cands.push(o); });
    if (n.role) {
      var o2 = { role: n.role };
      if (!fields.length) cands.push(o2); else { var o3 = { role: n.role }; o3[fields[0][0]] = fields[0][1]; cands.push(o3); }
    }
    function hits(o) {
      var out = [];
      for (var i = 0; i < rows.length; i++) {
        var m = rows[i].n, ok = true;
        // mirrors matcher.go matchBase: `text` compares Text, falling back to Label when Text is empty
        for (var k in o) if ((k === 'text' ? (m.text || m.label || '') : (m[k] || '')) !== o[k]) { ok = false; break; }
        if (ok) out.push(m);
      }
      return out;
    }
    var best = null, bestN = 1e9;
    for (var i = 0; i < cands.length; i++) {
      var h = hits(cands[i]);
      if (h.length === 1 && h[0] === n) return cands[i];
      if (h.length && h.indexOf(n) >= 0 && h.length < bestN) { best = cands[i]; bestN = h.length; }
    }
    if (!best) return null;
    var res = {}; for (var kk in best) res[kk] = best[kk];
    var hh = hits(best); res.index = hh.indexOf(n);
    return res;
  }
  function boundsOf(n) { return n.bounds || { x: 0, y: 0, w: 0, h: 0 }; }
  function sameNode(a, b) {
    var x = boundsOf(a), y = boundsOf(b);
    return x.x === y.x && x.y === y.y && x.w === y.w && x.h === y.h && a.role === b.role &&
      (a.resourceId || '') === (b.resourceId || '') && (a.text || '') === (b.text || '') && (a.label || '') === (b.label || '');
  }

  /* ---------- state ---------- */
  var rows = [], byNode = new Map(), treeRoot = null, selRow = null, hoverRow = null;
  var gen = 0, loadedFor = null, reloadTimer = 0, searchTimer = 0;
  var matches = [], cursor = -1, onlyVis = false, useRe = false, queryRe = null, queryStr = '';

  function isOpen() { return !root.hidden && !collapsed && PF.picker.isOn() && !PF.multi && !area.hidden; }
  function setStatus(msg, retry) {
    status.textContent = ''; status.hidden = !msg;
    if (!msg) return;
    if (msg === 'loading') { status.appendChild(PF.el('span', 'et-spin')); status.appendChild(document.createTextNode(' Loading UI tree…')); return; }
    status.appendChild(document.createTextNode(msg));
    if (retry) { var b = tbtn('Retry', 'Reload'); b.onclick = function () { load(false); }; status.appendChild(b); }
  }

  /* ---------- build ---------- */
  function build(tree, keep) {
    var scrollTop = list.scrollTop; PF.picker.hover(null);
    var oldOpen = keep ? keep.open : null, selKey = keep ? keep.selKey : null;
    rows = []; byNode = new Map(); selRow = null; hoverRow = null; matches = []; cursor = -1;
    treeRoot = tree;
    // screen rect: root bounds if non-empty (Android's "hierarchy" root is 0x0), else first non-empty descendant, else none
    var rb = null;
    (function findRb(n) {
      var b = n.bounds; if (b && b.w > 0 && b.h > 0) { rb = b; return true; }
      var ks = n.children || []; for (var i = 0; i < ks.length; i++) if (findRb(ks[i])) return true;
      return false;
    })(tree);
    var frag = document.createDocumentFragment(), total = 0;
    (function count(n) { total++; (n.children || []).forEach(count); })(tree);
    var openAll = total < 300;
    function visit(n, depth, parent, idx) {
      var r = { n: n, depth: depth, parent: parent, kids: [], key: (parent ? parent.key : '') + '/' + idx + ':' + (n.role || '') + ':' + (n.resourceId || ''),
        open: false, match: false, desc: false, shown: true, el: null, caret: null, vis: false };
      r.open = oldOpen && oldOpen.has(r.key) ? oldOpen.get(r.key) : (openAll || depth < 3);
      var b = boundsOf(n);
      r.own = b.w > 0 && b.h > 0 && (!rb || (b.x < rb.x + rb.w && b.y < rb.y + rb.h && b.x + b.w > rb.x && b.y + b.h > rb.y));
      var h = ((n.text || '') + '\n' + (n.label || '') + '\n' + (n.resourceId || '') + '\n' + (n.role || '') + '\n' + shortRole(n.role)).toLowerCase();
      r.hay = h; r.fields = [n.text || '', n.label || '', n.resourceId || '', n.role || '', shortRole(n.role)];
      rows.push(r); byNode.set(n, r);
      if (parent) parent.kids.push(r);
      r.el = rowEl(r); frag.appendChild(r.el);
      var kids = n.children || [];
      for (var i = 0; i < kids.length; i++) visit(kids[i], depth + 1, r, i);
      return r;
    }
    visit(tree, 0, null, 0);
    // on-screen flag propagates to ancestors
    for (var i = rows.length - 1; i >= 0; i--) { var r = rows[i]; if (r.own) r.vis = true; if (r.vis && r.parent) r.parent.vis = true; }
    list.textContent = ''; list.appendChild(frag);
    updateIndent();
    count.textContent = total + (total === 1 ? ' node' : ' nodes');
    if (selKey) for (var j = 0; j < rows.length; j++) if (rows[j].key === selKey) { selRow = rows[j]; break; }
    applyQuery(true);
    if (selRow) { selRow.el.classList.add('sel'); }
    if (keep) list.scrollTop = scrollTop;
  }

  // Indent step adapts to tree depth and list width: clamp(40% of width / maxDepth, 4, 12) px.
  function updateIndent() {
    var md = 1; for (var i = 0; i < rows.length; i++) if (rows[i].depth > md) md = rows[i].depth;
    var step = Math.max(4, Math.min(12, Math.round(list.clientWidth * 0.4 / md * 10) / 10));
    list.style.setProperty('--et-step', step + 'px');
  }

  function rowEl(r) {
    var n = r.n, d = PF.el('div', 'et-row'); d._r = r;
    d.setAttribute('role', 'treeitem');
    d.style.paddingLeft = 'calc(4px + ' + r.depth + ' * var(--et-step, 12px))';
    var c = PF.el('span', 'et-caret', (n.children && n.children.length) ? '▸' : ''); c.setAttribute('data-caret', '1'); r.caret = c; d.appendChild(c);
    d.appendChild(PF.el('span', 'et-role', shortRole(n.role)));
    var t = n.text || n.label;
    if (t) d.appendChild(PF.el('span', 'et-text', '"' + t + '"'));
    var id = shortId(n.resourceId); if (id) d.appendChild(PF.el('span', 'et-id muted', id));
    var b = boundsOf(n);
    d.title = [n.role, n.text ? 'text: ' + n.text : '', n.label ? 'label: ' + n.label : '', n.resourceId ? 'id: ' + n.resourceId : '',
      'bounds: [' + [b.x, b.y, b.w, b.h].join(', ') + ']', ['clickable', 'enabled', 'focused'].filter(function (k) { return n[k]; }).join(', ')]
      .filter(Boolean).join('\n');
    var acts = PF.el('span', 'et-acts');
    [['Tap', '◎', 'Tap center'], ['sel', '{}', 'Copy selector'], ['dsl', 'DSL', 'Copy as DSL predicate']].forEach(function (a) {
      var bt = PF.el('button', 'et-ib', a[1]); bt.type = 'button'; bt.title = a[2]; bt.setAttribute('aria-label', a[2]); bt.setAttribute('data-act', a[0]);
      acts.appendChild(bt);
    });
    d.appendChild(acts);
    return d;
  }

  /* ---------- visibility ---------- */
  function layoutRows() {
    var searching = !!queryStr;
    for (var i = 0; i < rows.length; i++) {
      var r = rows[i], p = r.parent;
      var pass = (!searching || r.match || r.desc) && (!onlyVis || r.vis);
      var parentOpen = !p || (p.shown && (p.open || (searching && p.desc)));
      r.shown = parentOpen && pass;
      r.el.classList.toggle('h', !r.shown);
      r.el.classList.toggle('m', searching && r.match);
      if (r.kids.length) {
        var eo = r.open || (searching && r.desc);
        r.caret.textContent = eo ? '▾' : '▸';
        r.el.setAttribute('aria-expanded', eo ? 'true' : 'false');
      }
    }
  }

  /* ---------- search ---------- */
  function applyQuery(silent) {
    var s = q.value, bad = false; queryRe = null;
    if (s && useRe) { try { queryRe = new RegExp(s, 'i'); } catch (e) { bad = true; } }
    q.classList.toggle('bad', bad);
    if (bad) { mcount.textContent = 'invalid'; return; }
    queryStr = s;
    var needle = s.toLowerCase(), i, r;
    for (i = 0; i < rows.length; i++) { rows[i].match = false; rows[i].desc = false; }
    if (s) {
      for (i = 0; i < rows.length; i++) {
        r = rows[i];
        r.match = queryRe ? r.fields.some(function (f) { return f && queryRe.test(f); }) : r.hay.indexOf(needle) >= 0;
      }
      for (i = rows.length - 1; i >= 0; i--) { r = rows[i]; if ((r.match || r.desc) && r.parent) r.parent.desc = true; }
    }
    layoutRows();
    matches = s ? rows.filter(function (r) { return r.match && r.shown; }) : [];
    cursor = -1;
    if (selRow) { var k = matches.indexOf(selRow); if (k >= 0) cursor = k; }
    updateMCount();
  }
  function updateMCount() {
    if (!queryStr) { mcount.textContent = ''; return; }
    mcount.textContent = (cursor >= 0 ? cursor + 1 : 0) + ' / ' + matches.length;
  }
  function step(dir) {
    if (!matches.length) return;
    cursor = cursor < 0 ? (dir > 0 ? 0 : matches.length - 1) : (cursor + dir + matches.length) % matches.length;
    selectRow(matches[cursor], {});
    updateMCount();
  }
  q.addEventListener('input', function () { clearTimeout(searchTimer); searchTimer = setTimeout(function () { applyQuery(); }, 120); });
  q.addEventListener('keydown', function (e) {
    if (e.key === 'Enter') { e.preventDefault(); clearTimeout(searchTimer); applyQuery(); step(e.shiftKey ? -1 : 1); }
    else if (e.key === 'Escape' && q.value) { e.stopPropagation(); q.value = ''; applyQuery(); }
  });
  reBtn.addEventListener('click', function () { useRe = !useRe; reBtn.classList.toggle('on', useRe); reBtn.setAttribute('aria-pressed', useRe ? 'true' : 'false'); applyQuery(); });
  prevBtn.addEventListener('click', function () { step(-1); });
  nextBtn.addEventListener('click', function () { step(1); });
  visCb.addEventListener('change', function () { onlyVis = visCb.checked; layoutRows(); applyQuery(); });

  /* ---------- selection ---------- */
  function selectRow(r, opt) {
    opt = opt || {};
    if (!r) return;
    for (var p = r.parent; p; p = p.parent) p.open = true;
    // make sure the row is not filtered out
    if (queryStr && !r.match && !r.desc && opt.revealing) { q.value = ''; }
    if (opt.revealing && onlyVis && !r.vis) { visCb.checked = false; onlyVis = false; }
    if (selRow && selRow.el) selRow.el.classList.remove('sel');
    selRow = r; r.el.classList.add('sel');
    applyQueryKeepSel();
    if (!opt.noPicker) PF.picker.select(r.n);
    r.el.scrollIntoView({ block: 'nearest' });
  }
  function applyQueryKeepSel() { var qs = q.value; if (qs === queryStr && !queryRe) { layoutRows(); } else applyQuery(); updateMCount(); }
  function clearSel() { if (selRow && selRow.el) selRow.el.classList.remove('sel'); selRow = null; cursor = -1; updateMCount(); }

  function findRow(el, tree) {
    var r = byNode.get(el); if (r) return r;
    for (var i = 0; i < rows.length; i++) if (sameNode(rows[i].n, el)) return rows[i];
    return null;
  }
  PF.onElementPicked = function (el, tree) {
    if (!isOpen()) return;
    var r = findRow(el);
    if (!r && tree) { build(tree, keepState()); setStatus(''); r = findRow(el); }
    if (r) selectRow(r, { noPicker: true, revealing: true });
    fit();
  };
  PF.onPickCleared = function () { if (selRow) clearSel(); };

  /* ---------- load ---------- */
  function keepState() {
    var open = new Map(); rows.forEach(function (r) { if (r.kids.length) open.set(r.key, r.open); });
    return { open: open, selKey: selRow ? selRow.key : null };
  }
  function clearTree() {
    rows = []; byNode = new Map(); treeRoot = null; selRow = null; hoverRow = null; matches = []; cursor = -1;
    list.textContent = ''; count.textContent = ''; mcount.textContent = '';
  }
  function load(keep) {
    var id = PF.selectedId, g = ++gen;
    if (!id) { clearTree(); loadedFor = null; setStatus('Select a device'); return; }
    var silent = !!(keep && loadedFor === id && treeRoot);
    if (!silent) { clearTree(); setStatus('loading'); }
    loadedFor = id;
    PF.picker.getTree(true).then(function (tree) {
      if (g !== gen) return;
      if (!tree) throw new Error('Empty UI tree');
      var k = silent && treeRoot ? keepState() : null; // taken now, so it reflects the user's latest expansion/selection
      var prevSel = selRow && k ? selRow.n : null;
      build(tree, k); setStatus('');
      if (selRow && prevSel) PF.picker.select(selRow.n); // refresh highlight/inspector with the new node
      else if (!selRow) { var pk = PF.picker.picked && PF.picker.picked(), pr = pk && findRow(pk); if (pr) selectRow(pr, { noPicker: true, revealing: true }); }
    }).catch(function (e) {
      if (g !== gen) return;
      if (silent && treeRoot) return; // keep the stale tree on silent refresh failures
      clearTree();
      var msg = e && e.code === 'WDA_REQUIRED' ? (e.serverMessage || 'Start WDA to see the screen of iOS 17+ devices') : 'Could not load UI tree: ' + (e && e.message || e);
      setStatus(msg, true);
    });
  }
  function ensureLoaded() { if (!isOpen()) return; if (!treeRoot || loadedFor !== PF.selectedId) load(false); }

  /* ---------- row interactions ---------- */
  function toggleRow(r) {
    if (!r.kids.length) return;
    r.open = !r.open;
    layoutRows(); matches = queryStr ? rows.filter(function (x) { return x.match && x.shown; }) : []; updateMCount();
  }
  function doCopy(s) { PF.copyText(s, s); }
  function act(a, r) {
    if (a === 'Tap') PF.picker.tap(r.n);
    else if (a === 'sel') doCopy(JSON.stringify(selectorFor(r.n)));
    else if (a === 'dsl') { var pr = predicateFor(r.n); if (pr) doCopy(JSON.stringify(pr)); else PF.toast('No usable predicate', true); }
  }
  list.addEventListener('click', function (e) {
    var rowEl = e.target.closest ? e.target.closest('.et-row') : null; if (!rowEl) return;
    var r = rowEl._r, ab = e.target.closest('[data-act]');
    if (ab) { e.stopPropagation(); act(ab.getAttribute('data-act'), r); return; }
    if (e.target.getAttribute && e.target.getAttribute('data-caret')) { toggleRow(r); return; }
    selectRow(r, {});
    var k = matches.indexOf(r); if (k >= 0) { cursor = k; updateMCount(); }
    list.focus({ preventScroll: true });
  });
  list.addEventListener('dblclick', function (e) {
    var rowEl = e.target.closest ? e.target.closest('.et-row') : null;
    if (rowEl && !e.target.closest('[data-act]')) toggleRow(rowEl._r);
  });
  list.addEventListener('mouseover', function (e) {
    var rowEl = e.target.closest ? e.target.closest('.et-row') : null, r = rowEl && rowEl._r;
    if (r === hoverRow) return;
    hoverRow = r || null; PF.picker.hover(r ? r.n : null);
  });
  list.addEventListener('mouseleave', function () { hoverRow = null; PF.picker.hover(null); });
  list.addEventListener('keydown', function (e) {
    if (!selRow || e.altKey || e.ctrlKey || e.metaKey) {
      if (!selRow && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) { var f = rows.filter(function (r) { return r.shown; })[0]; if (f) { e.preventDefault(); selectRow(f, {}); } }
      return;
    }
    var vis = rows.filter(function (r) { return r.shown; }), i = vis.indexOf(selRow), r = selRow;
    if (e.key === 'ArrowDown') { e.preventDefault(); if (vis[i + 1]) selectRow(vis[i + 1], {}); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); if (i > 0) selectRow(vis[i - 1], {}); }
    else if (e.key === 'ArrowRight') {
      e.preventDefault();
      if (r.kids.length && !r.open) { r.open = true; layoutRows(); } else if (r.kids.length) { var c = r.kids.filter(function (k) { return k.shown; })[0]; if (c) selectRow(c, {}); }
    } else if (e.key === 'ArrowLeft') {
      e.preventDefault();
      if (r.kids.length && r.open) { r.open = false; layoutRows(); } else if (r.parent) selectRow(r.parent, {});
    } else if (e.key === 'Enter') { e.preventDefault(); selectRow(r, {}); }
  });
  list.addEventListener('contextmenu', function (e) {
    var rowEl = e.target.closest ? e.target.closest('.et-row') : null; if (!rowEl) return;
    e.preventDefault(); selectRow(rowEl._r, {});
    openMenu(e.clientX, e.clientY, rowEl._r);
  });
  function openMenu(x, y, r) {
    menu.textContent = '';
    [['Tap', 'Tap'], ['sel', 'Copy selector'], ['dsl', 'Copy as DSL predicate']].forEach(function (a) {
      var b = PF.el('button', '', a[1]); b.type = 'button';
      b.onclick = function () { closeMenu(); act(a[0], r); };
      menu.appendChild(b);
    });
    menu.hidden = false;
    var w = menu.offsetWidth, h = menu.offsetHeight;
    menu.style.left = Math.max(4, Math.min(x, window.innerWidth - w - 4)) + 'px';
    menu.style.top = Math.max(4, Math.min(y, window.innerHeight - h - 4)) + 'px';
  }
  function closeMenu() { menu.hidden = true; }
  document.addEventListener('mousedown', function (e) { if (!menu.hidden && !menu.contains(e.target)) closeMenu(); });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !menu.hidden) { closeMenu(); e.stopPropagation(); } }, true);
  window.addEventListener('blur', closeMenu);
  list.addEventListener('scroll', closeMenu);

  expBtn.addEventListener('click', function () { rows.forEach(function (r) { r.open = true; }); applyQuery(); });
  colBtn.addEventListener('click', function () { rows.forEach(function (r) { r.open = false; }); if (rows[0]) rows[0].open = true; applyQuery(); });
  refBtn.addEventListener('click', function () { load(true); });

  /* ---------- panel open / collapse / layout ---------- */
  /* Layout: inline (flex item left of the rails) when panel + rails + phone + visible inspector fit in the
     area, else a drawer overlaying only the space left of the phone. Inputs (area width, phone height-derived
     width, rail/inspector widths) do not depend on the panel's own mode, so there is no inline/drawer flip-flop. */
  function fit() {
    if (root.hidden) return;
    var phone = area.querySelector('.phone'), ar = area.getBoundingClientRect();
    var rp = $('railPanel'), ins = $('inspector');
    var phoneW = phone ? Math.round(phone.offsetHeight * 9 / 19) : 300; // border-box width from aspect-ratio 9/19; independent of layout mode
    var railW = rp && !rp.hidden ? rp.offsetWidth + 12 : 0;
    var insW = ins && !ins.hidden ? ins.offsetWidth + 12 : 0;
    var room = ar.width - 20 - (phoneW + 12 + railW + insW) - 12; // what is left for the panel when inline
    var inline = room >= MIN_W, narrow = false;
    root.classList.toggle('drawer', !inline);
    if (inline) card.style.width = Math.min(width, Math.floor(room)) + 'px';
    else if (phone) {
      var avail = Math.floor(phone.getBoundingClientRect().left - ar.left - 8 - 10);
      if (avail < 160) narrow = true; else card.style.width = Math.min(width, avail) + 'px';
    }
    root.classList.remove('nofit');
    if (!decided) { decided = true; collapsed = !inline; }
    root.classList.toggle('collapsed', collapsed || narrow);
  }
  function sync() {
    var on = PF.picker.isOn() && !PF.multi && !area.hidden;
    root.hidden = !on;
    if (on) { fit(); ensureLoaded(); } else { clearTimeout(reloadTimer); }
    if (!on) PF.picker.hover(null);
  }
  function setCollapsed(c) {
    collapsed = c; decided = true; lsSet(K_C, c ? '1' : '0');
    root.classList.toggle('collapsed', c);
    if (!c) {
      ensureLoaded();
      var pk = PF.picker.picked && PF.picker.picked();
      if (pk && treeRoot) { var pr = findRow(pk); if (pr) selectRow(pr, { noPicker: true, revealing: true }); }
    }
    notify();
  }
  hideBtn.addEventListener('click', function () { setCollapsed(true); });
  strip.addEventListener('click', function () { setCollapsed(false); });
  PF.onPickerToggle = function (on) { if (!on) { clearSel(); } sync(); notify(); };
  window.addEventListener('resize', fit);
  if (window.ResizeObserver) {
    var ro = new ResizeObserver(function () { fit(); });
    new ResizeObserver(updateIndent).observe(list);
    [area, area.querySelector('.phone'), $('inspector'), $('railPanel')].forEach(function (n) { if (n) ro.observe(n); });
  }
  new MutationObserver(function () { sync(); }).observe(area, { attributes: true, attributeFilter: ['hidden'] });
  $('multiToggle').addEventListener('change', function () { setTimeout(sync, 0); });

  PF.onGestureSent = function () {
    if (!isOpen()) return;
    clearTimeout(reloadTimer);
    reloadTimer = setTimeout(function () { if (isOpen()) load(true); }, 800);
  };
  PF.hooks.onSelect.push(function () {
    clearTimeout(reloadTimer); gen++; clearTree(); loadedFor = null; setStatus(PF.selectedId ? '' : 'Select a device');
    if (isOpen()) load(false);
  });

  /* ---------- resize handle ---------- */
  var drag = null;
  grip.addEventListener('pointerdown', function (e) {
    drag = { x: e.clientX, w: card.getBoundingClientRect().width, id: e.pointerId };
    try { grip.setPointerCapture(e.pointerId); } catch (err) { /* ignore */ }
    root.classList.add('resizing'); e.preventDefault();
  });
  grip.addEventListener('pointermove', function (e) {
    if (!drag || e.pointerId !== drag.id) return;
    width = Math.max(MIN_W, Math.min(MAX_W, Math.round(drag.w + e.clientX - drag.x)));
    fit();
    window.dispatchEvent(new Event('resize'));
  });
  function endDrag(e) {
    if (!drag || e.pointerId !== drag.id) return;
    drag = null; root.classList.remove('resizing'); lsSet(K_W, String(width)); notify();
  }
  grip.addEventListener('pointerup', endDrag);
  grip.addEventListener('pointercancel', endDrag);

  setStatus(PF.selectedId ? '' : 'Select a device');
  sync();
})();
