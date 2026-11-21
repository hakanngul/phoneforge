/*
 * PhoneForge web UI entry point (vanilla JS, no dependencies).
 * Files: api.js, ui.js (modal/forms), automation.js, multiwindow.js, apps.js, testing.js,
 * nodes.js, webhooks.js, farms.js, credentials.js, integrations.js, app.js.
 *
 * Tap coordinate math (implemented in automation.js):
 *   The mirror <img> is object-fit: contain, so the bitmap is scaled by
 *   scale = min(boxW/naturalW, boxH/naturalH) and centered with letterbox
 *   offsets offX = (boxW - naturalW*scale)/2, offY likewise. A click maps to the
 *   device pixel ((clientX - rect.left - offX)/scale, (clientY - rect.top - offY)/scale);
 *   values outside [0,natural) are letterbox clicks and are ignored.
 *
 * Element picker: on click, fetch /ui-tree, walk depth-first, keep the deepest
 * node whose bounds contain the device pixel, show it in the inspector and draw
 * its bounds back onto the mirror using the inverse transform.
 */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;

  /* tabs */
  var tabs = document.querySelectorAll('#mainTabs .tab');
  function showTab(id) {
    Array.prototype.forEach.call(tabs, function (t) { t.classList.toggle('active', t.getAttribute('data-tab') === id); });
    ['apps-tab', 'automation-tab', 'testing-tab'].forEach(function (p) {
      var el = $(p); el.hidden = p !== id;
    });
    try { localStorage.setItem('pf.tab', id); } catch (e) { /* ignore */ }
    if (id === 'apps-tab') PF.loadApps();
    if (id === 'testing-tab' && PF.loadTests) PF.loadTests();
  }
  Array.prototype.forEach.call(tabs, function (t) { t.addEventListener('click', function () { showTab(t.getAttribute('data-tab')); }); });
  var saved = 'automation-tab';
  try { saved = localStorage.getItem('pf.tab') || saved; } catch (e) { /* ignore */ }
  showTab(saved);

  /* quit */
  var quit = $('quitBtn');
  /* in-page confirm (window.confirm is a no-op in embedded webviews) */
  function confirmQuit(onYes) {
    var bg = document.createElement('div');
    bg.className = 'modal-bg';
    bg.style.zIndex = 60;
    bg.innerHTML = '<div class="modal" role="alertdialog" aria-modal="true" aria-labelledby="qcTitle" style="width:min(420px,92vw)">' +
      '<div class="modal-head"><h3 id="qcTitle">Quit PhoneForge?</h3></div>' +
      '<div class="modal-body"><p style="margin:0 0 14px">This stops the server and closes the app; devices will no longer be controllable.</p>' +
      '<div class="btn-row" style="justify-content:flex-end"><button class="btn" data-qc="cancel">Cancel</button>' +
      '<button class="btn danger" data-qc="ok">Quit</button></div></div></div>';
    function close() { document.removeEventListener('keydown', onKey, true); if (bg.parentNode) bg.parentNode.removeChild(bg); }
    function onKey(e) { if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(); } }
    bg.addEventListener('click', function (e) { if (e.target === bg) close(); });
    bg.querySelector('[data-qc="cancel"]').addEventListener('click', close);
    bg.querySelector('[data-qc="ok"]').addEventListener('click', function () { close(); onYes(); });
    document.addEventListener('keydown', onKey, true);
    document.body.appendChild(bg);
    bg.querySelector('[data-qc="cancel"]').focus();
  }
  if (quit) quit.addEventListener('click', function () {
    confirmQuit(doQuit);
  });
  function doQuit() {
    quit.disabled = true;
    fetch('/api/v1/server/shutdown', { method: 'POST', headers: { 'X-PhoneForge-Action': 'shutdown' } }).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      document.title = 'PhoneForge stopped';
      document.body.innerHTML = '<div style="display:flex;height:100vh;align-items:center;justify-content:center;flex-direction:column;font-family:system-ui,sans-serif;color:#ccc;background:#0e0d14"><h2>PhoneForge stopped</h2><p>You can close this tab.</p></div>';
    }).catch(function (e) { quit.disabled = false; PF.toast('Could not stop PhoneForge: ' + e.message); });
  }

  /* health */
  function health() {
    PF.api('GET', '/health').then(function (h) {
      $('ver').textContent = 'v' + (h.version || '?') + ' · ' + (h.deviceCount != null ? h.deviceCount : 0) + ' device' + (h.deviceCount === 1 ? '' : 's');
    }).catch(function () { $('ver').textContent = 'server unreachable'; });
  }
  health(); setInterval(health, 10000);

  PF.startAutomation();
})();
