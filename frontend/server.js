/* Server panel + connection monitor: status, uptime, start/stop history with reasons,
 * and a banner when the server stops answering. A web page can stop the server (Quit)
 * but cannot start it again; the banner says how. */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var LS = 'pf.lastServer';
  var START_CMD = 'phoneforge server start';
  var fails = 0, banner = null, lastStatus = null, lastSeen = null, shuttingDown = false, timer = null;

  function inDesktop() { return window.PF_ORIGIN === 'desktop-ui'; }
  PF.originLabel = function () { return window.PF_ORIGIN || 'web-ui'; };

  try {
    var saved = JSON.parse(localStorage.getItem(LS) || 'null');
    if (saved) { lastStatus = saved.status; lastSeen = saved.seen ? new Date(saved.seen) : null; }
  } catch (e) { /* ignore */ }
  function remember() {
    try { localStorage.setItem(LS, JSON.stringify({ status: lastStatus, seen: lastSeen && lastSeen.toISOString() })); } catch (e) { /* ignore */ }
  }

  function when(t) { var d = new Date(t); return isNaN(d) ? String(t || '') : d.toLocaleString(); }
  function clock(d) { return d ? d.toLocaleTimeString() : '?'; }
  function dur(sec) {
    sec = Math.max(0, Math.floor(sec));
    var d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60), s = sec % 60;
    return d ? d + 'd ' + h + 'h' : h ? h + 'h ' + m + 'm' : m ? m + 'm ' + s + 's' : s + 's';
  }
  function ago(d) { return d ? dur((Date.now() - d.getTime()) / 1000) + ' ago' : ''; }

  /* ---------- banner ---------- */
  function ensureBanner() {
    if (banner) return banner;
    banner = PF.el('div', 'srv-banner'); banner.hidden = true; banner.setAttribute('role', 'alert');
    var main = document.querySelector('main');
    document.body.insertBefore(banner, main);
    return banner;
  }
  function showBanner() {
    var b = ensureBanner();
    b.innerHTML = '';
    b.appendChild(PF.el('strong', '', shuttingDown ? 'Server was shutting down and is no longer running' : 'Server is not running'));
    var s = lastStatus;
    var bits = [];
    if (s) {
      if (s.pid) bits.push('pid ' + s.pid);
      if (s.version) bits.push('v' + s.version);
      if (s.addr) bits.push(s.addr);
      if (s.startedAt) bits.push('started ' + when(s.startedAt));
      if (s.startedVia) bits.push('via ' + s.startedVia);
    }
    if (lastSeen) bits.push('last seen up ' + clock(lastSeen) + ' (' + ago(lastSeen) + ')');
    b.appendChild(PF.el('div', 'srv-line', bits.length ? 'Last known: ' + bits.join(' · ') : 'No earlier status was seen by this page.'));
    var how = PF.el('div', 'srv-line');
    if (inDesktop()) {
      how.textContent = 'Use the PhoneForge app: Server menu ▸ Start server (or the menu-bar icon).';
    } else {
      how.appendChild(document.createTextNode('A web page cannot start the server. In a terminal run '));
      how.appendChild(PF.el('code', '', START_CMD));
      how.appendChild(document.createTextNode(' (or '));
      how.appendChild(PF.el('code', '', 'phoneforged'));
      how.appendChild(document.createTextNode('), or open the PhoneForge app. '));
      how.appendChild(PF.btn('Copy command', 'sm', function () { PF.copyText(START_CMD, 'command'); }));
    }
    b.appendChild(how);
    b.appendChild(PF.el('div', 'srv-line muted', 'Why it stopped is recorded in ~/.phoneforge/server-events.jsonl (phoneforge server history). This page reconnects automatically.'));
    b.hidden = false;
    setDot('down');
  }
  function hideBanner() { if (banner) banner.hidden = true; }
  function setDot(state) { var d = $('srvDot'); if (d) d.className = 'srv-dot ' + state; }

  /* ---------- monitor ---------- */
  function check() {
    PF.api('GET', '/health').then(function (h) {
      var wasDown = fails >= 2;
      fails = 0;
      shuttingDown = !!h.shuttingDown;
      $('ver').textContent = 'v' + (h.version || '?') + ' · ' + (h.deviceCount != null ? h.deviceCount : 0) + ' device' + (h.deviceCount === 1 ? '' : 's');
      lastSeen = new Date();
      setDot(shuttingDown ? 'warn' : 'up');
      if (wasDown) {
        hideBanner();
        PF.toast('Server is back', true);
        if (PF.loadDevices) PF.loadDevices();
      }
      return PF.api('GET', '/server/status').then(function (s) { lastStatus = s; remember(); }, function () { remember(); });
    }).catch(function () {
      fails++;
      $('ver').textContent = 'server unreachable';
      if (fails >= 2) showBanner();
    }).then(function () {
      timer = setTimeout(check, fails ? 2000 : 5000);
    });
  }
  PF.startServerMonitor = function () { clearTimeout(timer); check(); };

  /* ---------- quit (stop the server) ---------- */
  PF.quitServer = function (btn) {
    PF.confirm('This stops the server and closes the app; devices will no longer be controllable.', function () {
      if (btn) btn.disabled = true;
      fetch('/api/v1/server/shutdown', { method: 'POST', headers: { 'X-PhoneForge-Action': 'shutdown', 'X-PhoneForge-Origin': PF.originLabel() } }).then(function (r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        clearTimeout(timer);
        document.title = 'PhoneForge stopped';
        document.body.innerHTML = '<div style="display:flex;height:100vh;align-items:center;justify-content:center;flex-direction:column;font-family:system-ui,sans-serif;color:#ccc;background:#0e0d14"><h2>PhoneForge stopped</h2><p>You can close this tab. Start it again with <code>phoneforge server start</code> or the PhoneForge app.</p></div>';
      }).catch(function (e) { if (btn) btn.disabled = false; PF.toast('Could not stop PhoneForge: ' + e.message); });
    }, { title: 'Quit PhoneForge?', okLabel: 'Quit', danger: true });
  };

  /* ---------- panel ---------- */
  var BAD = { unexpected_exit: 1, crash: 1, listen_failed: 1 };
  var LABEL = { start: 'started', stop: 'stopped', unexpected_exit: 'unexpected exit', crash: 'crashed', listen_failed: 'listen failed' };

  PF.openServer = function () {
    var tick = null, poll = null, status = null;
    var m = PF.modal.open('Server', { wide: true, onClose: function () { clearInterval(tick); clearInterval(poll); } });
    var statusBox = PF.el('div'), histBox = PF.el('div');
    var bar = PF.el('div', 'btn-row');
    bar.appendChild(PF.btn('Refresh', 'sm', load));
    bar.appendChild(PF.btn('⏻ Quit PhoneForge (stop server)', 'sm danger', function (e) { PF.quitServer(e.currentTarget); }));
    m.body.appendChild(statusBox); m.body.appendChild(bar);
    var hint = PF.el('p', 'muted');
    if (inDesktop()) hint.textContent = 'This app can also start, stop and restart its server from the Server menu.';
    else { hint.appendChild(document.createTextNode('A web page can stop the server but cannot start it again. To start it: ')); hint.appendChild(PF.el('code', '', START_CMD)); hint.appendChild(document.createTextNode(' (or open the PhoneForge app).')); }
    m.body.appendChild(hint);
    m.body.appendChild(PF.section('Start / stop history'));
    m.body.appendChild(histBox);

    function kv(k, v) { var r = PF.el('div', 'srv-kv'); r.appendChild(PF.el('span', 'srv-k', k)); var d = PF.el('span', 'srv-v'); if (v instanceof Node) d.appendChild(v); else d.textContent = v; r.appendChild(d); return r; }
    var uptimeEl = PF.el('span');
    function paintStatus(err) {
      statusBox.innerHTML = '';
      var c = PF.el('div', 'card'), head = PF.el('div', 'card-head');
      head.appendChild(PF.el('strong', '', 'Status'));
      head.appendChild(PF.pill(err ? 'unreachable' : status && status.stopping ? 'stopping' : 'running', err ? 'bad' : status && status.stopping ? 'warn' : 'ok'));
      c.appendChild(head);
      if (err) c.appendChild(PF.el('p', 'muted', 'Could not read /server/status (' + err.message + '). ' + (lastStatus ? 'Last known: pid ' + lastStatus.pid + ', ' + (lastStatus.addr || '') + '.' : '')));
      else if (status) {
        c.appendChild(kv('PID', String(status.pid)));
        c.appendChild(kv('Version', status.version || '?'));
        c.appendChild(kv('Address', status.addr || ''));
        c.appendChild(kv('Started', when(status.startedAt)));
        c.appendChild(kv('Uptime', uptimeEl));
        c.appendChild(kv('Started via', (status.startedVia || '?') + ' (owner: ' + (status.owner || '?') + ')'));
        if (status.parentPid) c.appendChild(kv('Parent', status.parentPid + (status.parentCmd ? ' ' + status.parentCmd : '')));
        c.appendChild(kv('Event log', status.eventsFile || ''));
        showUptime();
      }
      statusBox.appendChild(c);
    }
    function showUptime() { if (status) uptimeEl.textContent = dur((Date.now() - new Date(status.startedAt).getTime()) / 1000); }

    function paintHistory(events, err) {
      histBox.innerHTML = '';
      if (err) { histBox.appendChild(PF.note('Could not load history: ' + err.message, 'bad')); return; }
      var rows = events.map(function (e) {
        var cell = PF.el('div');
        cell.appendChild(PF.el('div', '', e.reason || ''));
        var extra = [];
        if (e.origin) extra.push('origin: ' + e.origin);
        if (e.signal) extra.push('signal: ' + e.signal);
        if (e.event === 'start' && e.parentCmd) extra.push('parent: ' + e.parentPid + ' ' + e.parentCmd);
        if (e.lastHeartbeat) extra.push('last heartbeat: ' + when(e.lastHeartbeat));
        if (e.pidGone != null) extra.push(e.pidGone ? 'pid gone' : 'pid still alive');
        if (e.uptimeSec) extra.push('ran ' + dur(e.uptimeSec));
        if (extra.length) cell.appendChild(PF.el('div', 'muted', extra.join(' · ')));
        if (e.message && e.message !== e.reason) cell.appendChild(PF.el('div', 'muted', e.message));
        if (e.logTail && e.logTail.length) {
          var d = PF.el('details'); d.appendChild(PF.el('summary', 'muted', 'last log lines (' + e.logTail.length + ')'));
          d.appendChild(PF.el('pre', 'srv-log', e.logTail.join('\n'))); cell.appendChild(d);
        }
        var kind = BAD[e.event] ? 'bad' : e.event === 'start' ? 'ok' : '';
        return [when(e.time), PF.pill(LABEL[e.event] || e.event, kind), String(e.pid || ''), e.startedVia || '', cell];
      });
      var t = PF.table(['Time', 'Event', 'PID', 'Via', 'Reason'], rows, 'No lifecycle events recorded yet.');
      Array.prototype.forEach.call(t.querySelectorAll('tbody tr'), function (tr, i) { if (events[i] && BAD[events[i].event]) tr.className = 'srv-bad'; });
      histBox.appendChild(t);
    }

    function load() {
      PF.api('GET', '/server/status').then(function (s) { status = s; paintStatus(); }, function (e) { paintStatus(e); });
      PF.api('GET', '/server/history?limit=100').then(function (r) { paintHistory((r && r.events) || []); }, function (e) { paintHistory([], e); });
    }
    load();
    tick = setInterval(showUptime, 1000);
    poll = setInterval(load, 5000);
  };

  var btn = $('serverBtn');
  if (btn) btn.addEventListener('click', function () { PF.openServer(); });
})();
