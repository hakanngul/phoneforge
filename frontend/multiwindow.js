/*
 * MultiWindow: a responsive grid of live mirrors for several devices at once.
 * Each connected device gets a tile (phone frame + name + platform badge) with its own
 * sequential screenshot loop (GET /devices/{id}/screenshot, ~2 fps, never overlapping,
 * with backoff on errors). Tiles are reconciled whenever the device list refreshes, loops stop
 * when Multi is off or the Automation tab is hidden. Clicking a tile makes it the active device.
 * A tile whose device cannot screenshot (e.g. iOS without the WDA bridge) shows a notice.
 * Tiles have a checkbox so the user can pick which devices to watch.
 */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var tiles = {};          // id -> tile state
  var excluded = {};       // id -> true when unchecked by the user
  var MULTI_FPS = 2;

  function active() { return PF.multi && !$('automation-tab').hidden; }

  function makeTile(d) {
    var t = { id: d.id, gen: 0, timer: null, url: null, fails: 0 };
    t.root = PF.el('div', 'mtile');
    var head = PF.el('div', 'mhead');
    t.cb = document.createElement('input'); t.cb.type = 'checkbox'; t.cb.checked = !excluded[d.id];
    t.cb.title = 'Include in grid';
    t.cb.addEventListener('click', function (e) { e.stopPropagation(); });
    t.cb.addEventListener('change', function () {
      if (t.cb.checked) delete excluded[d.id]; else excluded[d.id] = true;
      sync();
    });
    t.name = PF.el('strong', 'mname');
    t.plat = PF.el('span', 'plat');
    head.appendChild(t.cb); head.appendChild(t.name); head.appendChild(t.plat);
    t.phone = PF.el('div', 'mphone');
    t.img = document.createElement('img'); t.img.alt = 'screen'; t.img.draggable = false;
    t.msg = PF.el('div', 'placeholder');
    t.phone.appendChild(t.img); t.phone.appendChild(t.msg);
    t.root.appendChild(head); t.root.appendChild(t.phone);
    t.root.addEventListener('click', function () { PF.selectDevice(d.id); markActive(); });
    return t;
  }

  function setMsg(t, text) { t.msg.textContent = text || ''; t.msg.hidden = !text; }

  function stopTile(t) {
    clearTimeout(t.timer); t.gen++;
    if (t.url) { URL.revokeObjectURL(t.url); t.url = null; }
  }

  function loop(t, gen) {
    if (gen !== t.gen) return;
    var d = devOf(t.id);
    if (!active() || !d || d.state !== 'connected' || t.cb.checked === false) { t.running = false; return; }
    var t0 = Date.now();
    PF.api('GET', '/devices/' + encodeURIComponent(t.id) + '/screenshot?t=' + t0, undefined, 'blob').then(function (blob) {
      if (gen !== t.gen) return;
      var u = URL.createObjectURL(blob);
      t.img.src = u;
      if (t.url) URL.revokeObjectURL(t.url);
      t.url = u; t.fails = 0; setMsg(t, '');
    }).catch(function () {
      if (gen !== t.gen) return;
      t.fails++;
      if (!t.img.getAttribute('src')) setMsg(t, PF.isIOS(d) ? PF.IOS_NOTICE : 'Screenshot unavailable');
    }).then(function () {
      if (gen !== t.gen) return;
      var wait = Math.max(1000 / MULTI_FPS - (Date.now() - t0), 0) + Math.min(t.fails, 8) * 1000;
      t.timer = setTimeout(function () { loop(t, gen); }, wait);
    });
  }

  function devOf(id) {
    for (var i = 0; i < PF.devices.length; i++) if (PF.devices[i].id === id) return PF.devices[i];
    return null;
  }

  function markActive() {
    Object.keys(tiles).forEach(function (id) { tiles[id].root.classList.toggle('sel', id === PF.selectedId); });
  }

  // Reconcile tiles with the device list and (re)start loops as needed.
  function sync() {
    var grid = $('multiGrid');
    if (!PF.multi) { Object.keys(tiles).forEach(function (id) { stopTile(tiles[id]); }); return; }
    var seen = {};
    PF.devices.forEach(function (d) {
      seen[d.id] = true;
      var t = tiles[d.id];
      if (!t) { t = tiles[d.id] = makeTile(d); grid.appendChild(t.root); }
      t.name.textContent = d.name || d.model || d.id;
      t.plat.textContent = d.platform || '?';
      t.plat.className = 'plat ' + (d.platform || '');
      var up = d.state === 'connected';
      t.root.classList.toggle('off', !up || !t.cb.checked);
      if (!up) { stopTile(t); t.running = false; setMsg(t, 'Device is ' + d.state); }
      else if (!t.cb.checked) { stopTile(t); t.running = false; setMsg(t, 'Paused'); }
      else if (!t.running && active()) { t.running = true; if (t.msg.textContent === 'Paused' || /^Device is/.test(t.msg.textContent)) setMsg(t, 'Loading...'); loop(t, ++t.gen); }
      else if (!t.running && !t.img.getAttribute('src')) setMsg(t, 'Loading...');
    });
    Object.keys(tiles).forEach(function (id) {
      if (!seen[id]) { stopTile(tiles[id]); grid.removeChild(tiles[id].root); delete tiles[id]; }
    });
    var empty = $('multiEmpty');
    if (!PF.devices.length) {
      if (!empty) { empty = PF.el('div', 'muted', 'No devices to show.'); empty.id = 'multiEmpty'; grid.appendChild(empty); }
    } else if (empty) grid.removeChild(empty);
    markActive();
  }

  function setMulti(on) {
    PF.multi = !!on;
    $('multiToggle').checked = PF.multi;
    $('multiGrid').hidden = !PF.multi;
    $('singleArea').hidden = PF.multi;
    $('pickerToggle').disabled = PF.multi;
    try { localStorage.setItem('pf.multi', PF.multi ? '1' : '0'); } catch (e) { /* ignore */ }
    if (PF.multi) {
      Object.keys(tiles).forEach(function (id) { tiles[id].running = false; });
      sync();
    } else {
      Object.keys(tiles).forEach(function (id) { stopTile(tiles[id]); tiles[id].running = false; });
      PF.restartFrames();
    }
  }

  $('multiToggle').addEventListener('change', function () { setMulti(this.checked); });
  PF.hooks.onDevices.push(function () { if (PF.multi) sync(); });
  PF.hooks.onSelect.push(markActive);
  // pause loops when leaving Automation, resume on return
  document.addEventListener('click', function (e) {
    if (e.target && e.target.classList && e.target.classList.contains('tab')) setTimeout(function () {
      if (!PF.multi) return;
      Object.keys(tiles).forEach(function (id) { tiles[id].running = false; });
      sync();
    }, 0);
  });
  PF.multi = false;
  try { if (localStorage.getItem('pf.multi') === '1') setTimeout(function () { setMulti(true); }, 0); } catch (e) { /* ignore */ }
})();
