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

  /* health */
  function health() {
    PF.api('GET', '/health').then(function (h) {
      $('ver').textContent = 'v' + (h.version || '?') + ' · ' + (h.deviceCount != null ? h.deviceCount : 0) + ' device' + (h.deviceCount === 1 ? '' : 's');
    }).catch(function () { $('ver').textContent = 'server unreachable'; });
  }
  health(); setInterval(health, 10000);

  PF.startAutomation();
})();
