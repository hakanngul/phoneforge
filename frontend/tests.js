/*
 * Testing workspace: projects, scripts, editor with validation gutter, run + results + history.
 * API base /api/v1/tests. All server text is inserted with textContent.
 *
 * Validation gutter: the textarea (wrap=off, fixed 18px line-height) sits next to a gutter column of one
 * <div> per line. The gutter scrolls with the textarea (scrollTop mirrored). Edits are debounced 600 ms,
 * then POST /validate?kind=mob|json (raw body) returns {ok, errors:[{line,message}]}; lines in errors get
 * class "bad" (red) with the message as title, and the errors are listed below the editor; clicking one
 * selects that line and scrolls it into view. Stale responses are dropped via a sequence counter.
 */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var LH = 18;
  var NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
  var EXAMPLE = 'app "com.android.settings"\nwait_for ~"Wi" timeout:10000\nassert_exists ~"Wi"\n';
  var JSON_TEMPLATE = '{\n  "version": "0.2",\n  "steps": []\n}\n';
  var MOB_TEMPLATE = '# new script\n';

  var projects = [], pid = null, scripts = [], runs = [];
  var cur = null;        // {name, kind, saved}
  var shownRunId = null, loaded = false, valSeq = 0, valTimer = null, lastErrLines = {}, gutCount = -1;
  try { pid = localStorage.getItem('pf.tproj') || null; } catch (e) { /* ignore */ }

  /* ---------- http ---------- */
  function req(method, path, body, contentType) {
    var opts = { method: method, headers: {} };
    if (body !== undefined) { opts.body = body; opts.headers['Content-Type'] = contentType || 'text/plain'; }
    return fetch(PF.API + '/tests' + path, opts).then(function (res) {
      return res.text().then(function (t) {
        var j = null; try { j = t ? JSON.parse(t) : null; } catch (e) { /* raw text */ }
        if (!res.ok) {
          var e = new Error((j && (j.error || j.message)) || ('HTTP ' + res.status));
          e.status = res.status; e.code = j && j.code; e.errors = j && j.errors;
          throw e;
        }
        return { json: j, text: t };
      });
    }, function (err) { throw new Error('Network error: ' + err.message); });
  }
  function jreq(method, path, body, ct) { return req(method, path, body, ct).then(function (r) { return r.json; }); }
  function notWired(e) { return e && (e.status === 501 || (e.status === 404 && !e.code)); }
  function errText(e) { return notWired(e) ? 'Test workspace API not available on this server yet (' + e.status + ').' : e.message; }
  function fail(e) { PF.toast(errText(e)); }

  function confirmModal(title, text, okLabel, onOk, danger) {
    var m = PF.modal.open(title);
    m.body.appendChild(PF.el('p', 'confirm-text', text));
    var row = PF.el('div', 'btn-row');
    row.appendChild(PF.btn(okLabel, danger ? 'danger' : 'primary', function () { m.close(); onOk(); }));
    row.appendChild(PF.btn('Cancel', '', function () { m.close(); }));
    m.body.appendChild(row);
  }

  function chipClass(s) {
    s = String(s || '').toLowerCase();
    if (s === 'ok' || s === 'passed' || s === 'success' || s === 'pass') return 'ok';
    if (s === 'failed' || s === 'error' || s === 'fail') return 'failed';
    if (s.indexOf('needs') === 0 || s === 'warning' || s === 'warn') return 'warn';
    return 'skipped';
  }
  function fmtMs(ms) { return ms == null ? '' : ms >= 1000 ? (ms / 1000).toFixed(1) + ' s' : Math.round(ms) + ' ms'; }
  function fmtTime(t) { var d = new Date(t); return isNaN(d) ? String(t || '') : d.toLocaleString(); }
  function kindOf(name) { return /\.json$/i.test(name) ? 'json' : 'mob'; }
  function dirty() { return !!cur && $('edText').value !== cur.saved; }

  /* ---------- projects ---------- */
  function loadProjects() {
    return jreq('GET', '/projects').then(function (list) {
      projects = list || [];
      loaded = true;
      if (pid && !projects.some(function (p) { return p.id === pid; })) pid = null;
      if (!pid && projects.length) pid = projects[0].id;
      renderProjects();
      return selectProject(pid, true);
    }).catch(function (e) {
      projects = []; renderProjects(true, errText(e));
    });
  }

  function renderProjects(isErr, msg) {
    var ul = $('tpList'); ul.innerHTML = '';
    if (!projects.length) { ul.appendChild(PF.el('li', 'muted', msg || 'No projects yet. Create one with + New.')); }
    projects.forEach(function (p) {
      var li = PF.el('li', p.id === pid ? 'sel' : '');
      li.title = p.path || p.name;
      li.appendChild(PF.el('span', 'n', p.name));
      var x = PF.el('button', 'btn sm x', '✕'); x.title = 'Delete project'; x.type = 'button';
      x.addEventListener('click', function (ev) {
        ev.stopPropagation();
        confirmModal('Delete project?', 'Delete project "' + p.name + '" with all its scripts? This cannot be undone.', 'Delete', function () {
          jreq('DELETE', '/projects/' + encodeURIComponent(p.id)).then(function () {
            if (pid === p.id) { pid = null; closeScript(); }
            return loadProjects();
          }).catch(fail);
        }, true);
      });
      li.appendChild(x);
      li.addEventListener('click', function () { guardDirty(function () { selectProject(p.id); }); });
      ul.appendChild(li);
    });
    $('tsNew').disabled = !pid;
  }

  function selectProject(id, keepEditor) {
    pid = id || null;
    try { if (pid) localStorage.setItem('pf.tproj', pid); } catch (e) { /* ignore */ }
    if (!keepEditor) closeScript();
    renderProjects();
    scripts = []; runs = [];
    renderScripts(); renderRuns();
    if (!pid) return Promise.resolve();
    return Promise.all([loadScripts(), loadRuns()]);
  }

  $('tpNew').addEventListener('click', function () {
    var f = $('tpForm'); f.hidden = !f.hidden; if (f.hidden) return;
    f.innerHTML = '';
    var inp = PF.input('text', 'Project name', 'grow');
    var go = PF.btn('Create', 'sm primary', create);
    function create() {
      var n = inp.value.trim(); if (!n) return;
      go.disabled = true;
      guardDirty(function () {
        jreq('POST', '/projects', JSON.stringify({ name: n }), 'application/json').then(function (p) {
          f.hidden = true; closeScript(); if (p && p.id) pid = p.id;
          return loadProjects();
        }).catch(fail).then(function () { go.disabled = false; });
      }, function () { go.disabled = false; });
    }
    inp.addEventListener('keydown', function (e) { if (e.key === 'Enter') create(); });
    f.appendChild(inp); f.appendChild(go); inp.focus();
  });

  /* ---------- scripts ---------- */
  function sp(name) { return '/projects/' + encodeURIComponent(pid) + '/scripts/' + encodeURIComponent(name); }

  function loadScripts() {
    var id = pid;
    return jreq('GET', '/projects/' + encodeURIComponent(id) + '/scripts').then(function (l) {
      if (id !== pid) return;
      scripts = l || []; renderScripts();
    }).catch(function (e) { if (id === pid) renderScripts(errText(e)); });
  }

  function renderScripts(msg) {
    var ul = $('tsList'); ul.innerHTML = '';
    if (!pid) { ul.appendChild(PF.el('li', 'muted', 'Select a project.')); return; }
    if (!scripts.length) ul.appendChild(PF.el('li', 'muted', msg || 'No scripts yet.'));
    scripts.forEach(function (s) {
      var li = PF.el('li', cur && cur.name === s.name ? 'sel' : '');
      li.title = (s.size != null ? s.size + ' bytes' : '') + (s.modifiedAt ? ', modified ' + fmtTime(s.modifiedAt) : '');
      li.appendChild(PF.el('span', 'n', s.name));
      li.appendChild(PF.pill(s.kind || kindOf(s.name)));
      var x = PF.el('button', 'btn sm x', '✕'); x.title = 'Delete script'; x.type = 'button';
      x.addEventListener('click', function (ev) {
        ev.stopPropagation();
        confirmModal('Delete script?', 'Delete "' + s.name + '"? This cannot be undone.', 'Delete', function () {
          jreq('DELETE', sp(s.name)).then(function () {
            if (cur && cur.name === s.name) closeScript();
            return loadScripts();
          }).catch(fail);
        }, true);
      });
      li.appendChild(x);
      li.addEventListener('click', function () { guardDirty(function () { openScript(s.name, s.kind); }); });
      ul.appendChild(li);
    });
  }

  $('tsNew').addEventListener('click', function () {
    var f = $('tsForm'); f.hidden = !f.hidden; if (f.hidden) return;
    f.innerHTML = '';
    var inp = PF.input('text', 'name', 'grow');
    var sel = document.createElement('select');
    [['mob', '.mob'], ['json', '.json']].forEach(function (k) { var o = PF.el('option', '', k[1]); o.value = k[0]; sel.appendChild(o); });
    var go = PF.btn('Create', 'sm primary', create);
    var hint = PF.el('div', 'fhint'); hint.style.width = '100%';
    function create() {
      var kind = sel.value, n = inp.value.trim();
      if (!n) return;
      if (!/\.(mob|json)$/i.test(n)) n += '.' + kind; else kind = kindOf(n);
      if (!NAME_RE.test(n)) { hint.textContent = 'Name: letters, digits, . _ - only; must start with a letter or digit.'; return; }
      if (scripts.some(function (s) { return s.name === n; })) { hint.textContent = 'A script with that name exists.'; return; }
      go.disabled = true;
      guardDirty(function () {
        jreq('PUT', sp(n), kind === 'json' ? JSON_TEMPLATE : MOB_TEMPLATE).then(function () {
          f.hidden = true;
          return loadScripts().then(function () { return openScript(n, kind); });
        }).catch(function (e) { hint.textContent = errText(e); }).then(function () { go.disabled = false; });
      }, function () { go.disabled = false; });
    }
    inp.addEventListener('keydown', function (e) { if (e.key === 'Enter') create(); });
    f.appendChild(inp); f.appendChild(sel); f.appendChild(go); f.appendChild(hint); inp.focus();
  });

  /* ---------- editor ---------- */
  var ta = $('edText'), gut = $('edGut');

  function guardDirty(go, cancel) {
    if (!dirty()) { go(); return; }
    var m = PF.modal.open('Unsaved changes', { onClose: function () { if (!chosen && cancel) cancel(); } }), chosen = false;
    m.body.appendChild(PF.el('p', 'confirm-text', '"' + cur.name + '" has unsaved changes.'));
    var row = PF.el('div', 'btn-row');
    row.appendChild(PF.btn('Discard changes', 'danger', function () { chosen = true; m.close(); go(); }));
    row.appendChild(PF.btn('Keep editing', '', function () { m.close(); }));
    m.body.appendChild(row);
  }
  window.addEventListener('beforeunload', function (e) { if (dirty()) { e.preventDefault(); e.returnValue = ''; } });

  function openScript(name, kind) {
    return req('GET', sp(name)).then(function (r) {
      cur = { name: name, kind: kind || kindOf(name), saved: r.text };
      ta.value = r.text;
      $('edWrap').hidden = false; $('edEmpty').hidden = true;
      ta.scrollTop = 0; lastErrLines = {}; gutCount = -1;
      renderGutter(); renderErrs(null);
      updateEditorState(); renderScripts();
      scheduleValidate(0);
    }).catch(fail);
  }

  function closeScript() {
    cur = null; ta.value = '';
    $('edWrap').hidden = true; $('edEmpty').hidden = false; $('edErrs').hidden = true;
    clearTimeout(valTimer); valSeq++;
    updateEditorState();
  }

  function updateEditorState() {
    $('edTitle').textContent = cur ? cur.name : 'Editor';
    $('edDirty').hidden = !dirty();
    $('edSave').disabled = !cur || !dirty();
    $('edExample').disabled = !cur || cur.kind !== 'mob';
    var d = PF.selectedDevice(), canRun = !!cur && !!d && d.state === 'connected' && !running;
    $('edRun').disabled = !canRun;
    $('edHint').textContent = !cur ? '' : !PF.selectedId ? 'Select a device in the Automation tab to run this script.'
      : (!d || d.state !== 'connected') ? 'The selected device is not connected.' : '';
    $('edRun').title = $('edHint').textContent || 'Run on the selected device';
  }
  PF.hooks.onSelect.push(updateEditorState);
  PF.hooks.onDevices.push(updateEditorState);

  function renderGutter() {
    var n = ta.value.split('\n').length;
    if (n !== gutCount) {
      gut.innerHTML = '';
      for (var i = 1; i <= n; i++) gut.appendChild(PF.el('div', '', String(i)));
      gutCount = n;
    }
    Array.prototype.forEach.call(gut.children, function (c, i) {
      var msg = lastErrLines[i + 1];
      c.className = msg ? 'bad' : '';
      if (msg) c.title = msg; else c.removeAttribute('title');
    });
    gut.scrollTop = ta.scrollTop;
  }

  function renderErrs(res, note) {
    var box = $('edErrs'); box.innerHTML = '';
    if (note) { box.hidden = false; box.appendChild(PF.el('div', 'mut', note)); return; }
    if (!res) { box.hidden = true; return; }
    box.hidden = false;
    if (res.ok || !(res.errors || []).length) { box.appendChild(PF.el('div', 'okline', 'No problems found.')); return; }
    res.errors.forEach(function (er) {
      var row = PF.el('div', 'e');
      if (er.line > 0) row.appendChild(PF.el('b', '', 'Line ' + er.line));
      row.appendChild(document.createTextNode(er.message || ''));
      if (er.line > 0) row.addEventListener('click', function () { jumpTo(er.line); });
      box.appendChild(row);
    });
  }

  function jumpTo(line) {
    var lines = ta.value.split('\n'), start = 0, i;
    line = Math.min(Math.max(line, 1), lines.length);
    for (i = 0; i < line - 1; i++) start += lines[i].length + 1;
    ta.focus();
    ta.setSelectionRange(start, start + lines[line - 1].length);
    ta.scrollTop = Math.max((line - 1) * LH - ta.clientHeight / 2, 0);
    gut.scrollTop = ta.scrollTop;
  }

  function scheduleValidate(ms) {
    clearTimeout(valTimer);
    if (!cur) return;
    valTimer = setTimeout(validate, ms == null ? 600 : ms);
  }

  function validate() {
    if (!cur) return;
    var seq = ++valSeq, body = ta.value, kind = cur.kind;
    req('POST', '/validate?kind=' + encodeURIComponent(kind), body).then(function (r) {
      if (seq !== valSeq) return;
      var res = r.json || { ok: true, errors: [] };
      lastErrLines = {};
      (res.errors || []).forEach(function (er) { if (er.line > 0 && !lastErrLines[er.line]) lastErrLines[er.line] = er.message || 'error'; });
      renderGutter(); renderErrs(res);
    }).catch(function (e) {
      if (seq !== valSeq) return;
      lastErrLines = {}; renderGutter();
      if (e.errors && e.errors.length) {
        e.errors.forEach(function (er) { if (er.line > 0) lastErrLines[er.line] = er.message; });
        renderGutter(); renderErrs({ ok: false, errors: e.errors });
      } else renderErrs(null, 'Validation unavailable: ' + errText(e));
    });
  }

  ta.addEventListener('input', function () { renderGutter(); updateEditorState(); scheduleValidate(); });
  ta.addEventListener('scroll', function () { gut.scrollTop = ta.scrollTop; });
  ta.addEventListener('keydown', function (e) {
    if (e.key === 'Tab' && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      ta.setRangeText('  ', ta.selectionStart, ta.selectionEnd, 'end');
      ta.dispatchEvent(new Event('input'));
    } else if ((e.metaKey || e.ctrlKey) && (e.key === 's' || e.key === 'S')) {
      e.preventDefault(); save();
    }
  });
  document.addEventListener('keydown', function (e) {
    if ((e.metaKey || e.ctrlKey) && (e.key === 's' || e.key === 'S') && !$('testing-tab').hidden && !$('wsView').hidden && e.target !== ta) {
      if (cur) { e.preventDefault(); save(); }
    }
  });

  function save() {
    if (!cur || !dirty()) return Promise.resolve(true);
    var text = ta.value, name = cur.name;
    $('edSave').disabled = true;
    return jreq('PUT', sp(name), text).then(function () {
      if (cur && cur.name === name) cur.saved = text;
      updateEditorState(); PF.toast('Saved ' + name, true);
      loadScripts();
      return true;
    }).catch(function (e) {
      if (e.errors && e.errors.length) {
        lastErrLines = {}; e.errors.forEach(function (er) { if (er.line > 0) lastErrLines[er.line] = er.message; });
        renderGutter(); renderErrs({ ok: false, errors: e.errors });
      }
      fail(e); updateEditorState(); return false;
    });
  }
  $('edSave').addEventListener('click', save);

  $('edExample').addEventListener('click', function () {
    if (!cur || cur.kind !== 'mob') return;
    var v = ta.value, pos = ta.selectionStart, pre = v.slice(0, pos);
    var ins = (pre && !/\n$/.test(pre) ? '\n' : '') + EXAMPLE;
    ta.setRangeText(ins, ta.selectionStart, ta.selectionEnd, 'end');
    ta.focus();
    ta.dispatchEvent(new Event('input'));
  });

  /* ---------- run ---------- */
  var running = false;
  $('edRun').addEventListener('click', function () {
    if (!cur || !PF.needDevice()) return;
    var name = cur.name, dev = PF.selectedId, projectId = pid;
    running = true; updateEditorState();
    var box = $('resBox'); box.className = 'results tpad muted'; box.textContent = 'Running ' + name + '...';
    $('resTitle').textContent = 'Results';
    save().then(function (ok) {
      if (!ok) throw new Error('Save failed; not running.');
      return jreq('POST', '/projects/' + encodeURIComponent(projectId) + '/scripts/' + encodeURIComponent(name) + '/run', JSON.stringify({ deviceId: dev }), 'application/json');
    }).then(function (rec) {
      shownRunId = rec && rec.id;
      renderRun(rec, 'Latest run');
      return loadRuns();
    }).catch(function (e) {
      box.className = 'results tpad'; box.innerHTML = '';
      box.appendChild(PF.el('div', 'err', errText(e)));
      if (e.errors) e.errors.forEach(function (er) { box.appendChild(PF.el('div', 'err', (er.line ? 'Line ' + er.line + ': ' : '') + er.message)); });
    }).then(function () { running = false; updateEditorState(); });
  });

  /* ---------- results ---------- */
  function showShot(b64, label) {
    var m = PF.modal.open(label || 'Screenshot', { wide: true });
    var im = PF.el('img', 'runimg'); im.alt = 'screenshot';
    im.src = 'data:image/png;base64,' + b64;
    m.body.appendChild(im);
  }

  function renderRun(rec, title) {
    var box = $('resBox');
    box.className = 'results tpad'; box.innerHTML = '';
    $('resTitle').textContent = title || 'Results';
    if (!rec) { box.className = 'results tpad muted'; box.textContent = 'No run yet.'; return; }
    var r = rec.result || {};
    var status = r.status || (rec.passed ? 'passed' : 'failed');
    var head = PF.el('div', 'sum');
    head.appendChild(PF.el('span', 'chip ' + chipClass(status), status));
    head.appendChild(document.createTextNode(' ' + (rec.script || '')));
    box.appendChild(head);
    box.appendChild(PF.el('div', 'meta2', [rec.deviceId, rec.startedAt ? fmtTime(rec.startedAt) : '', fmtMs(rec.durationMs != null ? rec.durationMs : r.totalMs)].filter(Boolean).join(' · ')));
    (r.steps || []).forEach(function (s) {
      var row = PF.el('div', 'step');
      row.appendChild(PF.el('span', 'muted', '#' + s.index));
      row.appendChild(PF.el('span', 'chip ' + chipClass(s.status), s.status || '?'));
      row.appendChild(PF.el('strong', '', s.action));
      if (s.matched) row.appendChild(PF.el('span', 'muted', 'matched [' + [s.matched.x, s.matched.y, s.matched.w, s.matched.h].join(',') + ']'));
      if (s.durationMs != null) row.appendChild(PF.el('span', 'dur', fmtMs(s.durationMs)));
      if (s.error) row.appendChild(PF.el('div', 'err', (s.code ? s.code + ': ' : '') + s.error));
      (s.warnings || []).forEach(function (w) { row.appendChild(PF.el('div', 'warnl', String(w))); });
      if (s.screenshot && /^[A-Za-z0-9+\/=\s]+$/.test(s.screenshot)) {
        var im = PF.el('img', 'thumb'); im.alt = 'step ' + s.index + ' screenshot'; im.title = 'Click to enlarge';
        im.src = 'data:image/png;base64,' + s.screenshot.replace(/\s+/g, '');
        im.addEventListener('click', function () { showShot(s.screenshot.replace(/\s+/g, ''), 'Step #' + s.index + ' ' + (s.action || '')); });
        row.appendChild(im);
      }
      box.appendChild(row);
    });
    if (!(r.steps || []).length) box.appendChild(PF.el('div', 'muted', 'No step details.'));
    markRuns();
  }

  /* ---------- history ---------- */
  function loadRuns() {
    var id = pid;
    if (!id) return Promise.resolve();
    return jreq('GET', '/runs?projectId=' + encodeURIComponent(id) + '&limit=30').then(function (l) {
      if (id !== pid) return;
      runs = l || []; renderRuns();
    }).catch(function (e) { if (id === pid) { runs = []; renderRuns(errText(e)); } });
  }

  function renderRuns(msg) {
    var ul = $('runList'); ul.innerHTML = ''; ul.className = 'tlist runs';
    if (!runs.length) { ul.appendChild(PF.el('li', 'muted', msg || 'No runs yet.')); return; }
    runs.forEach(function (r) {
      var res = r.result || {}, status = res.status || (r.passed ? 'passed' : 'failed');
      var li = PF.el('li', r.id === shownRunId ? 'sel' : ''); li.setAttribute('data-id', r.id);
      li.appendChild(PF.el('span', 'chip ' + chipClass(status), status));
      li.appendChild(PF.el('span', 'n', r.script || ''));
      li.appendChild(PF.el('span', 'muted', fmtMs(r.durationMs)));
      var t = PF.el('div', 'muted', fmtTime(r.startedAt)); t.style.width = '100%'; t.style.fontSize = '11px';
      li.appendChild(t);
      li.addEventListener('click', function () {
        shownRunId = r.id; markRuns();
        jreq('GET', '/runs/' + encodeURIComponent(r.id)).then(function (full) { if (shownRunId === r.id) renderRun(full, 'Run ' + String(r.id).slice(0, 8)); }).catch(fail);
      });
      ul.appendChild(li);
    });
  }
  function markRuns() {
    Array.prototype.forEach.call($('runList').children, function (li) { li.classList.toggle('sel', li.getAttribute('data-id') === shownRunId); });
  }
  $('runsReload').addEventListener('click', function () { loadRuns(); });

  /* ---------- entry ---------- */
  PF.loadTests = function () {
    if (!loaded) loadProjects(); else { loadScripts(); loadRuns(); }
    updateEditorState();
  };
})();
