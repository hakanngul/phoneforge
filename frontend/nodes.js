/* Remote nodes (Tailscale / any network) panel in the devices sidebar + "Share my devices" explainer. */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var box = $('nodesBox');
  var results = {}; // node id -> {ok, text}

  function render(nodes, loadError) {
    box.innerHTML = '';
    var det = PF.el('details'); det.open = nodes.length === 0 || !!window.__nodesOpen;
    det.addEventListener('toggle', function () { window.__nodesOpen = det.open; });
    det.appendChild(PF.el('summary', '', 'Nodes (' + nodes.length + ') — add by address'));

    if (loadError) det.appendChild(PF.note('Could not load nodes: ' + loadError, 'bad'));
    nodes.forEach(function (n) {
      var row = PF.el('div', 'node');
      var top = PF.el('div', 'node-top');
      top.appendChild(PF.el('strong', '', n.name || n.id));
      if (n.token === 'set') top.appendChild(PF.pill('token set', 'ok'));
      row.appendChild(top);
      row.appendChild(PF.el('div', 'sub muted', n.addr));
      var acts = PF.el('div', 'node-acts');
      var res = PF.el('span', 'node-res');
      var r = results[n.id];
      if (r) { res.textContent = r.text; res.className = 'node-res ' + (r.ok ? 'ok' : 'bad'); }
      var test = PF.btn('Test', 'sm', function () {
        res.textContent = 'testing...'; res.className = 'node-res';
        PF.busy(test, PF.api('POST', '/nodes/' + encodeURIComponent(n.id) + '/test').then(function (t) {
          results[n.id] = { ok: true, text: 'OK v' + (t.version || '?') + ' · ' + (t.deviceCount != null ? t.deviceCount : 0) + ' device(s)' };
        }, function (e) {
          results[n.id] = { ok: false, text: e.status === 502 ? 'Unreachable' : 'Failed: ' + e.message };
        }).then(function () { res.textContent = results[n.id].text; res.className = 'node-res ' + (results[n.id].ok ? 'ok' : 'bad'); }));
      });
      var rm = PF.btn('Remove', 'sm', function () {
        PF.confirm('Remove node "' + (n.name || n.id) + '"?', function () {
          PF.busy(rm, PF.api('DELETE', '/nodes/' + encodeURIComponent(n.id)).then(function () {
            delete results[n.id]; PF.toast('Node removed', true); load(); PF.loadDevices();
          }));
        }, { title: 'Remove node', okLabel: 'Remove', danger: true });
      });
      acts.appendChild(test); acts.appendChild(rm); acts.appendChild(res);
      row.appendChild(acts);
      det.appendChild(row);
    });

    var form = PF.el('form', 'node-form');
    var name = PF.input('text', 'Name (e.g. Lab Mac)');
    var addr = PF.input('text', 'http://100.x.y.z:8700');
    var tok = PF.input('password', 'Token (optional)');
    var add = PF.btn('Add node', 'sm primary');
    add.type = 'submit';
    [name, addr, tok].forEach(function (i) { form.appendChild(i); });
    form.appendChild(add);
    form.addEventListener('submit', function (e) {
      e.preventDefault();
      var a = addr.value.trim();
      if (!/^https?:\/\//i.test(a)) { PF.toast('Address must start with http:// or https://'); return; }
      var body = { name: name.value.trim() || a, addr: a, token: tok.value };
      PF.busy(add, PF.api('POST', '/nodes', body).then(function () {
        tok.value = ''; PF.toast('Node added', true); window.__nodesOpen = true; load(); PF.loadDevices();
      }));
    });
    det.appendChild(form);
    box.appendChild(det);
  }

  function load() {
    PF.api('GET', '/nodes').then(function (n) { render(n || []); }, function (e) { render([], e.message); });
  }

  /* Share my devices: honest explainer, nothing is toggled on the server. */
  $('shareBtn').addEventListener('click', function () {
    var m = PF.modal.open('Share my devices');
    var b = m.body;
    b.appendChild(PF.note('Sharing is not a switch in this UI. Devices are shared by running the Pocketlab server bound to a LAN or Tailscale address, protected with a token. Other Pocketlab instances then add it as a Remote node.', 'info'));
    var cur = PF.el('p', 'muted', 'Checking current bind address...');
    b.appendChild(cur);
    PF.api('GET', '/health').then(function (h) {
      var parts = [];
      if (h.addr || h.bind || h.listen) parts.push('bind: ' + (h.addr || h.bind || h.listen));
      if (h.hostname) parts.push('hostname: ' + h.hostname);
      parts.push('this page was loaded from ' + location.host);
      cur.textContent = 'Current server — ' + parts.join(' · ') + (parts.length === 1 ? '' : '') + '.';
    }, function () { cur.textContent = 'Could not read /health. This page was loaded from ' + location.host + '.'; });
    b.appendChild(PF.section('1. Start the server on a reachable address'));
    b.appendChild(PF.codeBlock('pocketlabd -addr 0.0.0.0:8700 -token <token>', 'command'));
    b.appendChild(PF.el('p', 'muted', 'Use your Tailscale IP (e.g. 100.x.y.z:8700) instead of 0.0.0.0 to limit exposure to your tailnet. Always set a long random token; without one anyone who can reach the port can control your phones.'));
    b.appendChild(PF.section('2. Connect from another machine'));
    b.appendChild(PF.el('p', 'muted', 'On the other Pocketlab, open Automation → Remote → Nodes, enter http://<your-ip>:8700 and the token, then press Test.'));
  });

  load();
})();
