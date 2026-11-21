/* OCR tool: run text recognition on the selected device, draw the boxes over the live view and list the
   text. Clicking a box or a list row taps that text on the device (server-side POST /ocr/devices/{id}/tap). */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var pop = $('ocrPop'), btn = $('ocrBtn');
  var layer = null, results = [], deviceId = null, listEl = null, statusEl = null;

  function ensureLayer() {
    if (layer && layer.parentNode) return layer;
    layer = PF.el('div', 'ocr-layer');
    $('screen').appendChild(layer);
    return layer;
  }

  function clearOverlay() { if (layer) layer.innerHTML = ''; }

  // Position the boxes using the mirror geometry exported by automation.js (device px -> css px).
  function drawOverlay() {
    clearOverlay();
    var geo = PF.mirrorGeo && PF.mirrorGeo.metrics && PF.mirrorGeo.metrics();
    if (!geo || !results.length) return;
    var sr = $('screen').getBoundingClientRect(), l = ensureLayer();
    results.forEach(function (r, i) {
      var b = r.bounds || {};
      var box = PF.el('div', 'ocr-box');
      box.dataset.i = i;
      box.title = r.text;
      box.style.left = (geo.r.left - sr.left + geo.offX + b.x * geo.kx) + 'px';
      box.style.top = (geo.r.top - sr.top + geo.offY + b.y * geo.ky) + 'px';
      box.style.width = Math.max(b.w * geo.kx, 3) + 'px';
      box.style.height = Math.max(b.h * geo.ky, 3) + 'px';
      box.addEventListener('click', function (e) { e.stopPropagation(); tapResult(i); });
      box.addEventListener('mouseenter', function () { mark(i, true); });
      box.addEventListener('mouseleave', function () { mark(i, false); });
      l.appendChild(box);
    });
  }

  function mark(i, on) {
    [layer && layer.children[i], listEl && listEl.children[i]].forEach(function (n) { if (n) n.classList.toggle('on', on); });
  }

  // index among earlier results with identical text, so the server taps the same occurrence.
  function occurrence(i) {
    var key = String(results[i].text).trim().toLowerCase(), n = 0;
    for (var k = 0; k < i; k++) if (String(results[k].text).trim().toLowerCase() === key) n++;
    return n;
  }

  function tapResult(i) {
    if (!PF.needDevice() || !results[i]) return;
    var r = results[i];
    PF.guardDev(PF.api('POST', '/ocr/devices/' + encodeURIComponent(PF.selectedId) + '/tap', { text: r.text.trim(), exact: true, index: occurrence(i) }).then(function () {
      PF.toast('Tapped "' + r.text.trim() + '"', true);
      if (PF.mirrorSoon) PF.mirrorSoon();
    }));
  }

  function render(data) {
    pop.innerHTML = '';
    var head = PF.el('div', 'btn-row');
    var run = PF.btn('Re-scan', 'sm primary', runOCR);
    var words = document.createElement('label'); words.className = 'toggle';
    var wc = document.createElement('input'); wc.type = 'checkbox'; wc.id = 'ocrWords'; wc.checked = !!render.words;
    wc.addEventListener('change', function () { render.words = wc.checked; });
    words.appendChild(wc); words.appendChild(document.createTextNode(' Word boxes'));
    var clear = PF.btn('Clear overlay', 'sm', function () { results = []; clearOverlay(); render(null); });
    var close = PF.btn('Close', 'sm', function () { pop.hidden = true; });
    [run, words, clear, close].forEach(function (n) { head.appendChild(n); });
    pop.appendChild(head);
    statusEl = PF.el('div', 'muted', data ? (results.length + ' text item(s)' + (data.engine ? ' via ' + data.engine : '') + '. Click one to tap it.') : 'Press Re-scan to read the screen.');
    statusEl.style.marginTop = '6px';
    pop.appendChild(statusEl);
    listEl = PF.el('ul', 'ocr-list');
    results.forEach(function (r, i) {
      var li = PF.el('li');
      li.appendChild(PF.el('span', '', String(r.text).trim() || '(blank)'));
      li.appendChild(PF.el('span', 'cf', Math.round((r.confidence || 0) * 100) + '%'));
      li.addEventListener('click', function () { tapResult(i); });
      li.addEventListener('mouseenter', function () { mark(i, true); });
      li.addEventListener('mouseleave', function () { mark(i, false); });
      listEl.appendChild(li);
    });
    pop.appendChild(listEl);
  }

  function runOCR() {
    if (!PF.needDevice()) return;
    var id = PF.selectedId;
    if (statusEl) statusEl.textContent = 'Reading screen...';
    var b = $('ocrBtn');
    PF.busy(b, PF.api('POST', '/ocr/devices/' + encodeURIComponent(id), { words: !!render.words }).then(function (data) {
      if (id !== PF.selectedId) return;
      deviceId = id;
      results = ((data && data.results) || []).filter(function (r) { return r && String(r.text).trim(); });
      render(data || {});
      drawOverlay();
      if (!results.length) PF.toast('No text recognized', true);
    }).catch(function (e) {
      if (statusEl) statusEl.textContent = 'OCR failed: ' + e.message;
      return PF.guardDev(Promise.reject(e));
    }));
  }

  btn.addEventListener('click', function () {
    if (!pop.hidden) { pop.hidden = true; return; }
    $('webPop').hidden = true; $('clipPop').hidden = true;
    if (!PF.needDevice()) return;
    pop.hidden = false;
    render(results.length ? {} : null);
    runOCR();
  });
  window.addEventListener('resize', function () { if (results.length) drawOverlay(); });
  PF.hooks.onSelect.push(function () { results = []; deviceId = null; clearOverlay(); pop.hidden = true; });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !pop.hidden && $('modal').hidden) pop.hidden = true; });
})();
