/* Apps tab: list, search, launch/stop. */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var apps = [];

  function render() {
    var q = $('appSearch').value.trim().toLowerCase();
    var ul = $('appList');
    ul.innerHTML = '';
    var shown = apps.filter(function (a) {
      return !q || (a.bundleId || '').toLowerCase().indexOf(q) >= 0 || (a.name || '').toLowerCase().indexOf(q) >= 0;
    });
    $('appsMsg').textContent = apps.length ? shown.length + ' of ' + apps.length + ' apps' : '';
    shown.forEach(function (a) {
      var li = PF.el('li');
      var n = PF.el('span', 'n', (a.name ? a.name + '  ' : '') + a.bundleId);
      var l = PF.el('button', 'btn sm', 'Launch');
      var s = PF.el('button', 'btn sm', 'Stop');
      l.onclick = function () { PF.guard(PF.api('POST', PF.dev('/launch-app'), { bundleId: a.bundleId }).then(function () { PF.toast('Launched ' + a.bundleId, true); })); };
      s.onclick = function () { PF.guard(PF.api('POST', PF.dev('/stop-app'), { bundleId: a.bundleId }).then(function () { PF.toast('Stopped ' + a.bundleId, true); })); };
      var u = PF.el('button', 'btn sm danger', 'Uninstall');
      u.onclick = function () { confirmUninstall(a); };
      li.appendChild(n); li.appendChild(l); li.appendChild(s); li.appendChild(u);
      ul.appendChild(li);
    });
  }

  function confirmUninstall(a) {
    var m = PF.modal.open('Uninstall app');
    m.body.appendChild(PF.el('p', 'confirm-text', 'Uninstall ' + (a.name ? a.name + ' (' + a.bundleId + ')' : a.bundleId) + ' from this device? This removes the app and its data.'));
    var row = PF.el('div', 'btn-row');
    var ok = PF.btn('Uninstall', 'danger', function () {
      m.close();
      PF.guardDev(PF.api('DELETE', PF.dev('/apps/' + encodeURIComponent(a.bundleId))).then(function () {
        PF.toast('Uninstalled ' + a.bundleId, true); load();
      }));
    });
    row.appendChild(ok); row.appendChild(PF.btn('Cancel', '', m.close));
    m.body.appendChild(row);
  }

  function installApk(file) {
    var msg = $('installMsg'), btn = $('apkBtn');
    if (!PF.needDevice()) return;
    if (!/\.apk$/i.test(file.name)) { PF.toast('Choose an .apk file'); return; }
    msg.hidden = false; btn.disabled = true;
    msg.textContent = 'Uploading ' + file.name + ' 0%';
    PF.upload(PF.dev('/install-app'), 'file', file, function (f, phase) {
      msg.textContent = phase === 'sent' ? 'Uploaded, installing ' + file.name + '...' : 'Uploading ' + file.name + ' ' + Math.round(f * 100) + '%';
    }).then(function () {
      msg.textContent = 'Installed ' + file.name; PF.toast('Installed ' + file.name, true); load();
    }).catch(function (e) {
      msg.textContent = 'Install failed';
      if (e.code === 'UNSUPPORTED' || e.status === 501) PF.toast(e.serverMessage || 'Install is not supported on this device', true);
      else PF.toast(e.message);
    }).then(function () { btn.disabled = false; });
  }
  $('apkBtn').addEventListener('click', function () { if (PF.needDevice()) $('apkFile').click(); });
  $('apkFile').addEventListener('change', function () {
    var f = this.files && this.files[0]; this.value = '';
    if (f) installApk(f);
  });

  function load() {
    apps = [];
    $('appList').innerHTML = '';
    var d = PF.selectedDevice();
    $('appsTitle').textContent = d ? 'Apps - ' + (d.name || d.id) : 'Apps';
    if (!PF.selectedId) { $('appsMsg').textContent = 'Select a device in the Automation tab.'; return; }
    if (d && d.state !== 'connected') { $('appsMsg').textContent = 'Device is ' + d.state + '.'; return; }
    $('appsMsg').textContent = 'Loading...';
    var id = PF.selectedId;
    PF.api('GET', PF.dev('/apps')).then(function (r) {
      if (id !== PF.selectedId) return;
      apps = r || [];
      if (!apps.length) $('appsMsg').textContent = 'No apps reported.';
      render();
    }).catch(function (e) { $('appsMsg').textContent = 'Failed to load apps.'; PF.toast(e.message); });
  }

  PF.loadApps = load;
  $('appSearch').addEventListener('input', render);
  $('appsRefresh').addEventListener('click', load);
  PF.hooks.onSelect.push(function () { if (!$('apps-tab').hidden) load(); });
})();
