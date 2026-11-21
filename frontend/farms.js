/* Mobile Farms modal: BrowserStack, Sauce Labs, AWS Device Farm. Credentials are write-only. */
(function () {
  'use strict';
  var PF = window.PF;
  var PROVIDERS = [
    { id: 'browserstack', title: 'BrowserStack', fields: [['username', 'Username', false], ['access_key', 'Access key', true]] },
    { id: 'saucelabs', title: 'Sauce Labs', fields: [['username', 'Username', false], ['accessKey', 'Access key', true]] },
    { id: 'awsdevicefarm', title: 'AWS Device Farm', fields: [['accessKeyId', 'Access key ID', true], ['secretAccessKey', 'Secret access key', true], ['region', 'Region (e.g. us-west-2)', false]] }
  ];

  PF.openFarms = function () {
    var m = PF.modal.open('Mobile Farms', { wide: true });
    var host = PF.el('div', 'cards');
    m.body.appendChild(PF.el('p', 'muted', 'Connect cloud device farms. Credentials are saved on the server and never shown here again.'));
    m.body.appendChild(host);
    var state = {};

    function card(p) {
      var s = state[p.id] || {};
      var c = PF.el('div', 'card');
      var head = PF.el('div', 'card-head');
      head.appendChild(PF.el('strong', '', p.title));
      head.appendChild(PF.pill(s.hasCredentials ? 'configured' : 'not configured', s.hasCredentials ? 'ok' : ''));
      head.appendChild(PF.pill(s.enabled ? 'enabled' : 'disabled', s.enabled ? 'ok' : ''));
      c.appendChild(head);

      var inputs = {};
      p.fields.forEach(function (f) {
        var i = PF.input(f[2] ? 'password' : 'text', s.hasCredentials ? 'set — enter to replace' : f[1]);
        inputs[f[0]] = i; c.appendChild(PF.row(f[1], i));
      });
      var out = PF.el('div', 'farm-out');
      var row = PF.el('div', 'btn-row');
      var save = PF.btn('Save credentials', 'primary sm', function () {
        var body = {};
        p.fields.forEach(function (f) { var v = inputs[f[0]].value.trim(); if (v) body[f[0]] = v; });
        if (!Object.keys(body).length) { PF.toast('Enter credentials first', true); return; }
        PF.busy(save, PF.api('PUT', '/farms/' + p.id, body).then(function () {
          p.fields.forEach(function (f) { inputs[f[0]].value = ''; });
          PF.toast(p.title + ' saved', true); load();
        }));
      });
      var list = PF.btn('List devices', 'sm', function () {
        out.innerHTML = ''; out.appendChild(PF.el('div', 'muted', 'Loading...'));
        PF.busy(list, PF.api('GET', '/farms/' + p.id + '/devices').then(function (ds) {
          out.innerHTML = '';
          out.appendChild(PF.table(['Name', 'OS', 'Version', 'Device ID', 'Real'],
            (ds || []).map(function (d) { return [d.name, d.os, d.osVersion, d.deviceId, d.real ? 'real' : 'virtual']; }), 'No devices returned.'));
        }, function (e) {
          out.innerHTML = '';
          var msg = e.status === 501 ? 'Listing devices needs the AWS SDK, which this server build does not include (501).'
            : e.status === 412 ? 'Save credentials first (412: credentials missing).' : e.message;
          out.appendChild(PF.note(msg, e.status === 501 ? 'info' : 'bad'));
        }));
      });
      var dis = PF.btn('Disable', 'sm danger', function () {
        if (!window.confirm('Remove stored credentials and disable ' + p.title + '?')) return;
        PF.busy(dis, PF.api('DELETE', '/farms/' + p.id).then(function () { out.innerHTML = ''; PF.toast(p.title + ' disabled', true); load(); }));
      });
      dis.disabled = !s.hasCredentials && !s.enabled;
      row.appendChild(save); row.appendChild(list); row.appendChild(dis);
      c.appendChild(row); c.appendChild(out);
      return c;
    }

    function load() {
      PF.api('GET', '/farms').then(function (fs) {
        state = {}; (fs || []).forEach(function (f) { state[f.provider] = f; });
      }, function (e) { PF.toast('Could not load farms: ' + e.message); }).then(function () {
        host.innerHTML = ''; PROVIDERS.forEach(function (p) { host.appendChild(card(p)); });
      });
    }
    load();
  };
})();
