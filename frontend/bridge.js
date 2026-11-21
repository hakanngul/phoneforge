/*
 * Bridge controls (Android): start/stop the UiAutomator2 helper, status dot per device row,
 * "Bridge: ..." chip in the Live toolbar, and pinch (Ctrl/Cmd + wheel, Zoom +/- buttons).
 *
 * Consent: the first Start for a device shows a confirm modal; acceptance is remembered per device id
 * in localStorage ("pf.bridgeConsent": {deviceId: true}).
 * Assets missing: POST .../bridge/start answering 409 BRIDGE_ASSETS_MISSING opens a modal offering
 * "Download helper (official Appium release)" -> POST /bridge/assets/download, then Start is retried.
 * Polling: GET .../bridge every 5 s for the selected Android device only.
 *
 * iOS (real devices): the same chip/row UI drives the WebDriverAgent (WDA) bridge via
 * /devices/{id}/iosbridge/{status,start,stop,logs,install}. Chip shows "WDA: stopped|starting|running|error";
 * Start/Stop in the toolbar and device row; "Install helper" appears while status.installed is false (confirm
 * first); an error opens a modal with the error text and the last runner log lines. When the state flips
 * (start/stop) PF.iosBridgeChanged(id) makes the Live view switch between MJPEG and screenshots.
 */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var status = {};      // device id -> last status object (or {unavailable: msg})
  var starting = {};    // device id -> true while a start request is in flight
  var stopping = {};
  var pollTimer = null;
  var CONSENT_KEY = 'pf.bridgeConsent';

  function bpath(id, suffix) { return '/devices/' + encodeURIComponent(id) + '/bridge' + (suffix || ''); }
  function selAndroid() { var d = PF.selectedDevice(); return d && PF.isAndroid(d) && !PF.isRemote(d) ? d : null; }

  /* ---------- consent ---------- */
  function hasConsent(id) {
    try { var m = JSON.parse(localStorage.getItem(CONSENT_KEY) || '{}'); return !!m[id]; } catch (e) { return false; }
  }
  function setConsent(id) {
    try { var m = JSON.parse(localStorage.getItem(CONSENT_KEY) || '{}'); m[id] = true; localStorage.setItem(CONSENT_KEY, JSON.stringify(m)); } catch (e) { /* ignore */ }
  }

  /* ---------- state ---------- */
  function stateOf(id) {
    var s = status[id];
    if (starting[id]) return 'starting';
    if (!s || s.unavailable) return 'stopped';
    if (s.running) return 'running';
    if (s.lastError) return 'error';
    return 'stopped';
  }
  function tipOf(id) {
    var s = status[id], st = stateOf(id);
    if (s && s.unavailable) return 'Bridge unavailable: ' + s.unavailable;
    if (st === 'error') return 'Bridge error: ' + s.lastError;
    if (st === 'running') return 'Bridge running' + (s && s.port ? ' on port ' + s.port : '');
    if (st === 'starting') return 'Starting bridge...';
    if (s && s.supported === false) return 'Bridge not supported on this device';
    return 'Bridge stopped';
  }

  function setStatus(id, s) {
    var same = JSON.stringify(status[id]) === JSON.stringify(s);
    status[id] = s;
    if (!same || starting[id] !== undefined) refreshUI();
  }

  function fetchStatus(id) {
    return PF.api('GET', bpath(id)).then(function (s) { setStatus(id, s || {}); return s; }, function (e) {
      var m = (e.status === 404 || e.status === 501) ? 'endpoint not available on this server yet (' + e.status + ')' : e.message;
      setStatus(id, { unavailable: m });
    });
  }

  /* ---------- UI ---------- */
  function refreshUI() {
    var d = selAndroid(), chip = $('bridgeChip');
    refreshIOSUI();
    if (!d) { if (!selIOS()) chip.hidden = true; }
    else {
      var st = stateOf(d.id), s = status[d.id];
      chip.hidden = false;
      chip.className = 'bchip ' + st;
      chip.textContent = (s && s.unavailable) ? 'Bridge: unavailable' : 'Bridge: ' + (st === 'running' ? 'running' : st === 'starting' ? 'starting' : st === 'error' ? 'error' : 'stopped');
      chip.title = tipOf(d.id);
    }
    if (PF.refreshDeviceRows) PF.refreshDeviceRows();
  }

  // Called by automation.js for every device row: adds the play/stop button + status dot to Android rows.
  PF.decorateDevice = function (d, host) {
    if (isIOSDev(d)) { decorateIOS(d, host); return; }
    if (!PF.isAndroid(d) || PF.isRemote(d)) return;
    var st = stateOf(d.id), on = st === 'running' || st === 'starting';
    var b = PF.el('button', 'btn sm bbtn');
    b.type = 'button';
    b.appendChild(PF.el('span', on ? 'sqr' : 'tri'));
    b.appendChild(PF.el('span', 'bdot ' + st));
    b.title = (on ? 'Stop bridge. ' : 'Start bridge. ') + tipOf(d.id);
    b.setAttribute('aria-label', b.title);
    b.disabled = st === 'starting' || !!stopping[d.id] || d.state !== 'connected';
    b.addEventListener('click', function (ev) {
      ev.stopPropagation();
      if (on) stopBridge(d.id); else startBridge(d.id);
    });
    host.appendChild(b);
  };

  /* ---------- start / stop ---------- */
  function confirmModal(title, lines, okLabel, onOk, onCancel) {
    var m = PF.modal.open(title, { onClose: function () { if (!done && onCancel) onCancel(); } }), done = false;
    lines.forEach(function (t) { m.body.appendChild(PF.el('p', '', t)); });
    var row = PF.el('div', 'btn-row');
    row.appendChild(PF.btn(okLabel, 'primary', function () { done = true; m.close(); onOk(); }));
    row.appendChild(PF.btn('Cancel', '', function () { m.close(); }));
    m.body.appendChild(row);
  }

  function startBridge(id) {
    if (hasConsent(id)) { doStart(id); return; }
    confirmModal('Start bridge?', [
      'This starts the UiAutomator2 helper app on the device. If the helper is not installed, PhoneForge will install it.'
    ], 'Start', function () { setConsent(id); doStart(id); });
  }

  function doStart(id) {
    starting[id] = true; refreshUI();
    return PF.api('POST', bpath(id, '/start'), {}).then(function (s) {
      starting[id] = false; setStatus(id, s || {});
      if (s && s.lastError && !s.running) PF.toast('Bridge: ' + s.lastError);
      else PF.toast('Bridge started', true);
    }, function (e) {
      starting[id] = false;
      if (e.status === 409 && e.code === 'BRIDGE_ASSETS_MISSING') { refreshUI(); assetsModal(id, e.serverMessage); return; }
      if (e.status === 404 || e.status === 501) {
        setStatus(id, { unavailable: e.serverMessage || 'endpoint not available on this server yet (' + e.status + ')' });
        PF.toast('Bridge: ' + (e.serverMessage || 'not available on this server yet (' + e.status + ')'), true);
        return;
      }
      refreshUI(); PF.toast(e.message);
    });
  }

  function stopBridge(id) {
    stopping[id] = true; refreshUI();
    return PF.api('POST', bpath(id, '/stop'), {}).then(function (s) {
      setStatus(id, s || {}); PF.toast('Bridge stopped', true);
    }, function (e) {
      if (e.status === 404 || e.status === 501) PF.toast('Bridge: ' + (e.serverMessage || 'not available on this server yet'), true);
      else PF.toast(e.message);
    }).then(function () { stopping[id] = false; refreshUI(); });
  }

  /* ---------- assets missing ---------- */
  function assetsModal(id, serverMsg) {
    var m = PF.modal.open('Bridge helper not found');
    m.body.appendChild(PF.el('p', '', serverMsg || 'The UiAutomator2 helper APKs are not available on this machine.'));
    var info = PF.el('div', 'muted', 'Checking helper files...');
    m.body.appendChild(info);
    PF.api('GET', '/bridge/assets').then(function (a) {
      info.textContent = '';
      if (!a) return;
      info.appendChild(PF.el('div', '', 'Folder: ' + (a.dir || '?')));
      info.appendChild(PF.el('div', '', 'Server APK: ' + (a.serverApk || 'missing')));
      info.appendChild(PF.el('div', '', 'Test APK: ' + (a.testApk || 'missing')));
    }, function (e) {
      info.textContent = (e.status === 404 || e.status === 501) ? 'Helper status endpoint not available yet.' : e.message;
    });
    var msg = PF.el('div', 'muted'); msg.style.margin = '6px 0';
    m.body.appendChild(msg);
    var row = PF.el('div', 'btn-row');
    var dl = PF.btn('Download helper (official Appium release)', 'primary');
    dl.addEventListener('click', function () {
      dl.disabled = true; msg.textContent = 'Downloading...';
      PF.api('POST', '/bridge/assets/download', {}).then(function () {
        m.close(); doStart(id);
      }, function (e) {
        dl.disabled = false;
        msg.textContent = (e.status === 404 || e.status === 501) ? 'Download endpoint not available on this server yet (' + e.status + ').' : 'Download failed: ' + (e.serverMessage || e.message);
      });
    });
    row.appendChild(dl);
    row.appendChild(PF.btn('Cancel', '', function () { m.close(); }));
    m.body.appendChild(row);
  }

  /* ---------- iOS: WebDriverAgent bridge ---------- */
  var istatus = {};     // device id -> raw iosbridge status ({state, wdaUrl, mjpegUrl, error, installed}) or {unavailable}
  var istarting = {}, istopping = {}, iinstalling = {};
  function isIOSDev(d) { return !!d && d.platform === 'ios' && !PF.isRemote(d); }
  function selIOS() { var d = PF.selectedDevice(); return isIOSDev(d) ? d : null; }
  function ipath(id, suffix) { return '/devices/' + encodeURIComponent(id) + '/iosbridge' + suffix; }
  function iState(id) {
    if (istarting[id] || iinstalling[id]) return 'starting';
    var s = istatus[id];
    if (!s || s.unavailable) return 'stopped';
    return s.state || 'stopped';
  }
  function iTip(id) {
    var s = istatus[id] || {}, st = iState(id);
    if (iinstalling[id]) return 'Installing the WDA helper...';
    if (s.unavailable) return 'WDA unavailable: ' + s.unavailable;
    if (st === 'error') return 'WDA error: ' + (s.error || 'unknown') + ' (click for logs)';
    if (st === 'running') return 'WDA running';
    if (st === 'starting') return 'Starting WDA...';
    if (s.installed === false) return 'WDA helper is not installed on this iPhone';
    return 'WDA stopped';
  }
  PF.iosBridge = { running: function (id) { return !!istatus[id] && istatus[id].state === 'running' && !istarting[id]; } };

  function setIStatus(id, s) {
    var prev = istatus[id], wasRun = !!prev && prev.state === 'running';
    var same = JSON.stringify(prev) === JSON.stringify(s);
    istatus[id] = s;
    var isRun = !!s && s.state === 'running';
    if (!same) refreshUI();
    if (wasRun !== isRun && PF.iosBridgeChanged) PF.iosBridgeChanged(id);
  }
  function fetchIStatus(id) {
    return PF.api('GET', ipath(id, '/status')).then(function (s) { setIStatus(id, s || {}); return s; }, function (e) {
      setIStatus(id, { unavailable: (e.status === 404 || e.status === 501) ? 'endpoint not available on this server (' + e.status + ')' : e.message });
    });
  }

  var wdaBtn = PF.el('button', 'btn sm'), wdaInstallBtn = PF.el('button', 'btn sm', 'Install helper');
  wdaBtn.type = 'button'; wdaInstallBtn.type = 'button';
  wdaBtn.hidden = wdaInstallBtn.hidden = true;
  $('bridgeChip').parentNode.insertBefore(wdaBtn, $('bridgeChip').nextSibling);
  $('bridgeChip').parentNode.insertBefore(wdaInstallBtn, wdaBtn.nextSibling);
  wdaBtn.addEventListener('click', function () {
    var d = selIOS(); if (!d) return;
    var st = iState(d.id);
    if (st === 'running') stopIOS(d.id); else if (st !== 'starting') startIOS(d.id);
  });
  wdaInstallBtn.addEventListener('click', function () { var d = selIOS(); if (d) installIOS(d.id); });
  $('bridgeChip').addEventListener('click', function () {
    var d = selIOS(); if (d && (iState(d.id) === 'error' || (istatus[d.id] || {}).unavailable)) showIOSDetails(d.id);
  });
  $('bridgeChip').style.cursor = 'pointer';

  function refreshIOSUI() {
    var d = selIOS(), chip = $('bridgeChip');
    if (!d) { wdaBtn.hidden = wdaInstallBtn.hidden = true; return; }
    var st = iState(d.id), s = istatus[d.id] || {};
    chip.hidden = false;
    chip.className = 'bchip ' + st;
    chip.textContent = s.unavailable ? 'WDA: unavailable' : 'WDA: ' + st;
    chip.title = iTip(d.id);
    var conn = d.state === 'connected';
    wdaBtn.hidden = false;
    wdaBtn.textContent = st === 'running' ? 'Stop WDA' : 'Start WDA';
    wdaBtn.disabled = st === 'starting' || !!istopping[d.id] || !conn;
    wdaInstallBtn.hidden = !(s.installed === false) || st === 'running';
    wdaInstallBtn.disabled = st === 'starting' || !conn;
  }

  function decorateIOS(d, host) {
    var st = iState(d.id), on = st === 'running' || st === 'starting';
    var b = PF.el('button', 'btn sm bbtn');
    b.type = 'button';
    b.appendChild(PF.el('span', on ? 'sqr' : 'tri'));
    b.appendChild(PF.el('span', 'bdot ' + st));
    b.title = (on ? 'Stop WDA. ' : 'Start WDA. ') + iTip(d.id);
    b.setAttribute('aria-label', b.title);
    b.disabled = st === 'starting' || !!istopping[d.id] || d.state !== 'connected';
    b.addEventListener('click', function (ev) {
      ev.stopPropagation();
      if (on) stopIOS(d.id); else startIOS(d.id);
    });
    host.appendChild(b);
    if ((istatus[d.id] || {}).installed === false && !on) {
      var ib = PF.el('button', 'btn sm bbtn', 'Install helper');
      ib.type = 'button'; ib.disabled = d.state !== 'connected' || !!iinstalling[d.id];
      ib.addEventListener('click', function (ev) { ev.stopPropagation(); installIOS(d.id); });
      host.appendChild(ib);
    }
  }

  function startIOS(id) {
    istarting[id] = true; refreshUI();
    return PF.api('POST', ipath(id, '/start'), {}).then(function (s) {
      istarting[id] = false; setIStatus(id, s || {}); PF.toast('WDA running', true);
    }, function (e) {
      istarting[id] = false;
      if (e.status === 409 && e.code === 'IOSBRIDGE_NOT_INSTALLED') {
        return fetchIStatus(id).then(function () { installIOS(id, true); });
      }
      return fetchIStatus(id).then(function () {
        PF.toast('WDA: ' + (e.serverMessage || e.message));
        showIOSDetails(id, e.serverMessage || e.message);
      });
    }).then(refreshUI);
  }

  function stopIOS(id) {
    istopping[id] = true; refreshUI();
    return PF.api('POST', ipath(id, '/stop'), {}).then(function (s) {
      setIStatus(id, s || {}); PF.toast('WDA stopped', true);
    }, function (e) { PF.toast(e.serverMessage || e.message); }).then(function () { istopping[id] = false; refreshUI(); });
  }

  function installIOS(id, thenStart) {
    confirmModal('Install helper?', [
      'PhoneForge\'un WebDriverAgent\'ı bu iPhone\'a kurulacak.',
      'The WDA helper was built with your own signing identity (scripts/build-wda.sh) and is installed over USB. You may need to trust the developer profile on the phone afterwards.'
    ], 'Install', function () {
      iinstalling[id] = true; refreshUI();
      PF.api('POST', ipath(id, '/install'), {}).then(function (s) {
        iinstalling[id] = false; setIStatus(id, s || {});
        PF.toast('WDA helper installed', true);
        if (thenStart) startIOS(id);
      }, function (e) {
        iinstalling[id] = false; refreshUI();
        PF.toast(e.serverMessage || e.message);
      });
    });
  }

  function showIOSDetails(id, extra) {
    var s = istatus[id] || {};
    var m = PF.modal.open('WDA bridge error');
    m.body.appendChild(PF.el('p', '', (extra || s.error || s.unavailable || 'No error reported.')));
    if (s.state) m.body.appendChild(PF.el('div', 'muted', 'State: ' + s.state + ' / helper installed: ' + (s.installed === false ? 'no' : s.installed ? 'yes' : '?')));
    var pre = PF.el('pre', 'muted', 'Loading logs...');
    pre.style.cssText = 'max-height:260px;overflow:auto;white-space:pre-wrap;font-size:11px;margin:8px 0';
    m.body.appendChild(pre);
    PF.api('GET', ipath(id, '/logs?format=json')).then(function (r) {
      var lines = (r && r.lines) || [];
      pre.textContent = lines.length ? lines.slice(-30).join('\n') : '(no runner log lines yet)';
    }, function (e) { pre.textContent = 'Could not load logs: ' + e.message; });
    var row = PF.el('div', 'btn-row');
    row.appendChild(PF.btn('Close', '', function () { m.close(); }));
    m.body.appendChild(row);
  }

  /* ---------- polling (selected device only) ---------- */
  function poll() {
    var d = selAndroid(), i = selIOS();
    if (d && d.state === 'connected' && !document.hidden && !starting[d.id]) fetchStatus(d.id);
    if (i && i.state === 'connected' && !document.hidden && !istarting[i.id] && !iinstalling[i.id]) fetchIStatus(i.id);
  }
  function onSel() {
    var d = selAndroid(), i = selIOS();
    refreshUI();
    if (d) fetchStatus(d.id);
    if (i) fetchIStatus(i.id);
  }
  PF.hooks.onSelect.push(onSel);
  PF.hooks.onDevices.push(function () {
    var d = selAndroid(), i = selIOS();
    if (d && !status[d.id]) fetchStatus(d.id);
    if (i && !istatus[i.id]) fetchIStatus(i.id);
  });
  pollTimer = setInterval(poll, 5000);

  /* ---------- pinch ---------- */
  function sendPinch(x, y, scale) {
    if (!PF.needDevice()) return;
    var d = PF.selectedDevice();
    if (d && PF.isIOS(d) && !(PF.iosBridge && PF.iosBridge.running(d.id))) { PF.toast('Pinch on iOS needs the WDA bridge (Start WDA)', true); return; }
    PF.guardDev(PF.api('POST', PF.dev('/pinch'), { x: Math.round(x), y: Math.round(y), scale: Math.round(scale * 100) / 100, durationMs: 300 }).then(function () {
      if (PF.mirrorSoon) PF.mirrorSoon();
    }, function (e) {
      if (e.code === 'UNSUPPORTED' || e.status === 501) throw e; // guardDev shows the server message
      if (e.status === 404) { var e2 = new Error('Pinch endpoint not available on this server yet (404)'); throw e2; }
      throw e;
    }));
  }

  var wheel = null;   // {x, y, scale, timer}
  function onWheel(ev) {
    if (!(ev.ctrlKey || ev.metaKey)) return;
    ev.preventDefault();
    if (!PF.selectedId) return;
    var G = PF.mirrorGeo, p = G && G.toDevice(ev, true);
    if (!p) return;
    var f = ev.deltaY < 0 ? 1.15 : 1 / 1.15;
    if (!wheel) {
      wheel = { x: p.x, y: p.y, scale: 1, devId: PF.selectedId };
      wheel.timer = setTimeout(function () {
        var w = wheel; wheel = null;
        if (!w || w.devId !== PF.selectedId) return;
        var s = Math.min(Math.max(w.scale, 0.4), 3);
        if (Math.abs(s - 1) < 0.03) return;
        sendPinch(w.x, w.y, s);
      }, 250);
    }
    wheel.scale *= f; // accumulated within the 250 ms window -> one request
  }
  [$('mirror'), $('vmirror')].forEach(function (el) { el.addEventListener('wheel', onWheel, { passive: false }); });

  function zoom(scale) {
    if (!PF.needDevice()) return;
    var m = PF.mirrorGeo && PF.mirrorGeo.metrics();
    if (!m) { PF.toast('No screen loaded yet', true); return; }
    sendPinch(m.dw / 2, m.dh / 2, scale);
  }
  $('zoomIn').addEventListener('click', function () { zoom(1.5); });
  $('zoomOut').addEventListener('click', function () { zoom(0.67); });
})();
