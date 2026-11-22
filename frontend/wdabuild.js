/*
 * In-app WebDriverAgent build. PhoneForge builds WDA on the user's own Mac (it must be signed with
 * the user's Apple team), so everything happens here instead of a terminal script.
 *
 *   PF.wdaBuild.open({mode: 'device'|'sim', onSuccess(status), onClose()})
 *       opens the progress modal, starts (or attaches to) the build, polls
 *       GET /ios/wda/build?since=N once a second and renders phase + live log lines.
 *       Pre-check problems come back as structured errors {code, error, hint, lines}:
 *       TEAM_NOT_FOUND / TEAM_INVALID -> asks for the 10-char Team ID and retries;
 *       XCODE_* and everything else -> message + what to do, with Retry.
 *   PF.wdaBuild.showIssue(title, issue, {retry, retryLabel}) renders any structured issue
 *       (install/launch/readiness errors use the same {code, message, hint, lines} shape).
 *   PF.wdaBuild.env(quick) -> Promise of GET /ios/wda env.
 */
(function () {
  'use strict';
  var PF = window.PF;
  var PHASES = [['checking', 'Checking'], ['extracting', 'Preparing source'], ['building', 'Building'], ['done', 'Done']];

  function envOf(quick) {
    return PF.api('GET', '/ios/wda' + (quick ? '?quick=1' : '')).then(function (r) { return (r && r.env) || {}; }, function () { return null; });
  }

  function issueFromErr(e) {
    return { code: e.code || '', message: e.serverMessage || e.message, hint: e.hint || '', lines: e.lines || [] };
  }

  function renderIssue(host, is) {
    host.appendChild(PF.note(is.message || 'Something went wrong.', 'bad'));
    if (is.hint) {
      var h = PF.el('div', 'muted', is.hint);
      h.style.cssText = 'white-space:pre-wrap;margin:6px 0';
      host.appendChild(h);
    }
    if (is.lines && is.lines.length) {
      var pre = PF.el('pre', 'muted', is.lines.join('\n'));
      pre.style.cssText = 'max-height:160px;overflow:auto;white-space:pre-wrap;font-size:11px;margin:6px 0';
      host.appendChild(pre);
    }
    if (is.code) host.appendChild(PF.el('div', 'muted', 'Code: ' + is.code));
  }

  function showIssue(title, is, opts) {
    opts = opts || {};
    var m = PF.modal.open(title, { onClose: opts.onClose });
    renderIssue(m.body, is);
    var row = PF.el('div', 'btn-row');
    if (opts.retry) row.appendChild(PF.btn(opts.retryLabel || 'Try again', 'primary', function () { m.close(); opts.retry(); }));
    row.appendChild(PF.btn('Close', '', function () { m.close(); }));
    m.body.appendChild(row);
  }

  function open(opts) {
    opts = opts || {};
    var mode = opts.mode === 'sim' ? 'sim' : 'device';
    var closed = false, finished = false, timer = null, next = 0, started = Date.now();
    var m = PF.modal.open(mode === 'sim' ? 'Build WDA for the simulator' : 'Build WebDriverAgent', {
      wide: true,
      onClose: function () { closed = true; clearTimeout(timer); if (!finished && opts.onClose) opts.onClose(); }
    });
    var body = m.body;

    function clear() { body.innerHTML = ''; }
    function closeBtnRow(extra) {
      var row = PF.el('div', 'btn-row');
      (extra || []).forEach(function (b) { row.appendChild(b); });
      row.appendChild(PF.btn('Close', '', function () { m.close(); }));
      return row;
    }

    /* ----- Team ID prompt ----- */
    function askTeam(is) {
      clear();
      renderIssue(body, is);
      var inp = PF.input('text', 'ABCDE12345');
      inp.maxLength = 10; inp.style.textTransform = 'uppercase';
      body.appendChild(PF.row('Apple Team ID', inp, '10 letters/digits. Xcode > Settings > Accounts > your team. It is saved in PhoneForge settings.'));
      var err = PF.el('div', 'muted'); body.appendChild(err);
      var go = PF.btn('Save and build', 'primary', function () {
        var v = inp.value.trim().toUpperCase();
        if (!/^[A-Z0-9]{10}$/.test(v)) { err.textContent = 'A Team ID is exactly 10 letters or digits.'; return; }
        start(v);
      });
      inp.addEventListener('keydown', function (e) { if (e.key === 'Enter') go.click(); });
      body.appendChild(closeBtnRow([go]));
      inp.focus();
    }

    /* ----- start ----- */
    function start(teamId) {
      clear();
      body.appendChild(PF.el('p', 'muted', 'Starting...'));
      var payload = { mode: mode };
      if (teamId) payload.teamId = teamId;
      PF.api('POST', '/ios/wda/build', payload).then(function (st) {
        next = 0; started = Date.now();
        progress(st); poll();
      }, function (e) {
        if (closed) return;
        if (e.code === 'BUILD_BUSY') { next = 0; poll(true); return; }
        var is = issueFromErr(e);
        if (e.code === 'TEAM_NOT_FOUND' || e.code === 'TEAM_INVALID') { askTeam(is); return; }
        clear();
        renderIssue(body, is);
        body.appendChild(closeBtnRow([PF.btn('Try again', 'primary', function () { start(teamId); })]));
      });
    }

    /* ----- progress ----- */
    var ui = null;
    function buildUI() {
      clear();
      var intro = PF.el('p', 'muted', mode === 'sim'
        ? 'Building the unsigned simulator runner with Xcode. This takes a few minutes the first time.'
        : 'Building WebDriverAgent with your own Apple team on this Mac (needs Xcode). The first build takes a few minutes. You can close this window; the build keeps running.');
      body.appendChild(intro);
      var steps = PF.el('div'); steps.style.cssText = 'display:flex;gap:8px;margin:8px 0;flex-wrap:wrap';
      var pills = {};
      PHASES.forEach(function (p) { pills[p[0]] = PF.pill(p[1], ''); steps.appendChild(pills[p[0]]); });
      body.appendChild(steps);
      var detail = PF.el('div', 'muted', ''); detail.style.cssText = 'margin:4px 0;font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap';
      body.appendChild(detail);
      var pre = PF.el('pre', 'muted', '');
      pre.style.cssText = 'height:240px;overflow:auto;white-space:pre-wrap;font-size:11px;margin:8px 0;background:var(--panel2);border:1px solid var(--line);border-radius:6px;padding:6px';
      body.appendChild(pre);
      var result = PF.el('div'); body.appendChild(result);
      var cancel = PF.btn('Cancel build', 'danger', function () {
        cancel.disabled = true;
        PF.api('POST', '/ios/wda/build/cancel', {}).catch(function () { /* already finished */ });
      });
      body.appendChild(closeBtnRow([cancel]));
      ui = { pills: pills, detail: detail, pre: pre, result: result, cancel: cancel, lines: [] };
    }

    function progress(st) {
      if (!ui) buildUI();
      var idx = -1;
      PHASES.forEach(function (p, i) { if (p[0] === st.phase) idx = i; });
      PHASES.forEach(function (p, i) {
        var kind = '';
        if (st.state === 'failed' || st.state === 'canceled') kind = i < idx ? 'ok' : (i === idx ? 'warn' : '');
        else kind = i < idx || st.state === 'succeeded' ? 'ok' : (i === idx ? 'warn' : '');
        ui.pills[p[0]].className = 'pill ' + kind;
      });
      var secs = Math.round((Date.now() - started) / 1000);
      ui.detail.textContent = (st.detail || '') + (st.state === 'running' ? '  (' + secs + 's)' : '');
      if (st.team && mode === 'device') ui.detail.title = 'Team ' + st.team;
      (st.lines || []).forEach(function (l) { ui.lines.push(l); });
      if (ui.lines.length > 300) ui.lines = ui.lines.slice(-300);
      var atEnd = ui.pre.scrollTop + ui.pre.clientHeight >= ui.pre.scrollHeight - 24;
      ui.pre.textContent = ui.lines.join('\n');
      if (atEnd) ui.pre.scrollTop = ui.pre.scrollHeight;
      if (typeof st.next === 'number') next = st.next;
    }

    function poll(attach) {
      if (closed) return;
      PF.api('GET', '/ios/wda/build?since=' + next).then(function (st) {
        if (closed) return;
        if (attach && !ui) { started = st.startedAt ? Date.parse(st.startedAt) || Date.now() : Date.now(); }
        progress(st);
        if (st.state === 'running') { timer = setTimeout(poll, 1000); return; }
        finish(st);
      }, function (e) {
        if (closed) return;
        if (ui) ui.detail.textContent = 'Lost contact with the server: ' + e.message + ' (retrying)';
        timer = setTimeout(poll, 2000);
      });
    }

    function finish(st) {
      ui.cancel.hidden = true;
      if (st.state === 'succeeded') {
        finished = true;
        ui.result.appendChild(PF.note('Build succeeded.', 'info'));
        if (opts.onSuccess) {
          var cb = opts.onSuccess; m.close();
          setTimeout(function () { cb(st); }, 0);
          return;
        }
        return;
      }
      if (st.state === 'canceled') { ui.result.appendChild(PF.note('Build canceled.', '')); }
      else if (st.issue) { renderIssue(ui.result, st.issue); if (st.logFile) ui.result.appendChild(PF.el('div', 'muted', 'Full log: ' + st.logFile)); }
      else ui.result.appendChild(PF.note('The build failed. See the log above.', 'bad'));
      var retry = PF.btn('Try again', 'primary', function () { ui = null; start(); });
      ui.result.appendChild(PF.el('div', 'btn-row')).appendChild(retry);
    }

    // Attach to a build that is already running (e.g. the window was closed earlier), else start.
    PF.api('GET', '/ios/wda/build').then(function (st) {
      if (st && st.state === 'running' && (st.mode || 'device') === mode) { next = 0; poll(true); } else start();
    }, function () { start(); });
  }

  PF.wdaBuild = { open: open, showIssue: showIssue, env: envOf, issueFromErr: issueFromErr };
})();
