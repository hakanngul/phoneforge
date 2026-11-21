/* Crash Reports tool: list crash reports for the selected device (REST /devices/{id}/crashes) and view one. */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var btn = $('crashBtn');
  var state = { reports: [], app: '' };

  function fmtTime(ts) {
    if (!ts || ts.indexOf('0001-') === 0) return '';
    var d = new Date(ts);
    return isNaN(d) ? ts : d.toLocaleString();
  }

  function open() {
    if (!PF.needDevice()) return;
    var dev = PF.selectedDevice();
    var m = PF.modal.open('Crash Reports' + (dev && dev.name ? ' - ' + dev.name : ''), { wide: true });
    var bar = PF.el('div', 'crash-bar');
    var app = PF.input('text', 'Filter by app / bundle id / process', 'grow');
    app.value = state.app;
    var refresh = PF.btn('Refresh', 'sm primary', function () { load(); });
    app.addEventListener('keydown', function (e) { if (e.key === 'Enter') refresh.click(); });
    bar.appendChild(app); bar.appendChild(refresh);
    var list = PF.el('div'), view = PF.el('div');
    m.body.appendChild(bar); m.body.appendChild(list); m.body.appendChild(view);

    function show(r) {
      view.innerHTML = '';
      var head = PF.el('div', 'crash-head');
      head.appendChild(PF.el('strong', 'grow', r.name));
      var pre = PF.el('pre', 'crash-view', 'Loading...');
      var text = '';
      head.appendChild(PF.btn('Copy', 'sm', function () { if (text) PF.copyText(text, 'crash report'); }));
      if (dev && dev.platform === 'ios') {
        head.appendChild(PF.btn('Delete', 'sm', function () {
          if (!window.confirm('Delete ' + r.name + ' from the device?')) return;
          PF.api('DELETE', PF.dev('/crashes/' + encodeURIComponent(r.name))).then(function () {
            PF.toast('Deleted ' + r.name, true); view.innerHTML = ''; load();
          }).catch(function (e) { PF.toast(e.message); });
        }));
      }
      view.appendChild(head); view.appendChild(pre);
      PF.api('GET', PF.dev('/crashes/' + encodeURIComponent(r.name)), undefined, 'text').then(function (t) {
        text = t; pre.textContent = t;
      }).catch(function (e) { pre.textContent = e.message; });
    }

    function render() {
      list.innerHTML = '';
      var rows = state.reports.map(function (r) {
        var cells = [fmtTime(r.timestamp), r.app || r.process || r.bundleId || '(unknown)',
          [r.exceptionType, r.signal].filter(Boolean).join(' / '), r.kind || '', r.source || ''];
        return cells;
      });
      var t = PF.table(['Time', 'App', 'Exception', 'Kind', 'Source'], rows,
        'No crash reports found.');
      t.classList.add('crash-list');
      Array.prototype.forEach.call(t.querySelectorAll('tbody tr'), function (tr, i) {
        if (!state.reports[i]) return;
        tr.className = 'crash-row';
        tr.title = state.reports[i].bundleId || '';
        tr.addEventListener('click', function () { show(state.reports[i]); });
      });
      list.appendChild(t);
    }

    function load() {
      state.app = app.value.trim();
      var q = '?limit=100' + (state.app ? '&app=' + encodeURIComponent(state.app) : '');
      list.innerHTML = ''; list.appendChild(PF.note('Loading crash reports...'));
      PF.busy(refresh, PF.api('GET', PF.dev('/crashes' + q)).then(function (rs) {
        state.reports = rs || []; render();
      }).catch(function (e) {
        list.innerHTML = ''; list.appendChild(PF.note(e.serverMessage || e.message, 'bad'));
      }));
    }
    load();
  }

  btn.addEventListener('click', open);
})();
