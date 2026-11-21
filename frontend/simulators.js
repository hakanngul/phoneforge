/* iOS Simulators modal: list, boot/shutdown, start/stop WebDriverAgent. Booted simulators then
   appear in the normal device list (sim-<udid>). */
(function () {
  'use strict';
  var PF = window.PF;

  PF.openSimulators = function () {
    var m = PF.modal.open('iOS Simulators', { wide: true });
    var host = PF.el('div', 'cards');
    var note = PF.el('p', 'muted', 'Boot a simulator, then Start WDA for taps, typing and the UI tree. Live view uses MJPEG while WDA runs, screenshots otherwise.');
    m.body.appendChild(note);
    m.body.appendChild(host);
    var timer = null;

    function act(btn, method, path, ok) {
      PF.busy(btn, PF.api(method, path).then(function () {
        if (ok) PF.toast(ok, true);
        var rb = document.getElementById('refreshDevices'); if (rb) rb.click();
        return load();
      }, function (e) { PF.toast(e.message || String(e), false); return load(); }));
    }

    function card(s, built) {
      var c = PF.el('div', 'card');
      var head = PF.el('div', 'card-head');
      head.appendChild(PF.el('strong', '', s.name));
      head.appendChild(PF.pill(s.runtime, ''));
      head.appendChild(PF.pill(s.state, s.state === 'Booted' ? 'ok' : ''));
      var wda = s.wda || {};
      if (s.state === 'Booted') head.appendChild(PF.pill('WDA: ' + (wda.state || 'stopped'), wda.state === 'running' ? 'ok' : ''));
      c.appendChild(head);
      var base = '/simulators/' + encodeURIComponent(s.udid);
      var row = PF.el('div', 'btn-row');
      if (s.state !== 'Booted') {
        row.appendChild(PF.btn('Boot', 'primary sm', function (e) { act(e.currentTarget || row.firstChild, 'POST', base + '/boot', s.name + ' booted'); }));
      } else {
        row.appendChild(PF.btn('Shutdown', 'sm danger', function (e) { act(e.currentTarget, 'POST', base + '/shutdown', s.name + ' shut down'); }));
        if (s.platform === 'iOS') {
          if (wda.state === 'running') {
            row.appendChild(PF.btn('Stop WDA', 'sm', function (e) { act(e.currentTarget, 'POST', base + '/wda/stop', 'WDA stopped'); }));
          } else {
            var b = PF.btn('Start WDA', 'primary sm', function (e) { act(e.currentTarget, 'POST', base + '/wda/start', 'WDA running'); });
            b.disabled = !built || wda.state === 'starting';
            row.appendChild(b);
          }
        }
      }
      c.appendChild(row);
      if (wda.error) c.appendChild(PF.note(wda.error, 'error'));
      return c;
    }

    function load() {
      return PF.api('GET', '/simulators').then(function (r) {
        host.innerHTML = '';
        var built = r && r.wdaBuilt;
        if (!built) host.appendChild(PF.note('WDA is not built for the simulator yet: run scripts/build-wda.sh --sim', 'info'));
        var list = (r && r.simulators) || [];
        if (!list.length) host.appendChild(PF.el('p', 'muted', 'No simulators found (is Xcode installed?).'));
        list.forEach(function (s) { host.appendChild(card(s, built)); });
      }, function (e) { host.innerHTML = ''; host.appendChild(PF.note('Could not list simulators: ' + e.message, 'error')); });
    }
    load();
    timer = setInterval(function () { if (!document.body.contains(host)) { clearInterval(timer); return; } load(); }, 5000);
  };
})();
