/* Webhooks manager modal: CRUD table + add/edit form + test. Secrets are write-only. */
(function () {
  'use strict';
  var PF = window.PF;

  function fmtTime(s) { if (!s) return ''; var d = new Date(s); return isNaN(d) ? String(s) : d.toLocaleString(); }

  function lastDelivery(h) {
    var l = h.last_delivery;
    if (!l) return 'never';
    if (typeof l !== 'object') return String(l);
    var ok = l.ok != null ? l.ok : (l.code >= 200 && l.code < 300);
    return (ok ? 'OK' : 'FAIL') + (l.code ? ' ' + l.code : '') + (l.time ? ' · ' + fmtTime(l.time) : '') + (l.error ? ' · ' + l.error : '');
  }

  PF.openWebhooks = function () {
    var m = PF.modal.open('Webhooks', { wide: true });
    var list = PF.el('div'), formBox = PF.el('div');
    m.body.appendChild(PF.el('p', 'muted', 'POST event notifications to your URL. The optional secret signs deliveries and is write-only: it is never shown again.'));
    m.body.appendChild(list); m.body.appendChild(formBox);

    function form(h) {
      formBox.innerHTML = '';
      formBox.appendChild(PF.section(h ? 'Edit webhook' : 'Add webhook'));
      var name = PF.input('text', 'Name'), url = PF.input('text', 'https://example.com/hook');
      var ev = PF.input('text', 'events, comma separated (empty = all)');
      var secret = PF.input('password', h && h.has_secret ? 'Secret set — leave blank to keep' : 'Secret (optional)');
      var en = document.createElement('input'); en.type = 'checkbox'; en.checked = h ? h.enabled !== false : true;
      if (h) { name.value = h.name || ''; url.value = h.url || ''; ev.value = (h.events || []).join(', '); }
      formBox.appendChild(PF.row('Name', name));
      formBox.appendChild(PF.row('URL', url));
      formBox.appendChild(PF.row('Events', ev));
      formBox.appendChild(PF.row('Secret', secret, 'Write-only.'));
      if (h) { var er = PF.row('Enabled', en); er.className += ' inline'; formBox.appendChild(er); }
      var save = PF.btn(h ? 'Save changes' : 'Add webhook', 'primary', function () {
        if (!/^https?:\/\//i.test(url.value.trim())) { PF.toast('URL must start with http:// or https://'); return; }
        var body = { name: name.value.trim() || url.value.trim(), url: url.value.trim(),
          events: ev.value.split(',').map(function (s) { return s.trim(); }).filter(Boolean) };
        if (secret.value) body.secret = secret.value;
        if (h) body.enabled = en.checked;
        var p = h ? PF.api('PUT', '/webhooks/' + encodeURIComponent(h.id), body) : PF.api('POST', '/webhooks', body);
        PF.busy(save, p.then(function () { secret.value = ''; PF.toast('Webhook saved', true); form(null); load(); }));
      });
      var row = PF.el('div', 'btn-row'); row.appendChild(save);
      if (h) row.appendChild(PF.btn('Cancel', '', function () { form(null); }));
      formBox.appendChild(row);
    }

    function load() {
      PF.api('GET', '/webhooks').then(function (hs) {
        hs = hs || [];
        var rows = hs.map(function (h) {
          var acts = PF.el('div', 'btn-row');
          var test = PF.btn('Test', 'sm', function () {
            PF.busy(test, PF.api('POST', '/webhooks/' + encodeURIComponent(h.id) + '/test').then(function (r) {
              if (r && r.ok) PF.toast('Delivered: HTTP ' + r.code + (r.time ? ' (' + r.time + ')' : ''), true);
              else PF.toast('Test failed' + (r && r.code ? ': HTTP ' + r.code : '') + (r && r.error ? ' — ' + r.error : ''));
              load();
            }));
          });
          acts.appendChild(test);
          acts.appendChild(PF.btn('Edit', 'sm', function () { form(h); }));
          var del = PF.btn('Delete', 'sm danger', function () {
            if (!window.confirm('Delete webhook "' + (h.name || h.url) + '"?')) return;
            PF.busy(del, PF.api('DELETE', '/webhooks/' + encodeURIComponent(h.id)).then(function () { PF.toast('Deleted', true); load(); }));
          });
          acts.appendChild(del);
          return [h.name || '', h.url || '', (h.events && h.events.length) ? h.events.join(', ') : 'all',
            PF.pill(h.enabled === false ? 'disabled' : 'enabled', h.enabled === false ? '' : 'ok'),
            lastDelivery(h), acts];
        });
        list.innerHTML = '';
        list.appendChild(PF.table(['Name', 'URL', 'Events', 'State', 'Last delivery', ''], rows, 'No webhooks yet.'));
      }, function (e) { list.innerHTML = ''; list.appendChild(PF.note('Could not load webhooks: ' + e.message, 'bad')); });
    }
    form(null); load();
  };
})();
