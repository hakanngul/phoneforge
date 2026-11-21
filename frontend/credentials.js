/*
 * Credentials / App Cards manager. Secrets are write-only: only names are listed, values are
 * never fetched or rendered. App-card secret fields come back masked and are shown as "secret".
 */
(function () {
  'use strict';
  var PF = window.PF;

  function isMasked(v) { return /^\*+$/.test(String(v == null ? '' : v)); }

  PF.openCredentials = function () {
    var m = PF.modal.open('Credentials / App Cards', { wide: true });
    var b = m.body;
    var secretsBox = PF.el('div'), cardsBox = PF.el('div'), cardForm = PF.el('div');

    b.appendChild(PF.note('Named secrets and app cards are stored on the server. Values are write-only — they are never displayed. App cards let the agent log into apps: the agent references fields by name and the server fills in the secret values.', 'info'));

    /* --- secrets --- */
    b.appendChild(PF.section('Secrets'));
    b.appendChild(secretsBox);
    var sn = PF.input('text', 'NAME (e.g. GITHUB_TOKEN)'), sv = PF.input('password', 'Value');
    var sRow = PF.el('div', 'inline-form');
    sRow.appendChild(sn); sRow.appendChild(sv);
    var sAdd = PF.btn('Save secret', 'primary sm', function () {
      var n = sn.value.trim();
      if (!n || !sv.value) { PF.toast('Name and value are required'); return; }
      PF.busy(sAdd, PF.api('PUT', '/credentials/secrets/' + encodeURIComponent(n), { value: sv.value }).then(function () {
        sn.value = ''; sv.value = ''; PF.toast('Secret saved', true); loadSecrets();
      }));
    });
    sRow.appendChild(sAdd); b.appendChild(sRow);

    function loadSecrets() {
      PF.api('GET', '/credentials/secrets').then(function (r) {
        var names = (r && r.names) || [];
        secretsBox.innerHTML = '';
        secretsBox.appendChild(PF.table(['Name', 'Value', ''], names.map(function (n) {
          var del = PF.btn('Delete', 'sm danger', function () {
            if (!window.confirm('Delete secret ' + n + '?')) return;
            PF.busy(del, PF.api('DELETE', '/credentials/secrets/' + encodeURIComponent(n)).then(function () { PF.toast('Deleted', true); loadSecrets(); }));
          });
          return [n, '•••••• (hidden)', del];
        }), 'No secrets stored.'));
      }, function (e) { secretsBox.innerHTML = ''; secretsBox.appendChild(PF.note('Could not load secrets: ' + e.message, 'bad')); });
    }

    /* --- app cards --- */
    b.appendChild(PF.section('App cards'));
    b.appendChild(cardsBox); b.appendChild(cardForm);

    function loadCards() {
      PF.api('GET', '/credentials/app-cards').then(function (r) {
        var cards = (r && r.cards) || [];
        cardsBox.innerHTML = '';
        cardsBox.appendChild(PF.table(['Bundle ID', 'Label', 'Fields', ''], cards.map(function (c) {
          var vals = c.values || {};
          var fields = PF.el('div');
          (c.fields || []).forEach(function (f) {
            var chip = PF.el('span', 'pill ' + (f.secret ? 'warn' : ''), f.name + (f.secret ? ' (secret)' : (vals[f.name] && !isMasked(vals[f.name]) ? ' = ' + vals[f.name] : '')));
            fields.appendChild(chip); fields.appendChild(document.createTextNode(' '));
          });
          var del = PF.btn('Delete', 'sm danger', function () {
            if (!window.confirm('Delete app card for ' + c.bundleId + '?')) return;
            PF.busy(del, PF.api('DELETE', '/credentials/app-cards/' + encodeURIComponent(c.id)).then(function () { PF.toast('Deleted', true); loadCards(); }));
          });
          return [c.bundleId, c.label || '', fields, del];
        }), 'No app cards yet.'));
      }, function (e) { cardsBox.innerHTML = ''; cardsBox.appendChild(PF.note('Could not load app cards: ' + e.message, 'bad')); });
    }

    function buildForm() {
      cardForm.innerHTML = '';
      cardForm.appendChild(PF.el('h5', 'sub-sec', 'Create app card'));
      var bundle = PF.input('text', 'com.example.app'), label = PF.input('text', 'Label (e.g. Example login)');
      cardForm.appendChild(PF.row('Bundle ID', bundle));
      cardForm.appendChild(PF.row('Label', label));
      var fieldsBox = PF.el('div', 'fields-box'), rows = [];
      function addField(name, secret) {
        var r = PF.el('div', 'inline-form');
        var n = PF.input('text', 'field name (e.g. username)'); n.value = name || '';
        var sc = document.createElement('input'); sc.type = 'checkbox'; sc.checked = !!secret;
        var scl = PF.el('label', 'toggle'); scl.appendChild(sc); scl.appendChild(document.createTextNode(' secret'));
        var v = PF.input(secret ? 'password' : 'text', 'value');
        sc.addEventListener('change', function () { v.type = sc.checked ? 'password' : 'text'; v.autocomplete = sc.checked ? 'new-password' : 'off'; });
        var rm = PF.btn('✕', 'sm', function () { fieldsBox.removeChild(r); rows.splice(rows.indexOf(o), 1); });
        var o = { n: n, sc: sc, v: v };
        rows.push(o);
        [n, scl, v, rm].forEach(function (x) { r.appendChild(x); });
        fieldsBox.appendChild(r);
      }
      addField('username', false); addField('password', true);
      cardForm.appendChild(fieldsBox);
      var row = PF.el('div', 'btn-row');
      row.appendChild(PF.btn('+ Field', 'sm', function () { addField('', false); }));
      var create = PF.btn('Create card', 'primary sm', function () {
        var bid = bundle.value.trim();
        if (!bid) { PF.toast('Bundle ID is required'); return; }
        var fields = [], values = {}, secrets = {};
        for (var i = 0; i < rows.length; i++) {
          var name = rows[i].n.value.trim(); if (!name) continue;
          fields.push({ name: name, secret: rows[i].sc.checked });
          if (rows[i].v.value) { if (rows[i].sc.checked) secrets[name] = rows[i].v.value; else values[name] = rows[i].v.value; }
        }
        if (!fields.length) { PF.toast('Add at least one field'); return; }
        PF.busy(create, PF.api('POST', '/credentials/app-cards', { bundleId: bid, label: label.value.trim(), fields: fields, values: values, secrets: secrets }).then(function () {
          PF.toast('App card created', true); buildForm(); loadCards();
        }));
      });
      row.appendChild(create); cardForm.appendChild(row);
    }
    buildForm(); loadSecrets(); loadCards();
  };
})();
