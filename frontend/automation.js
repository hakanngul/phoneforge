/*
 * PhoneForge Automation tab: devices, live mirror, element picker, activity, agent.
 *
 * TAP COORDINATE MATH
 *   The mirror <img> uses object-fit: contain, so the bitmap is scaled uniformly
 *   and centered inside the element box, leaving letterbox bars. With box size
 *   (bw, bh) and bitmap size (nw, nh):
 *     scale = min(bw / nw, bh / nh)
 *     renderedW = nw * scale, renderedH = nh * scale
 *     offX = (bw - renderedW) / 2, offY = (bh - renderedH) / 2
 *   For a click at (clientX, clientY) with element rect r:
 *     px = (clientX - r.left - offX) / scale   // device pixel x
 *     py = (clientY - r.top  - offY) / scale
 *   If px/py fall outside [0,nw) x [0,nh) the click was in the letterbox: ignored.
 *   The same transform (inverted) positions the highlight overlay:
 *     left = offX + bounds.x * scale, top = offY + bounds.y * scale, size = w*scale, h*scale.
 *
 * VIDEO MODE (canvas) MAPPING
 *   In video mode the mirror is a <canvas> holding the DOWNSCALED H.264 frame (cw x ch). The device's
 *   real size (dw x dh) comes from one GET /screenshot per device selection (cached in devSizes).
 *   Letterboxing uses the canvas bitmap size, then x_device = x_canvas * dw / cw (same for y), so
 *   taps land on real device pixels. If canvas and cached size disagree on orientation they are swapped
 *   (device rotated since the size was cached). In screenshot mode the <img> natural size IS the device size.
 *
 * GESTURES (pointer events on the mirror)
 *   down/up < 500 ms, moved <= 12 px  -> tap, but delayed 300 ms so a 2nd click can turn it into a double-tap.
 *   2nd press starts <= 300 ms after the 1st release, within 20 px, and is also a quick click -> /double-tap
 *   (the pending single tap is cancelled). Held >= 500 ms without moving -> /long-press {ms: hold time}.
 *   Moved > 12 px -> /swipe (or /drag with Alt) with durationMs = drag time clamped to 80..1500.
 *
 * ELEMENT PICKER
 *   With the picker on, a click maps to device pixel (px,py) as above, then
 *   GET /ui-tree is fetched and walked depth-first. A node "hits" if its bounds
 *   {x,y,w,h} contain the point (x<=px<x+w, y<=py<y+h). We recurse into children
 *   of every hit and keep the deepest hit; among overlapping siblings the later one wins
 *   (later siblings are drawn on top in Android/iOS trees). Nodes with zero-size bounds
 *   are skipped but their children are still examined.
 */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var img = $('mirror'), canvas = $('vmirror'), screenEl = $('screen');
  var frameTimer = null, frameBusy = false, frameGen = 0, lastUrl = null, frameFailures = 0;
  var picked = null;
  var videoActive = false, videoHasFrame = false, mirrorOn = false, fellBack = false;
  var mjpegActive = false, mjpegGot = false, mjpegTimer = null, mjpegAbort = null, mjpegUrl = null, noMjpeg = {}; // iOS: fetch()-parsed MJPEG -> blob URLs swapped into the mirror <img>
  var noVideo = {};          // device id -> true once the stream failed/unsupported (until user re-selects Video)
  var devSizes = {}, devSizeBusy = {};
  var mode = 'video';
  try { mode = localStorage.getItem('pf.mode') === 'screenshots' ? 'screenshots' : 'video'; } catch (e) { /* ignore */ }

  /* ---------- devices ---------- */
  function isRemote(d) { return String(d.id || '').indexOf('remote-') === 0; }
  PF.isRemote = isRemote;
  PF.isIOS = function (d) { return !!d && (d.platform === 'ios' || d.platform === 'simulator'); };
  PF.IOS_NOTICE = 'iOS live control needs the WDA bridge \u2014 not configured';

  function deviceItem(d) {
    var li = PF.el('li', d.id === PF.selectedId ? 'sel' : '');
    var st = String(d.state || '').toLowerCase();
    var dot = PF.el('span', 'dot ' + st); dot.title = d.state;
    var top = PF.el('div');
    top.appendChild(PF.el('strong', '', d.name || d.model || d.id));
    top.appendChild(document.createTextNode(' '));
    top.appendChild(PF.el('span', 'plat ' + (d.platform || ''), d.platform || '?'));
    if (isRemote(d)) top.appendChild(PF.el('span', 'plat remote', 'remote'));
    if (PF.decorateDevice) { try { PF.decorateDevice(d, top); } catch (e) { /* optional */ } }
    li.appendChild(dot); li.appendChild(top);
    li.appendChild(PF.el('div', 'sub', [d.model, d.osVersion, d.state].filter(Boolean).join(' / ')));
    li.onclick = function () { select(d.id); };
    return li;
  }

  // Server order is kept as-is (never sorted client-side). Skip the DOM rebuild when nothing changed.
  var lastSig = null, missCount = 0;
  function renderDevices(force) {
    var sig = JSON.stringify([PF.selectedId, PF.devices.map(function (d) { return [d.id, d.name, d.model, d.osVersion, d.state, d.platform]; })]);
    if (!force && sig === lastSig) return;
    lastSig = sig;
    var ul = $('deviceList'), rl = $('remoteDeviceList');
    ul.innerHTML = ''; rl.innerHTML = '';
    var local = PF.devices.filter(function (d) { return !isRemote(d); });
    var remote = PF.devices.filter(isRemote);
    $('localCount').textContent = local.length;
    $('remoteCount').textContent = remote.length;
    if (!local.length) {
      var li0 = PF.el('li', 'muted', 'No local devices found. Connect a phone with USB debugging / trust enabled.');
      li0.style.cursor = 'default'; li0.style.display = 'block';
      ul.appendChild(li0);
    }
    local.forEach(function (d) { ul.appendChild(deviceItem(d)); });
    if (!remote.length) {
      var li1 = PF.el('li', 'muted', 'No remote devices. Add a node below.');
      li1.style.cursor = 'default'; li1.style.display = 'block';
      rl.appendChild(li1);
    }
    remote.forEach(function (d) { rl.appendChild(deviceItem(d)); });
  }

  function loadDevices() {
    return PF.api('GET', '/devices').then(function (devs) {
      PF.devices = devs || [];
      // A device missing from one refresh (flaky adb) keeps its selection; drop it only after 3 misses in a row.
      if (PF.selectedId && !PF.selectedDevice()) {
        if (++missCount >= 3) { PF.selectedId = null; missCount = 0; onSelectionChange(); }
      } else missCount = 0;
      renderDevices();
      var sd = PF.selectedDevice(), up = !!sd && sd.state === 'connected';
      if (up && !mirrorOn && mirrorWanted()) startFrames();
      else if (!up && mirrorOn) stopMirror();
      updateLiveState();
      PF.hooks.onDevices.forEach(function (f) { try { f(); } catch (e) { /* ignore */ } });
    }).catch(function (e) {
      $('deviceList').innerHTML = '';
      $('deviceList').appendChild(PF.el('li', 'muted', 'Cannot reach server: ' + e.message));
    });
  }

  function select(id) {
    if (id === PF.selectedId) return;
    PF.selectedId = id;
    renderDevices(true);
    onSelectionChange();
  }

  function onSelectionChange() {
    clearPick();
    var d = PF.selectedDevice();
    $('liveTitle').textContent = d ? 'Live - ' + (d.name || d.id) : 'Live';
    frameFailures = 0;
    startFrames();
    PF.hooks.onSelect.forEach(function (f) { f(); });
  }

  function updateLiveState() {
    var d = PF.selectedDevice(), ph = $('noDevice');
    $('iosNotice').hidden = true;
    if (d && d.state === 'connected' && PF.isIOS(d) && frameFailures > 0 && !hasFrame()) {
      ph.hidden = true; $('iosNotice').hidden = false; return;
    }
    if (!d) { ph.textContent = 'Select a device'; ph.hidden = false; return; }
    if (d.state !== 'connected') { ph.textContent = 'Device is ' + d.state; ph.hidden = false; return; }
    if (hasFrame() && frameFailures === 0) ph.hidden = true;
    else if (!hasFrame()) { ph.textContent = videoActive ? 'Connecting to video stream...' : 'Loading screen...'; ph.hidden = false; }
  }

  /* ---------- mirror ---------- */
  function fpsMs() { return Math.round(1000 / (parseInt($('fps').value, 10) || 2)); }

  function hasFrame() { return videoActive ? videoHasFrame : mjpegActive ? mjpegGot : !!img.getAttribute('src'); }
  function mirrorWanted() { return !PF.multi && !$('automation-tab').hidden && !document.hidden; }

  function updateModeUI() {
    Array.prototype.forEach.call(document.querySelectorAll('#modeSwitch button'), function (b) {
      b.classList.toggle('active', b.getAttribute('data-mode') === mode);
    });
    $('fpsWrap').hidden = !(mode === 'screenshots' || fellBack);
    $('modeLabel').hidden = !fellBack;
    screenEl.classList.toggle('vid', videoActive);
  }

  // Stop every kind of mirroring (video stream + screenshot polling).
  function stopMirror() {
    clearTimeout(frameTimer);
    frameGen++;
    PF.video.stop();
    stopMjpeg();
    mirrorOn = false; videoActive = false; videoHasFrame = false;
    if (lastUrl) { URL.revokeObjectURL(lastUrl); lastUrl = null; }
    img.removeAttribute('src');
    statsStop();
    updateModeUI();
  }

  /* ---------- live stats badge (fps / throughput / source) ---------- */
  var stat = null, shotStats = { frames: 0, bytes: 0 };
  function fmtRate(bps) {
    if (bps >= 1048576) return (bps / 1048576).toFixed(1) + ' MB/s';
    if (bps >= 1024) return Math.round(bps / 1024) + ' KB/s';
    return Math.round(bps) + ' B/s';
  }
  function statsRender() {
    var el = $('liveStats');
    if (!stat || stat.fps === null) { el.hidden = true; return; }
    var fps = stat.fps, f = fps < 10 ? fps.toFixed(1) : String(Math.round(fps));
    el.textContent = f + ' fps \u00b7 ' + fmtRate(stat.bps) + ' \u00b7 ' + stat.label;
    el.className = 'lstats ' + (fps >= 24 ? 'ok' : fps >= 12 ? 'warn' : 'bad');
    el.title = 'Live stream: smoothed frames/s, data rate and source (' + stat.label + ')';
    el.hidden = false;
  }
  function statsStop() {
    if (stat) clearInterval(stat.timer);
    stat = null; $('liveStats').hidden = true;
  }
  // read() returns cumulative {frames, bytes}; sampled once a second, exponentially smoothed.
  function statsStart(label, read) {
    statsStop();
    var c = read(), s = stat = { label: label, fps: null, bps: null, f: c.frames, b: c.bytes, timer: null };
    s.timer = setInterval(function () {
      if (stat !== s) return;
      var n = read(), df = Math.max(n.frames - s.f, 0), db = Math.max(n.bytes - s.b, 0);
      s.f = n.frames; s.b = n.bytes;
      s.fps = s.fps === null ? df : s.fps * 0.6 + df * 0.4;
      s.bps = s.bps === null ? db : s.bps * 0.6 + db * 0.4;
      statsRender();
    }, 1000);
  }
  function statsLabel(l) { if (stat) { stat.label = l; statsRender(); } }

  function stopMjpeg() {
    clearTimeout(mjpegTimer); mjpegTimer = null;
    if (mjpegAbort) { try { mjpegAbort.abort(); } catch (e) { /* ignore */ } mjpegAbort = null; }
    img.onerror = null; img.onload = null;
    if (mjpegUrl) { URL.revokeObjectURL(mjpegUrl); mjpegUrl = null; }
    mjpegActive = false; mjpegGot = false;
  }

  function startFrames() {
    stopMirror();
    fellBack = false;
    updateModeUI();
    updateLiveState();
    var d = PF.selectedDevice();
    if (!mirrorWanted() || !d || d.state !== 'connected') return; // multi-window drives its own streams
    ensureDevSize();
    if (mode === 'video' && PF.isIOS(d) && PF.iosBridge && PF.iosBridge.running(d.id) && !noMjpeg[d.id]) startMjpeg(d.id);
    else if (mode === 'video' && PF.video.supported && !PF.isIOS(d) && !noVideo[d.id]) startVideo(d.id);
    else {
      fellBack = mode === 'video';
      if (fellBack) $('modeLabel').title = PF.isIOS(d) ? 'Start the WDA bridge for live video' : PF.video.supported ? 'Video unavailable for this device' : 'WebCodecs not supported by this browser';
      startShots();
    }
  }
  PF.restartFrames = startFrames;

  function startShots() {
    clearTimeout(frameTimer);
    frameGen++;
    PF.video.stop();
    if (mjpegActive) { stopMjpeg(); img.removeAttribute('src'); }
    videoActive = false; videoHasFrame = false; mirrorOn = true;
    shotStats = { frames: 0, bytes: 0 };
    statsStart('Screenshots', function () { return shotStats; });
    updateModeUI(); updateLiveState();
    tick(frameGen);
  }

  // iOS live view: the server re-emits WDA's (or the AV capture) MJPEG stream. It is read with fetch(), split into
  // JPEG frames by PF.mjpegParser and shown by swapping blob URLs into the mirror <img> (latest frame wins: frames
  // arriving while a decode is pending replace the queued one). Geometry code keeps using the img's natural size.
  function startMjpeg(id) {
    var gen = ++frameGen, t0 = Date.now(), ac = mjpegAbort = new AbortController();
    var mj = { frames: 0, bytes: 0 }, pending = null, rendering = false, fails = 0, gotAny = false;
    mjpegActive = true; mjpegGot = false; videoActive = false; videoHasFrame = false; mirrorOn = true;
    statsStart('WDA', function () { return mj; });
    updateModeUI(); updateLiveState();
    function live() { return gen === frameGen && id === PF.selectedId && !ac.signal.aborted; }
    function fail(reason) {
      if (gen !== frameGen || id !== PF.selectedId) return;
      noMjpeg[id] = true;
      PF.addLog({ level: 'info', message: 'MJPEG stream unavailable, using screenshots: ' + reason });
      fellBack = true; $('modeLabel').title = reason;
      startShots();
    }
    function render() {
      if (rendering || !pending || !live()) return;
      var blob = pending; pending = null; rendering = true;
      var u = URL.createObjectURL(blob);
      img.onload = function () {
        if (!live()) { URL.revokeObjectURL(u); return; }
        if (mjpegUrl) URL.revokeObjectURL(mjpegUrl);
        mjpegUrl = u; mj.frames++; rendering = false;
        if (!mjpegGot) {
          mjpegGot = true; frameFailures = 0;
          $('noDevice').hidden = true; $('iosNotice').hidden = true;
          if (picked) drawHighlight();
        }
        render();
      };
      img.onerror = function () { URL.revokeObjectURL(u); rendering = false; render(); }; // skip one bad frame
      img.src = u;
    }
    function onJpeg(bytes) {
      gotAny = true; fails = 0;
      pending = new Blob([bytes], { type: 'image/jpeg' });
      render();
    }
    function reconnect(why) {
      if (!live()) return;
      if (!gotAny && ++fails >= 3) { fail(why); return; }
      gotAny = false;
      setTimeout(connect, 1000);
    }
    function connect() {
      if (!live()) return;
      fetch(PF.API + PF.dev('/mjpeg') + '?format=raw&t=' + Date.now(), { signal: ac.signal, cache: 'no-store' }).then(function (res) {
        if (!live()) return;
        if (!res.ok) throw new Error('HTTP ' + res.status);
        if (!res.body || !res.body.getReader) throw new Error('streaming body not supported');
        var src = res.headers.get('X-Stream-Source');
        if (src) statsLabel(src === 'av' ? 'Native' : 'WDA');
        else PF.api('GET', PF.dev('/mjpeg/info')).then(function (inf) { if (live() && inf && inf.source) statsLabel(inf.source === 'av' ? 'Native' : 'WDA'); }).catch(function () { /* optional */ });
        var m = /boundary=("?)([^";\s]+)\1/i.exec(res.headers.get('Content-Type') || '');
        var bnd = res.headers.get('X-Boundary') || (m ? m[2] : null);
        var parser = PF.mjpegParser(bnd, onJpeg), reader = res.body.getReader();
        function pump() {
          return reader.read().then(function (r) {
            if (!live()) { try { reader.cancel(); } catch (e) { /* ignore */ } return; }
            if (r.done) { reconnect('stream ended'); return; }
            if (r.value && r.value.length) { mj.bytes += r.value.length; parser.feed(r.value); }
            return pump();
          });
        }
        return pump();
      }).catch(function (e) {
        if (!live() || (e && e.name === 'AbortError')) return;
        reconnect('stream error: ' + (e && e.message));
      });
    }
    mjpegTimer = setTimeout(function () { if (gen === frameGen && !mjpegGot) fail('no frames within 10 s'); }, 10000);
    connect();
  }

  // Re-evaluate the mirror source (e.g. the iOS WDA bridge just started or stopped).
  PF.iosBridgeChanged = function (id) {
    delete devSizes[id]; devSizeBusy[id] = false; // logical size may differ with/without WDA
    if (id !== PF.selectedId) return;
    noMjpeg[id] = false; frameFailures = 0;
    startFrames();
  };

  function startVideo(id) {
    videoActive = true; videoHasFrame = false; mirrorOn = true;
    statsStart('H.264', PF.video.stats);
    updateModeUI(); updateLiveState();
    PF.video.start({
      url: PF.API + PF.dev('/stream.h264'),
      canvas: canvas,
      onFrame: function () {
        if (id !== PF.selectedId) return;
        videoHasFrame = true; frameFailures = 0;
        $('noDevice').hidden = true; $('iosNotice').hidden = true;
        if (picked) drawHighlight();
      },
      onFallback: function (reason) {
        if (id !== PF.selectedId) return;
        noVideo[id] = true;
        PF.addLog({ level: 'info', message: 'video stream unavailable, using screenshots: ' + reason });
        fellBack = true; $('modeLabel').title = reason;
        startShots();
      }
    });
  }

  /* One GET /screenshot per device selection gives the real device resolution (the video is downscaled). */
  function ensureDevSize() {
    var id = PF.selectedId;
    if (!id || devSizes[id] || devSizeBusy[id]) return;
    devSizeBusy[id] = true;
    if (PF.isIOS(PF.selectedDevice())) {
      // iOS screenshots are 2x/3x pixels; taps and bounds use logical points (X-Screen-Width/Height).
      fetch(PF.API + PF.dev('/screenshot') + '?t=' + Date.now(), { cache: 'no-store' }).then(function (res) {
        var w = parseInt(res.headers.get('X-Screen-Width'), 10), h = parseInt(res.headers.get('X-Screen-Height'), 10);
        if (res.ok && w > 0 && h > 0) { devSizes[id] = { w: w, h: h }; devSizeBusy[id] = false; if (picked && id === PF.selectedId) drawHighlight(); return; }
        setTimeout(function () { devSizeBusy[id] = false; }, 5000);
      }).catch(function () { setTimeout(function () { devSizeBusy[id] = false; }, 5000); });
      return;
    }
    PF.api('GET', PF.dev('/screenshot') + '?t=' + Date.now(), undefined, 'blob').then(function (blob) {
      var u = URL.createObjectURL(blob), im = new Image();
      im.onload = function () {
        if (im.naturalWidth) devSizes[id] = { w: im.naturalWidth, h: im.naturalHeight };
        URL.revokeObjectURL(u); devSizeBusy[id] = false;
        if (picked && id === PF.selectedId) drawHighlight();
      };
      im.onerror = function () { URL.revokeObjectURL(u); setTimeout(function () { devSizeBusy[id] = false; }, 5000); };
      im.src = u;
    }).catch(function () { setTimeout(function () { devSizeBusy[id] = false; }, 5000); });
  }

  function tick(gen) {
    if (gen !== frameGen || PF.multi || videoActive || mjpegActive) return;
    var d = PF.selectedDevice();
    if (!d || d.state !== 'connected') { mirrorOn = false; return; }
    var t0 = Date.now();
    frameBusy = true;
    PF.api('GET', PF.dev('/screenshot') + '?t=' + t0, undefined, 'blob').then(function (blob) {
      if (gen !== frameGen) return;
      var u = URL.createObjectURL(blob);
      img.src = u;
      if (lastUrl) URL.revokeObjectURL(lastUrl);
      lastUrl = u;
      shotStats.frames++; shotStats.bytes += blob.size;
      frameFailures = 0;
      $('noDevice').hidden = true; $('iosNotice').hidden = true;
      if (picked) drawHighlight();
    }).catch(function (e) {
      if (gen !== frameGen) return;
      frameFailures++;
      if (PF.isIOS(d)) {
        // iOS without the WDA bridge: show a notice instead of a broken image / toast spam.
        $('iosNotice').querySelector('.detail').textContent = e.message;
        if (!img.getAttribute('src')) { $('noDevice').hidden = true; $('iosNotice').hidden = false; }
        return;
      }
      if (frameFailures === 1 || frameFailures % 10 === 0) PF.toast('Screenshot failed: ' + e.message);
      if (!img.getAttribute('src')) { $('noDevice').textContent = 'Screenshot unavailable'; $('noDevice').hidden = false; }
    }).then(function () {
      frameBusy = false;
      if (gen !== frameGen) return;
      var backoff = Math.min(frameFailures, 5) * 1000;
      frameTimer = setTimeout(function () { tick(gen); }, Math.max(fpsMs() - (Date.now() - t0), 0) + backoff);
    });
  }

  function refreshSoon() { setTimeout(function () { if (videoActive || mjpegActive || !mirrorOn) return; if (!frameBusy) { clearTimeout(frameTimer); tick(frameGen); } }, 350); }

  /* Geometry of the active mirror element. cw/ch = bitmap shown (video frame or screenshot),
     dw/dh = real device resolution; kx/ky convert device px -> css px. null if unknown. */
  function activeEl() { return videoActive ? canvas : img; }
  function useSizeCache() { return videoActive || PF.isIOS(PF.selectedDevice()); }
  function metrics() {
    var el = activeEl(), r = el.getBoundingClientRect();
    var cw = videoActive ? canvas.width : img.naturalWidth, ch = videoActive ? canvas.height : img.naturalHeight;
    if (!cw || !ch || !r.width || !r.height) return null;
    var dw = cw, dh = ch;
    if (useSizeCache()) {
      var ds = devSizes[PF.selectedId];
      if (!ds && videoActive) return null;
      if (ds) {
        dw = ds.w; dh = ds.h;
        if ((cw > ch) !== (dw > dh)) { dw = ds.h; dh = ds.w; } // rotated since size was cached
      }
    }
    var scale = Math.min(r.width / cw, r.height / ch);
    return { r: r, cw: cw, ch: ch, dw: dw, dh: dh, scale: scale,
      kx: scale * cw / dw, ky: scale * ch / dh,
      offX: (r.width - cw * scale) / 2, offY: (r.height - ch * scale) / 2 };
  }
  // client coords -> device pixels; null in the letterbox unless clamp is true.
  function toDevice(ev, clamp) {
    var m = metrics(); if (!m) return null;
    var cx = (ev.clientX - m.r.left - m.offX) / m.scale;
    var cy = (ev.clientY - m.r.top - m.offY) / m.scale;
    var px = cx * m.dw / m.cw, py = cy * m.dh / m.ch;
    if (px < 0 || py < 0 || px >= m.dw || py >= m.dh) {
      if (!clamp) return null;
      px = Math.min(Math.max(px, 0), m.dw - 1); py = Math.min(Math.max(py, 0), m.dh - 1);
    }
    return { x: Math.round(px), y: Math.round(py) };
  }

  function sendGesture(path, body) {
    if (!PF.needDevice()) return;
    PF.guardDev(PF.api('POST', PF.dev(path), body).then(refreshSoon));
  }
  function doTap(x, y) { sendGesture('/tap', { x: x, y: y }); }

  /* ---------- element picker ---------- */
  function contains(b, x, y) { return b && b.w > 0 && b.h > 0 && x >= b.x && x < b.x + b.w && y >= b.y && y < b.y + b.h; }
  function deepest(node, x, y) {
    var best = contains(node.bounds, x, y) ? node : null, kids = node.children || [];
    for (var i = 0; i < kids.length; i++) {
      var c = deepest(kids[i], x, y);
      // Deeper hit replaces the parent; for sibling hits the later one (on top) wins.
      if (c) best = c;
    }
    return best;
  }

  function selectorFor(el) {
    if (el.resourceId) return { resourceId: el.resourceId };
    if (el.text) return { text: el.text };
    if (el.label) return { label: el.label };
    return { role: el.role };
  }

  function drawHighlight() {
    var hl = $('hl'), m = metrics();
    if (!picked || !m) { hl.hidden = true; return; }
    var b = picked.bounds, sr = screenEl.getBoundingClientRect();
    hl.style.left = (m.r.left - sr.left + m.offX + b.x * m.kx) + 'px';
    hl.style.top = (m.r.top - sr.top + m.offY + b.y * m.ky) + 'px';
    hl.style.width = Math.max(b.w * m.kx, 2) + 'px';
    hl.style.height = Math.max(b.h * m.ky, 2) + 'px';
    hl.hidden = false;
  }

  function clearPick() { picked = null; $('hl').hidden = true; $('inspector').hidden = true; }

  function showInspector(el) {
    var box = $('inspector'); box.innerHTML = ''; box.hidden = false;
    var b = el.bounds, dl = PF.el('dl');
    [['role', el.role], ['text', el.text], ['label', el.label], ['resourceId', el.resourceId],
     ['bounds', '[' + [b.x, b.y, b.w, b.h].join(', ') + ']'],
     ['flags', ['clickable', 'enabled', 'focused'].filter(function (k) { return el[k]; }).join(', ')]
    ].forEach(function (p) {
      if (!p[1]) return;
      dl.appendChild(PF.el('dt', '', p[0])); dl.appendChild(PF.el('dd', '', p[1]));
    });
    box.appendChild(PF.el('h2', '', 'Inspector'));
    box.appendChild(dl);
    var acts = PF.el('div', 'acts');
    var tap = PF.el('button', 'btn sm primary', 'Tap this');
    tap.onclick = function () { doTap(Math.round(b.x + b.w / 2), Math.round(b.y + b.h / 2)); };
    var cp = PF.el('button', 'btn sm', 'Copy selector');
    cp.onclick = function () {
      var s = JSON.stringify(selectorFor(el));
      var done = function () { PF.toast('Copied ' + s, true); };
      if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(s).then(done, function () { fallbackCopy(s); done(); });
      else { fallbackCopy(s); done(); }
    };
    var cl = PF.el('button', 'btn sm', 'Close');
    cl.onclick = clearPick;
    acts.appendChild(tap); acts.appendChild(cp); acts.appendChild(cl);
    box.appendChild(acts);
  }
  function fallbackCopy(s) { PF.copyFallback(s); }

  function pick(p) {
    PF.api('GET', PF.dev('/ui-tree')).then(function (tree) {
      var el = tree && deepest(tree, p.x, p.y);
      if (!el) { PF.toast('No element at ' + p.x + ',' + p.y, true); clearPick(); return; }
      picked = el; drawHighlight(); showInspector(el);
    }).catch(function (e) { PF.toast(e.message); });
  }

  /* ---------- pointer gestures ---------- */
  var LONG_MS = 500, DBL_MS = 300, MOVE_PX = 12, DBL_PX = 20;
  var down = null, pend = null; // active press / single tap waiting to see if it becomes a double-tap
  function dist(ax, ay, bx, by) { var dx = ax - bx, dy = ay - by; return Math.sqrt(dx * dx + dy * dy); }
  function clamp(v, lo, hi) { return Math.min(Math.max(v, lo), hi); }

  function flushPend(send) {
    var q = pend; if (!q) return;
    clearTimeout(q.timer); pend = null;
    if (send && q.devId === PF.selectedId) doTap(q.p.x, q.p.y);
  }

  function onDown(ev) {
    if (ev.pointerType === 'mouse' && ev.button !== 0) return;
    if (!PF.needDevice()) return;
    var p = toDevice(ev, false);
    if (!p) {
      if (videoActive && !devSizes[PF.selectedId]) PF.toast('Device size not known yet, try again in a moment', true);
      return; // letterbox or not loaded
    }
    ev.preventDefault();
    try { ev.currentTarget.setPointerCapture(ev.pointerId); } catch (e) { /* ignore */ }
    down = { t: performance.now(), cx: ev.clientX, cy: ev.clientY, p: p, alt: ev.altKey, moved: false, id: ev.pointerId, devId: PF.selectedId };
    if (pend) { clearTimeout(pend.timer); pend.held = true; } // second press: wait for its release
  }
  function onMove(ev) {
    if (!down || ev.pointerId !== down.id) return;
    if (ev.altKey) down.alt = true;
    if (!down.moved && dist(ev.clientX, ev.clientY, down.cx, down.cy) > MOVE_PX) down.moved = true;
  }
  function onCancel(ev) {
    if (!down || ev.pointerId !== down.id) return;
    down = null; flushPend(true);
  }
  function onUp(ev) {
    if (!down || ev.pointerId !== down.id) return;
    var d = down; down = null;
    var dur = performance.now() - d.t;
    if (d.devId !== PF.selectedId) { flushPend(false); return; }
    if ($('pickerToggle').checked) { flushPend(false); if (!d.moved) pick(d.p); return; }
    if (d.moved) {
      flushPend(true);
      var e = toDevice(ev, true) || d.p;
      sendGesture((d.alt || ev.altKey) ? '/drag' : '/swipe',
        { x1: d.p.x, y1: d.p.y, x2: e.x, y2: e.y, durationMs: Math.round(clamp(dur, 80, 1500)) });
      return;
    }
    if (dur >= LONG_MS) {
      flushPend(true);
      sendGesture('/long-press', { x: d.p.x, y: d.p.y, ms: Math.round(dur) });
      return;
    }
    // quick click
    if (pend && pend.held && dist(pend.cx, pend.cy, d.cx, d.cy) <= DBL_PX) {
      var first = pend.p; pend = null;
      sendGesture('/double-tap', { x: first.x, y: first.y });
      return;
    }
    flushPend(true);
    pend = { p: d.p, cx: d.cx, cy: d.cy, devId: d.devId, held: false, timer: setTimeout(function () {
      var q = pend; pend = null;
      if (q && q.devId === PF.selectedId) doTap(q.p.x, q.p.y);
    }, DBL_MS) };
  }
  [img, canvas].forEach(function (el) {
    el.addEventListener('pointerdown', onDown);
    el.addEventListener('pointermove', onMove);
    el.addEventListener('pointerup', onUp);
    el.addEventListener('pointercancel', onCancel);
    el.addEventListener('dragstart', function (e) { e.preventDefault(); });
  });
  /* hover inspector: cached UI tree, outline + tooltip under the cursor while picking */
  var hoverBox = PF.el('div', 'hl-hover'), hoverTip = PF.el('div', 'hl-tip');
  hoverBox.hidden = true; hoverTip.hidden = true;
  screenEl.appendChild(hoverBox); screenEl.appendChild(hoverTip);
  var treeCache = null, treeAt = 0, treeBusy = false;
  function hideHover() { hoverBox.hidden = true; hoverTip.hidden = true; }
  function loadTree() {
    if (treeBusy || !PF.selectedId) return;
    treeBusy = true; var id = PF.selectedId;
    PF.api('GET', PF.dev('/ui-tree')).then(function (t) { if (id === PF.selectedId) { treeCache = t; treeAt = Date.now(); } })
      .catch(function () { /* ignore */ }).then(function () { treeBusy = false; });
  }
  function onHover(ev) {
    if (!$('pickerToggle').checked) return;
    if (!treeCache || Date.now() - treeAt > 3000) loadTree();
    var m = metrics(), p = m && toDevice(ev, false), el = p && treeCache && deepest(treeCache, p.x, p.y);
    if (!el) { hideHover(); return; }
    var b = el.bounds, sr = screenEl.getBoundingClientRect();
    var l = m.r.left - sr.left + m.offX + b.x * m.kx, t = m.r.top - sr.top + m.offY + b.y * m.ky;
    hoverBox.style.left = l + 'px'; hoverBox.style.top = t + 'px';
    hoverBox.style.width = Math.max(b.w * m.kx, 2) + 'px'; hoverBox.style.height = Math.max(b.h * m.ky, 2) + 'px';
    hoverBox.hidden = false;
    var txt = el.text || el.label || el.resourceId || '';
    hoverTip.textContent = (el.role || 'element') + (txt ? ' \u2022 ' + txt : '') + (el.resourceId && txt !== el.resourceId ? ' \u2022 ' + el.resourceId : '');
    hoverTip.hidden = false;
    var tw = hoverTip.offsetWidth, th = hoverTip.offsetHeight, W = screenEl.clientWidth, H = screenEl.clientHeight, bh = b.h * m.ky, bw = b.w * m.kx;
    var ty = t - th - 4;                       // above
    if (ty < 0) ty = t + bh + 4;               // below
    if (ty + th > H) ty = Math.max(0, Math.min(t + 2, H - th - 2)); // inside the box / clamp
    var tx = Math.max(0, Math.min(l, W - tw - 2));
    hoverTip.style.left = tx + 'px';
    hoverTip.style.top = Math.max(0, Math.min(ty, H - th - 2)) + 'px';
  }
  [img, canvas].forEach(function (el) {
    el.addEventListener('pointermove', onHover);
    el.addEventListener('pointerleave', hideHover);
  });
  function setPicker(on) {
    $('pickerToggle').checked = on;
    screenEl.classList.toggle('picker', on);
    if (!on) { clearPick(); hideHover(); treeCache = null; } else loadTree();
  }
  $('pickerToggle').addEventListener('change', function () { setPicker(this.checked); });
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Escape' || !$('pickerToggle').checked) return;
    var t = e.target; if (t && /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName) && t !== $('pickerToggle')) return;
    setPicker(false);
  });
  window.addEventListener('resize', function () { if (picked) drawHighlight(); });

  /* ---------- toolbar ---------- */
  $('fps').addEventListener('change', function () { clearTimeout(frameTimer); if (!frameBusy && !videoActive && mirrorOn) tick(frameGen); });
  Array.prototype.forEach.call(document.querySelectorAll('#modeSwitch button'), function (b) {
    b.addEventListener('click', function () {
      var m = b.getAttribute('data-mode');
      if (m === 'video') noVideo = {}; // explicit retry
      if (m === mode && m === 'screenshots') return;
      mode = m;
      try { localStorage.setItem('pf.mode', mode); } catch (e) { /* ignore */ }
      startFrames();
    });
  });
  // Stop streaming when the tab is hidden or the Automation tab is left; resume on return.
  function syncWanted() {
    var w = mirrorWanted();
    if (!w && mirrorOn) stopMirror();
    else if (w && !mirrorOn) startFrames();
  }
  /* Show details toggle: body.hide-details hides observation / internal / duplicate rows instantly. */
  (function () {
    var cb = $('agentDetails'), on = false;
    try { on = localStorage.getItem('pf.agentDetails') === '1'; } catch (e) {}
    function apply() { document.body.classList.toggle('hide-details', !cb.checked); }
    cb.checked = on; apply();
    cb.addEventListener('change', function () { apply(); try { localStorage.setItem('pf.agentDetails', cb.checked ? '1' : '0'); } catch (e) {} });
  })();
  document.addEventListener('visibilitychange', syncWanted);
  document.addEventListener('click', function (e) {
    if (e.target && e.target.classList && e.target.classList.contains('tab')) setTimeout(syncWanted, 0);
  });
  $('multiToggle').addEventListener('change', function () { setTimeout(syncWanted, 0); });
  Array.prototype.forEach.call(document.querySelectorAll('[data-key]'), function (b) {
    b.addEventListener('click', function () {
      if (!PF.needDevice()) return;
      PF.guardDev(PF.api('POST', PF.dev('/press-key'), { key: b.getAttribute('data-key') }).then(refreshSoon));
    });
  });
  function doType() {
    if (!PF.needDevice()) return;
    var t = $('typeText').value; if (!t) return;
    PF.guardDev(PF.api('POST', PF.dev('/type'), { text: t }).then(function () { $('typeText').value = ''; refreshSoon(); }));
  }
  $('typeBtn').addEventListener('click', doType);
  $('typeText').addEventListener('keydown', function (e) { if (e.key === 'Enter') doType(); });
  function swipe(dir) {
    if (!PF.needDevice()) return;
    var m = metrics(); if (!m) { PF.toast('No screen loaded yet', true); return; }
    var x = Math.round(m.dw / 2), a = Math.round(m.dh * 0.75), b = Math.round(m.dh * 0.25);
    var body = dir === 'up' ? { x1: x, y1: a, x2: x, y2: b, durationMs: 300 } : { x1: x, y1: b, x2: x, y2: a, durationMs: 300 };
    sendGesture('/swipe', body);
  }
  $('swipeUp').addEventListener('click', function () { swipe('up'); });
  $('swipeDown').addEventListener('click', function () { swipe('down'); });
  $('refreshDevices').addEventListener('click', loadDevices);

  /* ---------- activity (SSE) ---------- */
  var es = null, retry = 1000;
  function setLive(on, text) { var l = $('live'); l.className = 'live' + (on ? '' : ' off'); l.textContent = (on ? '● ' : '○ ') + text; }
  function addLog(ev) {
    var log = $('log'), line = PF.el('div', String(ev.level || 'info').toLowerCase());
    var msg0 = String(ev.message || '');
    if (/^agent observation\b/i.test(msg0)) line.classList.add('row-obs');
    else if (/^agent action: ?ToolSearch\b/i.test(msg0)) line.classList.add('row-internal');
    var t = ev.time ? new Date(ev.time) : new Date();
    line.appendChild(PF.el('span', 't', isNaN(t) ? String(ev.time) : t.toLocaleTimeString()));
    line.appendChild(PF.el('span', 'l', ev.level || 'info'));
    line.appendChild(document.createTextNode(ev.message || ''));
    var stick = log.scrollTop + log.clientHeight >= log.scrollHeight - 20;
    log.appendChild(line);
    while (log.childNodes.length > 500) log.removeChild(log.firstChild);
    if (stick) log.scrollTop = log.scrollHeight;
  }
  PF.addLog = addLog;
  function connectEvents() {
    if (!window.EventSource) { setLive(false, 'unsupported'); return; }
    es = new EventSource(PF.API + '/events');
    es.onopen = function () { setLive(true, 'live'); retry = 1000; };
    es.onmessage = function (m) {
      try { addLog(JSON.parse(m.data)); } catch (e) { addLog({ level: 'info', message: m.data }); }
    };
    es.onerror = function () {
      setLive(false, 'disconnected');
      es.close();
      // 404 (not wired) is indistinguishable from a drop here; back off up to 30s.
      if (retry >= 4000 && !connectEvents.warned) { connectEvents.warned = true; addLog({ level: 'error', message: 'Activity stream unavailable (will keep retrying)' }); }
      setTimeout(connectEvents, retry);
      retry = Math.min(retry * 2, 30000);
    };
  }

  /* ---------- agent (fetch + ReadableStream SSE) ---------- */
  var agentAbort = null;
  var lastThought = '';
  var agentSink = null; // optional {line(text), done()} receiving a plain-text copy of the stream (AI Configuration console)
  function sinkText(ev) {
    var t = '[' + String(ev.type || 'info') + '] ' + (ev.text || ev.message || '');
    if (ev.tool) t += ' ' + ev.tool + (ev.args !== undefined ? ' ' + JSON.stringify(ev.args) : '');
    if (ev.result !== undefined && ev.result !== null) t += ' -> ' + (typeof ev.result === 'string' ? ev.result : JSON.stringify(ev.result)).slice(0, 300);
    return t;
  }
  var INTERNAL_TOOLS = /^(ToolSearch)$/i;
  function shortVal(v) { var t = typeof v === 'string' ? v : JSON.stringify(v); t = String(t == null ? '' : t); return t.length > 60 ? t.slice(0, 57) + '\u2026' : t; }
  function compactArgs(args) {
    if (!args || typeof args !== 'object') return args === undefined || args === null ? '' : shortVal(args);
    if (Array.isArray(args.actions) && args.actions.length) {
      var a0 = args.actions[0] || {}, rest = args.actions.length > 1 ? ' (+' + (args.actions.length - 1) + ')' : '';
      return (a0.type || a0.action || 'dsl') + ' ' + compactArgs(Object.keys(a0).reduce(function (o, k) { if (k !== 'type' && k !== 'action') o[k] = a0[k]; return o; }, {})) + rest;
    }
    var keys = ['bundleId', 'bundle_id', 'appId', 'app_id', 'package', 'app', 'url', 'text', 'query', 'label', 'name', 'id', 'predicate', 'direction', 'key', 'path'];
    for (var i = 0; i < keys.length; i++) if (args[keys[i]] !== undefined && args[keys[i]] !== '') return shortVal(args[keys[i]]);
    if (args.x !== undefined && args.y !== undefined) return args.x + ',' + args.y;
    var ks = Object.keys(args);
    for (var j = 0; j < ks.length; j++) { var v = args[ks[j]]; if (typeof v === 'string' || typeof v === 'number') return shortVal(v); }
    return '';
  }
  function stepLine(ev) {
    var type = String(ev.type || 'info').toLowerCase();
    var row = PF.el('div', 'st ' + type);
    if (type === 'observation') row.classList.add('row-obs');
    if (type === 'action' && ev.tool && INTERNAL_TOOLS.test(ev.tool)) row.classList.add('row-internal');
    if (type === 'done' && ev.text && ev.text === lastThought) row.classList.add('row-dup');
    if (type === 'thought' && ev.text) lastThought = ev.text;
    row.appendChild(PF.el('span', 'tag', type));
    if (type === 'action' && ev.tool) {
      var ca = compactArgs(ev.args);
      row.appendChild(PF.el('span', 'compact', '\u25b8 ' + ev.tool + (ca ? ' ' + ca : '')));
      var raw = PF.el('span', 'raw');
      raw.appendChild(PF.el('code', '', ev.tool + (ev.args !== undefined ? ' ' + JSON.stringify(ev.args) : '')));
      row.appendChild(raw);
      return row;
    }
    var txt = ev.text || ev.message || '';
    if (txt) row.appendChild(document.createTextNode(txt + ' '));
    if (ev.tool) { row.appendChild(PF.el('code', '', ev.tool + (ev.args !== undefined ? ' ' + JSON.stringify(ev.args) : ''))); }
    if (ev.result !== undefined && ev.result !== null) {
      var r = typeof ev.result === 'string' ? ev.result : JSON.stringify(ev.result);
      row.appendChild(document.createTextNode(' \u2192 ')); row.appendChild(PF.el('code', '', r.slice(0, 300)));
    }
    return row;
  }
  function tAdd(node) { var t = $('transcript'); t.hidden = false; t.appendChild(node); t.scrollTop = t.scrollHeight; }
  function tErr(msg) { tAdd(stepLine({ type: 'error', text: msg })); if (agentSink) agentSink.line('[error] ' + msg); }
  var NO_LLM_MSG = 'No LLM provider configured for the built-in agent. Set ANTHROPIC_API_KEY or run Ollama, then restart PhoneForge \u2014 or select an external agent such as Claude Code in Integrations \u2192 AI Configuration.';
  function noLLM(s) { return /no llm provider|not configured|ANTHROPIC_API_KEY/i.test(s); }
  function handleAgentEvent(ev) {
    var s = JSON.stringify(ev);
    if (ev.type === 'error' && noLLM(s)) {
      tErr(NO_LLM_MSG);
      return;
    }
    tAdd(stepLine(ev));
    if (agentSink) agentSink.line(sinkText(ev));
    PF.addLog({ level: ev.type === 'error' ? 'error' : ev.type === 'done' ? 'success' : 'info', message: 'agent ' + ev.type + ': ' + (ev.text || ev.tool || '') });
  }
  function parseChunk(chunk) {
    chunk.split('\n').forEach(function (line) {
      line = line.trim();
      if (line.indexOf('data:') === 0) line = line.slice(5).trim();
      else if (line.charAt(0) !== '{') return;
      if (!line) return;
      try { handleAgentEvent(JSON.parse(line)); } catch (e) { tAdd(stepLine({ type: 'observation', text: line })); }
    });
  }

  function runAgent(task, sink) {
    if (!PF.needDevice()) { if (sink) sink.done(); return; }
    var d = PF.selectedDevice();
    if (d && d.state !== 'connected') { PF.toast('Device is ' + d.state); if (sink) sink.done(); return; }
    agentSink = sink || null;
    tAdd(stepLine({ type: 'thought', text: 'Task: ' + task }));
    $('agentSend').disabled = true; $('agentStop').hidden = false;
    agentAbort = new AbortController();
    fetch(PF.API + PF.dev('/agent/run'), {
      method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
      body: JSON.stringify({ task: task, maxSteps: 15 }), signal: agentAbort.signal
    }).then(function (res) {
      if (!res.ok) {
        return res.text().then(function (t) {
          if (res.status === 404) tErr('Agent endpoint not available on this server yet (404).');
          else if (noLLM(t) || res.status === 503) tErr(NO_LLM_MSG + ' (' + PF.errMsg(res, t) + ')');
          else tErr('Agent failed: ' + PF.errMsg(res, t));
        });
      }
      var reader = res.body.getReader(), dec = new TextDecoder(), buf = '';
      function pump() {
        return reader.read().then(function (r) {
          if (r.done) { if (buf.trim()) parseChunk(buf); return; }
          buf += dec.decode(r.value, { stream: true });
          var idx;
          while ((idx = buf.indexOf('\n\n')) >= 0) { parseChunk(buf.slice(0, idx)); buf = buf.slice(idx + 2); }
          return pump();
        });
      }
      return pump();
    }).catch(function (e) {
      if (e.name === 'AbortError') tErr('Stopped.'); else tErr('Agent error: ' + e.message);
    }).then(function () {
      agentAbort = null; $('agentSend').disabled = false; $('agentStop').hidden = true; refreshSoon();
      var sk = agentSink; agentSink = null;
      if (sk) sk.done();
    });
  }


  /* ---------- agent selection (AI Configuration) + external agent runs ---------- */
  var aiSettings = null, extRun = null; // extRun: {id, es, cancelled}
  var AGENT_LABELS = { builtin: 'Built-in agent', claude: 'Claude Code', codex: 'Codex', gemini: 'Gemini CLI', cursor: 'Cursor' };
  function loadAISettings() {
    return PF.api('GET', '/ai/settings').then(function (st) { aiSettings = st; renderAgentUI(); return st; },
      function () { return aiSettings; });
  }
  PF.refreshAgentSettings = loadAISettings;
  function selAgent() { return (aiSettings && aiSettings.automation && aiSettings.automation.agent) || 'builtin'; }
  function renderAgentUI() {
    var a = selAgent(), ext = a !== 'builtin';
    var lbl = AGENT_LABELS[a] || a;
    $('agentWho').textContent = lbl;
    $('agentInput').placeholder = 'Describe what you want ' + lbl + ' to do…';
    var m = (aiSettings && aiSettings.automation && aiSettings.automation.model) || '';
    $('agentModel').hidden = !(a === 'claude');
    $('agentModelText').hidden = !(ext && a !== 'claude');
    $('agentModel').value = m || 'haiku';
    if ($('agentModel').value !== (m || 'haiku')) $('agentModel').value = 'default';
    $('agentModelText').value = a === 'claude' ? '' : m;
  }
  function saveModel(v) {
    PF.api('GET', '/ai/settings').then(function (st) {
      st.automation = st.automation || {}; st.automation.model = v;
      return PF.api('PUT', '/ai/settings', st);
    }).then(function (st) { aiSettings = st; renderAgentUI(); if (PF.refreshAIConfigModel) PF.refreshAIConfigModel(); },
      function (e) { PF.toast(e.message); renderAgentUI(); });
  }
  $('agentModel').addEventListener('change', function () { saveModel(this.value); });
  $('agentModelText').addEventListener('change', function () { saveModel(this.value.trim()); });

  /* Chat sessions: one Claude Code session id per device, used with --resume for follow-ups. */
  var chatSessions = {};
  function chatKey() { return PF.selectedId || ''; }
  $('agentNewChat').addEventListener('click', function () {
    delete chatSessions[chatKey()]; lastThought = '';
    var t = $('transcript'); t.innerHTML = ''; t.hidden = true;
    PF.toast('New chat started', true);
  });
  document.addEventListener('visibilitychange', function () { if (!document.hidden) loadAISettings(); });

  function toolResultText(c) {
    if (typeof c === 'string') return c;
    if (Array.isArray(c)) return c.map(function (x) { return x.type === 'text' ? x.text : '[' + (x.type || 'content') + ']'; }).join(' ');
    return c === undefined || c === null ? '' : JSON.stringify(c);
  }
  // Map one stream-json line from an external agent to transcript events.
  function mapExternalLine(line) {
    var j; try { j = JSON.parse(line); } catch (e) { return line.trim() ? [{ type: 'observation', text: line }] : []; }
    if (!j || typeof j !== 'object') return [];
    var out = [], content = j.message && Array.isArray(j.message.content) ? j.message.content : [];
    if (j.type === 'assistant') {
      content.forEach(function (c) {
        if (c.type === 'text' && c.text) out.push({ type: 'thought', text: c.text });
        else if (c.type === 'tool_use') out.push({ type: 'action', tool: String(c.name || '').replace(/^mcp__phoneforge__/, ''), args: c.input || {} });
      });
    } else if (j.type === 'user') {
      content.forEach(function (c) {
        if (c.type === 'tool_result') {
          var t = toolResultText(c.content);
          out.push({ type: c.is_error ? 'error' : 'observation', text: c.is_error ? t.slice(0, 300) : '', result: c.is_error ? undefined : t });
        }
      });
    } else if (j.type === 'result') {
      if (j.is_error) out.push({ type: 'error', text: j.result || j.subtype || 'agent failed' });
      else out.push({ type: 'done', text: j.result || 'done' });
    }
    return out;
  }

  function runExternal(task, sink, agent) {
    agentSink = sink || null;
    var lbl = AGENT_LABELS[agent] || agent;
    tAdd(stepLine({ type: 'thought', text: 'Task (' + lbl + '): ' + task }));
    $('agentSend').disabled = true; $('agentStop').hidden = false;
    var run = extRun = { id: null, es: null, cancelled: false, sawDone: false };
    function emit(ev) { handleAgentEvent(ev); }
    function finish() {
      if (run.es) { run.es.close(); run.es = null; }
      if (extRun === run) extRun = null;
      $('agentSend').disabled = false; $('agentStop').hidden = true; refreshSoon();
      var sk = agentSink; agentSink = null;
      if (sk) sk.done();
    }
    PF.api('POST', '/ai/runs', { type: 'automation', deviceId: PF.selectedId || '', prompt: task, sessionId: chatSessions[chatKey()] || '' }).then(function (r) {
      if (r && r.builtin) { agentSink = null; finish(); runAgent(task, sink); return; }
      run.id = r.id;
      if (run.cancelled) { PF.api('POST', '/ai/runs/' + encodeURIComponent(r.id) + '/cancel').catch(function () {}); }
      var es = run.es = new EventSource(PF.API + '/ai/runs/' + encodeURIComponent(r.id) + '/events');
      function data(ev) { try { return JSON.parse(ev.data).data || ''; } catch (x) { return ''; } }
      var runKey = chatKey();
      es.addEventListener('session', function (ev) { var id = data(ev); if (id) chatSessions[runKey] = id; });
      es.addEventListener('start', function () { emit({ type: 'thought', text: lbl + ' started' }); });
      es.addEventListener('stdout', function (ev) { mapExternalLine(data(ev)).forEach(function (e) { if (e.type === 'done') run.sawDone = true; emit(e); }); });
      es.addEventListener('stderr', function (ev) { var t = data(ev); if (t.trim()) emit({ type: 'error', text: t }); });
      es.addEventListener('error', function (ev) { if (ev.data) emit({ type: 'error', text: data(ev) }); });
      es.addEventListener('done', function (ev) {
        var d = data(ev);
        if (d === 'cancelled') emit({ type: 'error', text: 'Stopped.' });
        else if (d === 'failed') emit({ type: 'error', text: lbl + ' run failed' });
        else if (!run.sawDone) emit({ type: 'done', text: 'done' });
        finish();
      });
      es.onerror = function () { if (run.es === es && es.readyState === 2) finish(); };
    }, function (e) { tErr('Could not start ' + lbl + ': ' + e.message); finish(); });
  }
  function stopAgent() {
    if (agentAbort) agentAbort.abort();
    if (extRun) {
      extRun.cancelled = true;
      if (extRun.id) PF.api('POST', '/ai/runs/' + encodeURIComponent(extRun.id) + '/cancel').catch(function () {});
    }
  }

  function startTask(task) {
    if (!PF.needDevice()) return;
    var d = PF.selectedDevice();
    if (d && d.state !== 'connected') { PF.toast('Device is ' + d.state); return; }
    loadAISettings().then(function () {
      var a = selAgent();
      if (a === 'builtin') runAgent(task); else runExternal(task, null, a);
    });
  }

  /* Used by the AI Configuration panel when the "built-in" agent is selected: runs the existing
     /devices/{id}/agent/run SSE stream on the selected device. The stream shows in the Automation
     tab's agent transcript and, when onLine/onDone are given, is copied to the caller as plain text.
     Returns {stop()}; the AI Configuration panel wires its Cancel button to it. */
  PF.runBuiltinAgent = function (prompt, type, onLine, onDone) {
    var done = false;
    function finish() { if (!done) { done = true; if (onDone) onDone(); } }
    if (agentAbort || extRun) {
      if (onLine) onLine('[error] the built-in agent is already running');
      finish();
      return { stop: function () {} };
    }
    if (onLine && type) onLine('[start] built-in agent (' + type + ')');
    runAgent(prompt, { line: function (t) { if (onLine) onLine(t); }, done: finish });
    return { stop: function () { if (agentAbort) agentAbort.abort(); } };
  };
  $('agentForm').addEventListener('submit', function (e) {
    e.preventDefault();
    var v = $('agentInput').value.trim(); if (!v) return;
    $('agentInput').value = '';
    startTask(v);
  });
  $('agentStop').addEventListener('click', stopAgent);

  PF.refreshDeviceRows = function () { renderDevices(true); };
  PF.isAndroid = function (d) { return !!d && d.platform === 'android'; };
  PF.mirrorGeo = { metrics: metrics, toDevice: toDevice };
  PF.loadDevices = loadDevices;
  PF.selectDevice = select;
  PF.mirrorSoon = refreshSoon;
  PF.startAutomation = function () {
    loadDevices(); setInterval(loadDevices, 3000); connectEvents(); loadAISettings();
  };
})();
