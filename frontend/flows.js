/*
 * Testing > Flows sub-tab: workflow "flow" cases (YAML), cache viewer, runs with a live SSE timeline.
 * API base /api/v1/flows. All server text is inserted with textContent (never innerHTML).
 * The timeline does not interpret individual SSE payloads: any event schedules a debounced
 * GET /runs/{id} and the whole timeline is rendered from the Run JSON (robust to event schema changes).
 */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$, el = PF.el;
  var NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
  var TEMPLATE = 'version: 1\nname: New flow\napp:\n  bundleId: com.example.app\n  platform: any\nparams: {}\nsteps:\n  - id: s1\n    type: act\n    title: First step\n    do: Open the app\n    expect: The home screen is visible\n';

  var projects = [], pid = null, cases = [], cur = null; // cur: {project, name, saved, info}
  var mid = 'editor';           // editor | cache
  var run = null, es = null, refreshTimer = null, runs = [], compare = null, cacheState = null, histEntries = [], histSel = null;
  var root, ui = {}, inited = false, valSeq = 0;
  try { pid = localStorage.getItem('pf.fproj') || null; } catch (e) { /* ignore */ }

  /* ---------- http ---------- */
  function req(method, path, body, ct) {
    var opts = { method: method, headers: {} };
    if (body !== undefined) { opts.body = body; opts.headers['Content-Type'] = ct || 'text/plain'; }
    return fetch(PF.API + path, opts).then(function (res) {
      return res.text().then(function (t) {
        var j = null; try { j = t ? JSON.parse(t) : null; } catch (e) { /* raw */ }
        if (!res.ok) {
          var msg = (j && (j.error || j.message)) || (t && t.length < 200 ? t : '') || ('HTTP ' + res.status);
          if (res.status === 501) msg = 'Not implemented on this server yet: ' + msg;
          var e = new Error(msg);
          e.status = res.status; e.code = j && j.code; e.errors = j && j.errors;
          throw e;
        }
        return j;
      });
    }, function (err) { throw new Error('Network error: ' + err.message); });
  }
  function jbody(o) { return JSON.stringify(o); }
  function fail(e) { PF.toast(e && e.message ? e.message : String(e)); }
  function casePath(c, suffix) { return '/flows/cases/' + encodeURIComponent(c.project) + '/' + encodeURIComponent(c.name) + (suffix || ''); }
  function artifact(runId, file) { return PF.API + '/flows/runs/' + encodeURIComponent(runId) + '/artifacts/' + encodeURIComponent(file); }

  /* ---------- helpers ---------- */
  function fmtMs(ms) { return ms == null ? '' : ms >= 1000 ? (ms / 1000).toFixed(1) + ' s' : Math.round(ms) + ' ms'; }
  function fmtTime(t) { var d = new Date(t); return isNaN(d) ? String(t || '') : d.toLocaleString(); }
  function chipClass(s) {
    s = String(s || '').toLowerCase();
    if (s === 'passed' || s === 'pass' || s === 'ok' || s === 'success' || s === 'fresh' || s === 'done') return 'ok';
    if (s === 'failed' || s === 'fail' || s === 'error' || s === 'cancelled' || s === 'canceled') return 'failed';
    if (s === 'running' || s === 'stale' || s === 'healed' || s === 'queued' || s === 'pending') return 'warn';
    return 'skipped';
  }
  function chip(text, cls) { return el('span', 'chip ' + (cls || chipClass(text)), text); }
  function clear(n) { while (n.firstChild) n.removeChild(n.firstChild); return n; }
  function dur(r) {
    if (r.durationMs != null) return fmtMs(r.durationMs);
    if (r.startedAt && r.endedAt) return fmtMs(new Date(r.endedAt) - new Date(r.startedAt));
    return '';
  }
  function isActive(r) { return r && (r.status === 'running' || r.status === 'queued' || r.status === 'pending' || r.status === 'starting'); }
  function cacheBadge(c) {
    var ca = c.cache || {};
    if (!ca.present) return chip('none', 'skipped');
    if (ca.stale || ca.missing) return chip('stale ' + ((ca.stale || 0) + (ca.missing || 0)), 'warn');
    return chip('fresh', 'ok');
  }
  function enlarge(src, title) {
    var m = PF.modal.open(title || 'Screenshot', { wide: true });
    var img = el('img', 'runimg'); img.src = src; img.alt = title || 'screenshot';
    m.body.appendChild(img);
  }
  function btn(text, cls, fn, title) { var b = PF.btn(text, cls, fn); if (title) b.title = title; return b; }
  function section(title) { var h = el('div', 'col-head'); h.appendChild(el('h2', '', title)); return h; }

  /* ---------- DOM skeleton ---------- */
  function build() {
    root = $('flowsView');
    var left = el('aside', 'ws-col ws-left fl-col'), center = el('section', 'ws-col fl-col fl-mid'), right = el('aside', 'ws-col ws-right fl-col');

    var h = section('Project'); left.appendChild(h);
    ui.proj = el('select', 'fl-sel'); ui.proj.addEventListener('change', function () { selectProject(ui.proj.value); });
    var pw = el('div', 'tpad'); pw.appendChild(ui.proj); left.appendChild(pw);
    h = section('Cases');
    ui.newBtn = btn('+ New', 'sm', newCase); ui.newBtn.disabled = true; h.appendChild(ui.newBtn);
    left.appendChild(h);
    ui.newForm = el('div', 'inline-form tpad'); ui.newForm.hidden = true; left.appendChild(ui.newForm);
    ui.list = el('ul', 'tlist'); left.appendChild(ui.list);
    h = section('History'); ui.histReload = btn('↻', 'sm', loadRuns, 'Reload'); h.appendChild(ui.histReload); left.appendChild(h);
    ui.runs = el('ul', 'tlist runs'); left.appendChild(ui.runs);

    h = el('div', 'col-head');
    ui.title = el('h2', '', 'Flow'); h.appendChild(ui.title);
    ui.dirty = el('span', 'dirty', '●'); ui.dirty.hidden = true; ui.dirty.title = 'Unsaved changes'; h.appendChild(ui.dirty);
    h.appendChild(el('span', 'grow'));
    ui.tabEd = btn('Editor', 'sm', function () { setMid('editor'); }); ui.tabCache = btn('Cache', 'sm', function () { setMid('cache'); });
    ui.valBtn = btn('Validate', 'sm', function () { validate(true); }); ui.saveBtn = btn('Save', 'sm primary', save);
    [ui.tabEd, ui.tabCache, ui.valBtn, ui.saveBtn].forEach(function (b) { h.appendChild(b); });
    center.appendChild(h);
    ui.empty = el('div', 'muted tpad', 'Select or create a flow case.'); center.appendChild(ui.empty);
    ui.editor = el('div', 'fl-editor'); ui.editor.hidden = true;
    ui.text = el('textarea', 'fl-text'); ui.text.spellcheck = false; ui.text.setAttribute('wrap', 'off');
    ui.text.addEventListener('input', onEdit);
    ui.errs = el('div', 'errs'); ui.errs.hidden = true;
    ui.steps = el('div', 'fl-steps');
    ui.editor.appendChild(ui.text); ui.editor.appendChild(ui.errs); ui.editor.appendChild(ui.steps);
    center.appendChild(ui.editor);
    ui.cache = el('div', 'fl-cache'); ui.cache.hidden = true; center.appendChild(ui.cache);

    h = section('Run');
    ui.cancel = btn('Cancel', 'sm danger', cancelRun); ui.cancel.disabled = true; h.appendChild(ui.cancel);
    right.appendChild(h);
    var rc = el('div', 'tpad fl-ctl');
    ui.dev = el('select', 'fl-sel'); rc.appendChild(el('label', 'muted', 'Device')); rc.appendChild(ui.dev);
    ui.params = el('div', 'fl-params'); rc.appendChild(ui.params);
    var row = el('div', 'btn-row');
    ui.bAgent = btn('Run with agent', 'sm primary', function () { start('record', false); }, 'Record: the agent performs the flow and the cache is saved');
    ui.bReplay = btn('Replay', 'sm', function () { start('replay', false); }, 'Replay the cache without contacting an agent');
    ui.bHeal = btn('Replay + heal', 'sm', function () { start('replay', true); }, 'Replay and let the agent repair broken segments');
    [ui.bAgent, ui.bReplay, ui.bHeal].forEach(function (b) { row.appendChild(b); });
    rc.appendChild(row); right.appendChild(rc);
    h = section('Timeline'); right.appendChild(h);
    ui.tl = el('div', 'tpad muted fl-tl', 'No run selected.'); right.appendChild(ui.tl);

    root.appendChild(left); root.appendChild(center); root.appendChild(right);
    PF.hooks.onDevices.push(renderDevices);
    PF.hooks.onSelect.push(renderDevices);
    renderDevices();
  }

  /* ---------- devices & params ---------- */
  function renderDevices() {
    if (!ui.dev) return;
    var keep = ui.dev.value || PF.selectedId;
    clear(ui.dev);
    var list = PF.devices || [];
    if (!list.length) { var o = el('option', '', 'No devices'); o.value = ''; ui.dev.appendChild(o); }
    list.forEach(function (d) {
      var o = el('option', '', (d.name || d.model || d.id) + (d.platform ? ' (' + d.platform + ')' : '')); o.value = d.id; ui.dev.appendChild(o);
    });
    var want = PF.selectedId || keep;
    if (want) ui.dev.value = want;
  }
  function renderParams(entry) {
    clear(ui.params);
    var p = (entry && entry.case && entry.case.params) || {};
    Object.keys(p).sort().forEach(function (k) {
      var r = el('div', 'fl-prow'); r.appendChild(el('label', 'muted', k));
      var i = el('input'); i.type = 'text'; i.value = (p[k] && p[k].default) || ''; i.setAttribute('data-k', k); r.appendChild(i); ui.params.appendChild(r);
    });
  }
  function readParams() {
    var o = {};
    Array.prototype.forEach.call(ui.params.querySelectorAll('input'), function (i) { o[i.getAttribute('data-k')] = i.value; });
    return o;
  }

  /* ---------- projects & cases ---------- */
  function loadProjects() {
    return req('GET', '/tests/projects').then(function (list) {
      projects = list || [];
      if (pid && !projects.some(function (p) { return p.id === pid; })) pid = null;
      if (!pid && projects.length) pid = projects[0].id;
      clear(ui.proj);
      if (!projects.length) { var o = el('option', '', 'No projects'); o.value = ''; ui.proj.appendChild(o); }
      projects.forEach(function (p) { var o = el('option', '', p.name || p.id); o.value = p.id; ui.proj.appendChild(o); });
      if (pid) ui.proj.value = pid;
      ui.newBtn.disabled = !pid;
      return loadCases();
    }).catch(function (e) { clear(ui.list).appendChild(el('li', 'muted', e.message)); });
  }
  function selectProject(id) {
    if (guardDirty(function () { selectProject(id); })) return;
    pid = id || null; ui.newBtn.disabled = !pid;
    try { localStorage.setItem('pf.fproj', pid || ''); } catch (e) { /* ignore */ }
    closeCase();
    loadCases();
  }
  function loadCases() {
    clear(ui.list);
    if (!pid) { ui.list.appendChild(el('li', 'muted', 'Create a project in the Workspace tab first.')); return Promise.resolve(); }
    return req('GET', '/flows/cases?project=' + encodeURIComponent(pid)).then(function (l) {
      cases = l || []; renderCases();
    }).catch(function (e) { cases = []; clear(ui.list).appendChild(el('li', 'muted', e.message)); });
  }
  function renderCases() {
    clear(ui.list);
    if (!cases.length) ui.list.appendChild(el('li', 'muted', 'No flows yet. Create one with + New.'));
    cases.forEach(function (c) {
      var li = el('li', cur && cur.name === c.name && cur.project === c.project ? 'sel' : '');
      li.title = (c.title || c.name) + ' (' + c.steps + ' steps)';
      li.appendChild(el('span', 'n', c.title || c.name));
      if (!c.valid) li.appendChild(chip('invalid', 'failed'));
      li.appendChild(cacheBadge(c));
      var x = el('button', 'btn sm x', '✕'); x.type = 'button'; x.title = 'Delete case';
      x.addEventListener('click', function (ev) {
        ev.stopPropagation();
        PF.confirm('Delete flow "' + c.name + '" and its cache?', function () {
          req('DELETE', casePath(c)).then(function () {
            if (cur && cur.name === c.name && cur.project === c.project) closeCase();
            return loadCases();
          }).catch(fail);
        }, { title: 'Delete flow?', okLabel: 'Delete', danger: true });
      });
      li.appendChild(x);
      li.addEventListener('click', function () { openCase(c); });
      ui.list.appendChild(li);
    });
  }
  function newCase() {
    var f = clear(ui.newForm); f.hidden = false;
    var i = el('input'); i.type = 'text'; i.placeholder = 'case-name'; f.appendChild(i);
    var go = function () {
      var n = i.value.trim().replace(/\.(ya?ml|flow)$/i, '');
      if (!NAME_RE.test(n)) { PF.toast('Use letters, digits, . _ - (start with a letter or digit)'); return; }
      f.hidden = true;
      var c = { project: pid, name: n };
      req('PUT', casePath(c), TEMPLATE, 'text/plain').then(function () { return loadCases(); }).then(function () {
        var info = cases.filter(function (x) { return x.name === n; })[0]; if (info) openCase(info);
      }).catch(fail);
    };
    f.appendChild(btn('Create', 'sm primary', go)); f.appendChild(btn('Cancel', 'sm', function () { f.hidden = true; }));
    i.addEventListener('keydown', function (e) { if (e.key === 'Enter') go(); if (e.key === 'Escape') f.hidden = true; });
    i.focus();
  }

  /* ---------- editor ---------- */
  function dirty() { return !!cur && ui.text.value !== cur.saved; }
  function guardDirty(again) {
    if (!dirty()) return false;
    PF.confirm('Discard unsaved changes?', function () { cur.saved = ui.text.value; again(); }, { title: 'Unsaved changes', okLabel: 'Discard', danger: true });
    return true;
  }
  function closeCase() {
    cur = null; ui.title.textContent = 'Flow'; ui.dirty.hidden = true;
    ui.editor.hidden = true; ui.cache.hidden = true; ui.empty.hidden = false;
    clear(ui.params); renderCases();
  }
  function openCase(c) {
    if (guardDirty(function () { openCase(c); })) return;
    req('GET', casePath(c)).then(function (e) {
      cur = { project: c.project, name: c.name, saved: e.source || '', entry: e };
      ui.title.textContent = c.name;
      ui.text.value = cur.saved; ui.empty.hidden = true; ui.dirty.hidden = true;
      showErrors(e.errors || []); renderSteps(e);
      renderParams(e); renderCases(); setMid(mid);
      loadRuns();
    }).catch(fail);
  }
  function setMid(m) {
    mid = m;
    ui.editor.hidden = !cur || m !== 'editor'; ui.cache.hidden = !cur || m !== 'cache';
    ui.tabEd.classList.toggle('primary', m === 'editor'); ui.tabCache.classList.toggle('primary', m === 'cache');
    ui.valBtn.hidden = ui.saveBtn.hidden = m !== 'editor';
    if (cur && m === 'cache') loadCache();
  }
  function onEdit() {
    ui.dirty.hidden = !dirty();
    clearTimeout(onEdit.t); onEdit.t = setTimeout(function () { validate(false); }, 600);
  }
  function showErrors(errs) {
    clear(ui.errs);
    ui.errs.hidden = false;
    if (!errs.length) { ui.errs.appendChild(el('div', 'okline', 'Valid.')); return; }
    errs.forEach(function (e) {
      var d = el('div', 'e');
      d.appendChild(el('b', '', e.step ? 'step ' + e.step : 'case'));
      d.appendChild(document.createTextNode((e.field ? e.field + ': ' : '') + e.message));
      ui.errs.appendChild(d);
    });
  }
  function validate(loud) {
    var seq = ++valSeq;
    return req('POST', '/flows/cases/validate', ui.text.value, 'text/plain').then(function (r) {
      if (seq !== valSeq) return null;
      showErrors((r && r.errors) || []);
      if (loud) PF.toast(r && r.ok ? 'Valid' : 'Validation errors found', true);
      return r;
    }).catch(function (e) { if (loud) fail(e); });
  }
  function save() {
    if (!cur) return;
    var src = ui.text.value, c = cur;
    req('PUT', casePath(c), src, 'text/plain').then(function (e) {
      c.saved = src; c.entry = e; ui.dirty.hidden = !dirty();
      showErrors((e && e.errors) || []); renderSteps(e); renderParams(e);
      PF.toast('Saved', true);
      return loadCases();
    }).catch(function (e) {
      if (e.status === 400 && e.errors) { showErrors(e.errors); PF.toast('Not saved: invalid case'); } else fail(e);
    });
  }
  function renderSteps(entry, staleness) {
    clear(ui.steps);
    var steps = (entry && entry.case && entry.case.steps) || [];
    if (!steps.length) return;
    ui.steps.appendChild(el('div', 'muted fl-sh', 'Steps (preview of the saved case)'));
    staleness = staleness || cur && cur.staleness || {};
    steps.forEach(function (s) {
      var d = el('div', 'fl-step');
      var top = el('div', 'fl-steptop');
      top.appendChild(el('code', '', s.id)); top.appendChild(chip(s.type || '?', 'skipped'));
      top.appendChild(el('strong', 'n', s.title || ''));
      if (staleness[s.id]) top.appendChild(chip(staleness[s.id]));
      d.appendChild(top);
      if (s.do) d.appendChild(el('div', 'fl-do', 'do: ' + s.do));
      if (s.expect) d.appendChild(el('div', 'fl-do muted', 'expect: ' + s.expect));
      ui.steps.appendChild(d);
    });
    if (!staleness || cur && !cur.staleness) loadStaleness();
  }
  function loadStaleness() {
    if (!cur) return;
    var c = cur;
    req('GET', casePath(c, '/cache')).then(function (v) {
      if (cur !== c) return;
      c.staleness = (v && v.staleness) || {};
      renderSteps(c.entry, c.staleness);
    }).catch(function () { /* optional */ });
  }

  /* ---------- run control ---------- */
  function start(mode, heal) {
    if (!cur) { PF.toast('Select a flow first', true); return; }
    if (dirty()) { PF.toast('Save the flow before running', true); return; }
    var dev = ui.dev.value || PF.selectedId;
    if (!dev) { PF.toast('Select a device first', true); return; }
    var body = { project: cur.project, case: cur.name, deviceId: dev, mode: mode, heal: !!heal, params: readParams() };
    if (mode === 'record') body.agent = 'default';
    setBusy(true);
    req('POST', '/flows/runs', jbody(body), 'application/json').then(function (r) {
      var id = r && (r.run ? r.run.id : r.id);
      if (!id) throw new Error('Server returned no run id');
      return loadRuns().then(function () { openRun(id); });
    }).catch(function (e) { setBusy(false); fail(e); });
  }
  function setBusy(b) {
    [ui.bAgent, ui.bReplay, ui.bHeal].forEach(function (x) { x.disabled = b; });
  }
  function cancelRun() {
    if (!run) return;
    req('POST', '/flows/runs/' + encodeURIComponent(run.id) + '/cancel').then(function () { scheduleRefresh(); }).catch(fail);
  }

  /* ---------- timeline / SSE ---------- */
  function closeStream() {
    if (es) { es.close(); es = null; }
    clearTimeout(refreshTimer); refreshTimer = null;
  }
  function openRun(id) {
    closeStream();
    run = { id: id, status: 'loading', steps: [] };
    renderTimeline();
    fetchRun(id);
    if (typeof EventSource === 'undefined') return;
    es = new EventSource(PF.API + '/flows/runs/' + encodeURIComponent(id) + '/events');
    ['run', 'step', 'substep', 'assert', 'screenshot', 'log'].forEach(function (t) {
      es.addEventListener(t, scheduleRefresh);
    });
    es.addEventListener('done', function () { closeStream(); fetchRun(id); loadRuns(); loadCases(); if (mid === 'cache') loadCache(); });
    es.onerror = function () { /* server closes the stream after done; EventSource would auto-reconnect */
      if (es && es.readyState === 2) closeStream();
      else if (run && !isActive(run)) closeStream();
    };
  }
  function scheduleRefresh() {
    if (refreshTimer || !run) return;
    var id = run.id;
    refreshTimer = setTimeout(function () { refreshTimer = null; fetchRun(id); }, 250);
  }
  function fetchRun(id) {
    return req('GET', '/flows/runs/' + encodeURIComponent(id)).then(function (r) {
      if (!run || run.id !== id) return;
      run = r; renderTimeline();
      if (!isActive(r)) { closeStream(); setBusy(false); }
    }).catch(function (e) { if (run && run.id === id) { clear(ui.tl).appendChild(el('div', 'err', e.message)); setBusy(false); } });
  }
  function renderTimeline() {
    var box = clear(ui.tl); box.classList.remove('muted');
    if (!run) { box.classList.add('muted'); box.textContent = 'No run selected.'; ui.cancel.disabled = true; return; }
    ui.cancel.disabled = !isActive(run);
    var head = el('div', 'fl-runhead');
    head.appendChild(chip(run.status || '?'));
    head.appendChild(el('strong', '', run.mode || ''));
    head.appendChild(el('span', 'muted', dur(run)));
    if (run.healed && run.healed.length) head.appendChild(chip('healed ' + run.healed.length, 'warn'));
    var al = el('a', '', 'agent log'); al.href = artifact(run.id, 'agent.log'); al.target = '_blank'; al.rel = 'noopener'; head.appendChild(al);
    box.appendChild(head);
    box.appendChild(el('div', 'meta2', run.id + (run.deviceId ? ' / ' + run.deviceId : '') + (run.startedAt ? ' / ' + fmtTime(run.startedAt) : '')));
    if (run.error) box.appendChild(el('div', 'err', run.error));
    (run.steps || []).forEach(function (s) {
      var d = el('div', 'fl-tstep');
      var top = el('div', 'fl-steptop');
      top.appendChild(el('code', '', s.id)); top.appendChild(chip(s.state || '?'));
      top.appendChild(el('strong', 'n', s.title || ''));
      if (run.healed && run.healed.indexOf(s.id) >= 0) top.appendChild(chip('healed', 'warn'));
      top.appendChild(el('span', 'muted fl-dur', dur(s)));
      d.appendChild(top);
      (s.substeps || []).forEach(function (ss) { d.appendChild(el('div', 'fl-sub', '• ' + (ss.text || ''))); });
      (s.asserts || []).forEach(function (a) {
        var r = el('div', 'fl-assert');
        r.appendChild(chip(a.passed ? 'pass' : 'fail', a.passed ? 'ok' : 'failed'));
        r.appendChild(document.createTextNode(' ' + (a.text || '') + (a.detail ? ' (' + a.detail + ')' : '')));
        d.appendChild(r);
      });
      if (s.notes) d.appendChild(el('div', 'muted fl-sub', s.notes));
      if (s.error) d.appendChild(el('div', 'err', s.error));
      if (s.screenshot) {
        var src = artifact(run.id, String(s.screenshot).replace(/^.*\//, ''));
        var im = el('img', 'thumb'); im.src = src; im.alt = 'step ' + s.id; im.loading = 'lazy'; im.style.marginLeft = '0';
        (function (sid) { im.addEventListener('click', function () { enlarge(src, 'Step ' + sid); }); })(s.id);
        d.appendChild(im);
      }
      box.appendChild(d);
    });
    if (compare && compare.id !== run.id) {
      box.appendChild(btn('Compare screenshots with ' + compare.id.slice(0, 8), 'sm', function () { compareRuns(compare, run); }));
    }
  }

  /* ---------- history & screenshot compare ---------- */
  function loadRuns() {
    if (!cur) { runs = []; renderRuns(); return Promise.resolve(); }
    var id = cur.project + '/' + cur.name;
    return req('GET', '/flows/runs?limit=30&case=' + encodeURIComponent(id)).then(function (l) { runs = l || []; renderRuns(); })
      .catch(function (e) { runs = []; clear(ui.runs).appendChild(el('li', 'muted', e.message)); });
  }
  function renderRuns() {
    clear(ui.runs);
    if (!runs.length) ui.runs.appendChild(el('li', 'muted', cur ? 'No runs yet.' : 'Select a flow.'));
    runs.forEach(function (r) {
      var li = el('li', run && run.id === r.id ? 'sel' : '');
      li.appendChild(chip(r.status || '?')); li.appendChild(el('span', 'n', (r.mode || '') + ' ' + fmtTime(r.startedAt)));
      li.appendChild(el('span', 'muted', dur(r)));
      var cb = el('button', 'btn sm x', compare && compare.id === r.id ? '▣' : '□'); cb.type = 'button'; cb.title = 'Mark for screenshot compare';
      cb.addEventListener('click', function (ev) {
        ev.stopPropagation();
        if (compare && compare.id === r.id) compare = null;
        else if (compare && run && run.id !== compare.id) { compareRuns(compare, r); return; }
        else compare = r;
        renderRuns(); renderTimeline();
      });
      li.appendChild(cb);
      li.addEventListener('click', function () { openRun(r.id); renderRuns(); });
      ui.runs.appendChild(li);
    });
  }
  function compareRuns(a, b) {
    // a, b may be summaries; fetch full runs for step screenshots
    Promise.all([req('GET', '/flows/runs/' + encodeURIComponent(a.id)), req('GET', '/flows/runs/' + encodeURIComponent(b.id))]).then(function (rs) {
      var A = rs[0], B = rs[1], m = PF.modal.open('Compare runs', { wide: true });
      var byId = {}; (B.steps || []).forEach(function (s) { byId[s.id] = s; });
      var any = false;
      (A.steps || []).forEach(function (sa) {
        var sb = byId[sa.id];
        if (!sa.screenshot || !sb || !sb.screenshot) return;
        any = true;
        m.body.appendChild(el('div', 'fl-sh', 'Step ' + sa.id + (sa.title ? ' - ' + sa.title : '')));
        var row = el('div', 'fl-cmp');
        [[A, sa], [B, sb]].forEach(function (p) {
          var c = el('div'); c.appendChild(el('div', 'muted', p[0].id.slice(0, 8) + ' ' + (p[0].status || '')));
          var im = el('img', 'runimg'); im.src = artifact(p[0].id, String(p[1].screenshot).replace(/^.*\//, '')); im.alt = 'step ' + sa.id;
          c.appendChild(im); row.appendChild(c);
        });
        m.body.appendChild(row);
      });
      if (!any) m.body.appendChild(el('p', 'muted', 'No step has a screenshot in both runs.'));
      compare = null; renderRuns(); renderTimeline();
    }).catch(fail);
  }

  /* ---------- cache viewer ---------- */
  function loadCache(decorate) {
    var c = cur; if (!c) return Promise.resolve();
    clear(ui.cache).appendChild(el('div', 'muted tpad', 'Loading...'));
    return Promise.all([req('GET', casePath(c, '/cache')), req('GET', casePath(c, '/cache/history')).catch(function () { return []; })]).then(function (rs) {
      if (cur !== c) return;
      cacheState = rs[0] || {}; histEntries = rs[1] || []; histSel = null;
      c.staleness = cacheState.staleness || {};
      if (decorate) decorate(cacheState);
      renderCache();
    }).catch(function (e) { clear(ui.cache).appendChild(el('div', 'err tpad', e.message)); });
  }
  function pretty(o) { try { return JSON.stringify(o, null, 2); } catch (e) { return String(o); } }
  function renderCache() {
    var box = clear(ui.cache), c = cur;
    var cache = cacheState && cacheState.cache, st = (cacheState && cacheState.staleness) || {};
    var top = el('div', 'tpad btn-row');
    top.appendChild(btn('Delete cache', 'sm danger', function () {
      PF.confirm('Delete the cache of this flow?', function () {
        req('DELETE', casePath(c, '/cache')).then(function () { loadCache(); loadCases(); }).catch(fail);
      }, { title: 'Delete cache', okLabel: 'Delete', danger: true });
    }));
    top.appendChild(btn('↻', 'sm', loadCache, 'Reload'));
    box.appendChild(top);
    if (!cache) box.appendChild(el('div', 'muted tpad', 'No cache yet. Run with agent to record one.'));
    else {
      box.appendChild(el('div', 'meta2 tpad', 'v' + cache.version + ' / ' + (cache.platform || 'any') + ' / updated ' + fmtTime(cache.updatedAt)));
      (cache.segments || []).forEach(function (sg) {
        var d = el('details', 'fl-seg');
        var sm = el('summary'); sm.appendChild(el('code', '', sg.id)); sm.appendChild(chip(st[sg.id] || '?'));
        sm.appendChild(el('span', 'n', ' ' + (sg.title || '')));
        if (sg.healedAt) sm.appendChild(chip('healed', 'warn'));
        d.appendChild(sm);
        d.appendChild(el('div', 'meta2', 'recorded ' + fmtTime(sg.recordedAt) + (sg.healedAt ? ' / healed ' + fmtTime(sg.healedAt) : '')));
        d.appendChild(el('pre', 'fl-pre', pretty(sg.steps)));
        box.appendChild(d);
      });
    }
    box.appendChild(section('History'));
    var ul = el('ul', 'tlist');
    if (!histEntries.length) ul.appendChild(el('li', 'muted', 'No archived versions.'));
    histEntries.forEach(function (h) {
      var li = el('li', histSel === h.id ? 'sel' : '');
      li.appendChild(el('span', 'n', 'v' + h.version + ' / ' + fmtTime(h.archivedAt)));
      li.appendChild(btn('Restore', 'sm', function (ev) {
        ev.stopPropagation();
        PF.confirm('Restore this cache version? The current one is archived.', function () {
          var before = cacheState && cacheState.cache ? pretty(cacheState.cache) : '';
          req('POST', casePath(c, '/cache/history/' + encodeURIComponent(h.id) + '/restore')).then(function (restored) {
            PF.toast('Restored', true);
            loadCases();
            return loadCache(function (st) { if (before) st.diff = { before: before, after: pretty(restored && restored.cache ? restored.cache : st.cache) }; });
          }).catch(fail);
        }, { title: 'Restore cache', okLabel: 'Restore' });
      }));
      li.addEventListener('click', function () { histSel = histSel === h.id ? null : h.id; renderCache(); });
      ul.appendChild(li);
    });
    box.appendChild(ul);
    if (cacheState && cacheState.diff) {
      box.appendChild(section('Diff: before restore vs restored (current)'));
      var out = el('div', 'tpad'); out.appendChild(renderDiff(cacheState.diff.before, cacheState.diff.after)); box.appendChild(out);
    } else if (histEntries.length) {
      box.appendChild(el('div', 'muted tpad', 'The history API lists versions without content; restore one to see a diff against the previous current version.'));
    }
  }

  /* tiny LCS line diff: returns a node */
  function lineDiff(a, b) {
    var A = a.split('\n'), B = b.split('\n'), n = A.length, m = B.length, i, j;
    if (n * m > 4000000) return null;
    var L = [];
    for (i = 0; i <= n; i++) { L.push(new Array(m + 1).fill(0)); }
    for (i = n - 1; i >= 0; i--) for (j = m - 1; j >= 0; j--) L[i][j] = A[i] === B[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1]);
    var out = []; i = 0; j = 0;
    while (i < n && j < m) {
      if (A[i] === B[j]) { out.push([' ', A[i]]); i++; j++; }
      else if (L[i + 1][j] >= L[i][j + 1]) { out.push(['-', A[i]]); i++; }
      else { out.push(['+', B[j]]); j++; }
    }
    while (i < n) out.push(['-', A[i++]]);
    while (j < m) out.push(['+', B[j++]]);
    return out;
  }
  function renderDiff(a, b) {
    var d = lineDiff(a, b), pre = el('div', 'fl-pre fl-diff');
    if (!d) { pre.textContent = 'Too large to diff.'; return pre; }
    d.forEach(function (l) {
      pre.appendChild(el('div', l[0] === '+' ? 'add' : l[0] === '-' ? 'del' : '', l[0] + ' ' + l[1]));
    });
    return pre;
  }

  /* ---------- lifecycle ---------- */
  PF.flowsDiff = lineDiff;
  PF.loadFlows = function () {
    if (!inited) { inited = true; build(); }
    renderDevices();
    if (run && isActive(run) && !es) openRun(run.id);
    return loadProjects();
  };
  PF.leaveFlows = function () { closeStream(); };
  window.addEventListener('beforeunload', closeStream);
})();
