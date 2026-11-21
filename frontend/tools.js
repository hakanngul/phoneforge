/* Toolbar additions: Open URL, Hide keyboard, Clipboard popover, Record. UNSUPPORTED shows the server message. */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;

  function openUrl() {
    if (!PF.needDevice()) return;
    var u = $('urlText').value.trim();
    if (!u) { PF.toast('Enter a URL first', true); return; }
    PF.guardDev(PF.api('POST', PF.dev('/open-url'), { url: u }).then(function () {
      PF.toast('Opening ' + u, true); if (PF.mirrorSoon) PF.mirrorSoon();
    }));
  }
  $('urlBtn').addEventListener('click', openUrl);
  $('urlText').addEventListener('keydown', function (e) { if (e.key === 'Enter') openUrl(); });

  $('kbBtn').addEventListener('click', function () {
    if (!PF.needDevice()) return;
    PF.guardDev(PF.api('POST', PF.dev('/dismiss-keyboard'), {}).then(function () { if (PF.mirrorSoon) PF.mirrorSoon(); }));
  });

  /* clipboard popover */
  var pop = $('clipPop');
  $('clipBtn').addEventListener('click', function () { $('ocrPop').hidden = true; $('webPop').hidden = true; pop.hidden = !pop.hidden; if (!pop.hidden) $('clipText').focus(); });
  $('clipClose').addEventListener('click', function () { pop.hidden = true; });
  $('clipRead').addEventListener('click', function () {
    if (!PF.needDevice()) return;
    PF.busy($('clipRead'), PF.api('GET', PF.dev('/clipboard')).then(function (r) {
      $('clipText').value = (r && r.text) || '';
      if (!r || !r.text) PF.toast('Device clipboard is empty', true);
    }).catch(function (e) { return PF.guardDev(Promise.reject(e)); }));
  });
  $('clipSend').addEventListener('click', function () {
    if (!PF.needDevice()) return;
    PF.busy($('clipSend'), PF.api('POST', PF.dev('/clipboard'), { text: $('clipText').value }).then(function () {
      PF.toast('Sent to device clipboard', true);
    }).catch(function (e) { return PF.guardDev(Promise.reject(e)); }));
  });
  PF.hooks.onSelect.push(function () { pop.hidden = true; });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !pop.hidden && $('modal').hidden) pop.hidden = true; });

  /* record */
  var recTimer = null;
  $('recBtn').addEventListener('click', function () {
    if (!PF.needDevice()) return;
    var n = parseInt($('recSecs').value, 10);
    if (!(n >= 1)) n = 10;
    if (n > 180) n = 180;
    $('recSecs').value = n;
    var btn = $('recBtn'), id = PF.selectedId, left = n;
    btn.disabled = true; btn.textContent = 'Recording ' + left + 's';
    clearInterval(recTimer);
    recTimer = setInterval(function () { left = Math.max(left - 1, 0); btn.textContent = left > 0 ? 'Recording ' + left + 's' : 'Saving...'; }, 1000);
    PF.api('GET', '/devices/' + encodeURIComponent(id) + '/recording?seconds=' + n, undefined, 'blob').then(function (blob) {
      PF.saveBlob(blob, 'recording-' + id.replace(/[^A-Za-z0-9._-]/g, '_') + '-' + Date.now() + '.mp4');
      PF.toast('Recording saved (' + Math.round(blob.size / 1024) + ' KB)', true);
    }).catch(function (e) { return PF.guardDev(Promise.reject(e)); }).then(function () {
      clearInterval(recTimer); btn.disabled = false; btn.textContent = 'Record';
    });
  });
})();
