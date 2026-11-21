/* Right column: Activity / Logs sub-tabs and the device log stream (GET /logs, text/event-stream via fetch). */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var MAX_LINES = 2000;
  var abort = null, running = false, pending = [], flushTimer = null, runId = 0;

  /* sub-tabs */
  Array.prototype.forEach.call(document.querySelectorAll('#rightTabs .subtab'), function (b) {
    b.addEventListener('click', function () {
      Array.prototype.forEach.call(document.querySelectorAll('#rightTabs .subtab'), function (o) { o.classList.toggle('active', o === b); });
      ['activityPane', 'logsPane'].forEach(function (id) { $(id).hidden = id !== b.getAttribute('data-sub'); });
      if (b.getAttribute('data-sub') === 'logsPane') scrollEnd();
    });
  });

  function status(t) { $('logStatus').textContent = t || ''; }
  function setRunning(on) { running = on; $('logToggle').textContent = on ? 'Stop' : 'Start'; $('logToggle').classList.toggle('primary', !on); }
  function scrollEnd() { var o = $('logOut'); if ($('logAuto').checked) o.scrollTop = o.scrollHeight; }
  function matches(text) {
    var q = $('logFilter').value.trim().toLowerCase();
    return !q || text.toLowerCase().indexOf(q) >= 0;
  }

  function flush() {
    flushTimer = null;
    var out = $('logOut'), frag = document.createDocumentFragment();
    pending.forEach(function (t) {
      var line = PF.el('div', '', t);
      line.hidden = !matches(t);
      frag.appendChild(line);
    });
    pending = [];
    out.appendChild(frag);
    while (out.childNodes.length > MAX_LINES) out.removeChild(out.firstChild);
    scrollEnd();
  }
  function addLine(t) {
    pending.push(t);
    if (!flushTimer) flushTimer = setTimeout(flush, 100);
  }

  function parse(block) {
    block.split('\n').forEach(function (line) {
      line = line.replace(/\r$/, '');
      if (line.indexOf('data:') !== 0) return; // ignore comments / event: / id: lines
      var v = line.slice(5);
      if (v.charAt(0) === ' ') v = v.slice(1);
      addLine(v);
    });
  }

  function stop(msg) {
    runId++;
    if (abort) { try { abort.abort(); } catch (e) { /* ignore */ } abort = null; }
    setRunning(false);
    if (msg !== undefined) status(msg);
  }

  function start() {
    if (!PF.needDevice()) return;
    var d = PF.selectedDevice();
    if (d && d.state !== 'connected') { PF.toast('Device is ' + d.state); return; }
    stop();
    var my = ++runId;
    abort = new AbortController();
    setRunning(true); status('Connecting...');
    fetch(PF.API + PF.dev('/logs'), { headers: { Accept: 'text/event-stream' }, signal: abort.signal, cache: 'no-store' }).then(function (res) {
      if (my !== runId) return;
      if (!res.ok) {
        return res.text().then(function (t) {
          var code = null, msg = null;
          try { var j = JSON.parse(t); code = j.code; msg = j.error || j.message; } catch (e) { /* plain */ }
          if (code === 'UNSUPPORTED' || res.status === 501) PF.toast(msg || 'Logs are not supported on this device', true);
          else if (res.status === 404) PF.toast('Log streaming is not available on this server (404)');
          else PF.toast('Logs failed: ' + PF.errMsg(res, t));
          stop('Not available (' + res.status + ')');
        });
      }
      status('Streaming');
      var reader = res.body.getReader(), dec = new TextDecoder(), buf = '';
      function pump() {
        return reader.read().then(function (r) {
          if (my !== runId) { try { reader.cancel(); } catch (e) { /* ignore */ } return; }
          if (r.done) { if (buf.trim()) parse(buf); stop('Stream ended'); return; }
          buf += dec.decode(r.value, { stream: true });
          var i;
          while ((i = buf.indexOf('\n\n')) >= 0) { parse(buf.slice(0, i)); buf = buf.slice(i + 2); }
          return pump();
        });
      }
      return pump();
    }).catch(function (e) {
      if (my !== runId || (e && e.name === 'AbortError')) return;
      PF.toast('Logs error: ' + e.message);
      stop('Error');
    });
  }

  $('logToggle').addEventListener('click', function () { if (running) stop('Stopped'); else start(); });
  $('logClear').addEventListener('click', function () { pending = []; $('logOut').innerHTML = ''; });
  $('logAuto').addEventListener('change', scrollEnd);
  $('logFilter').addEventListener('input', function () {
    Array.prototype.forEach.call($('logOut').childNodes, function (n) { n.hidden = !matches(n.textContent); });
    scrollEnd();
  });
  PF.hooks.onSelect.push(function () { if (running) stop('Stopped (device changed)'); });
})();
