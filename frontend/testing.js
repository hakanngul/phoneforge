/* Testing tab: Quick run (one-off JSON DSL runner) + sub-tab switch. Workspace lives in tests.js.
   Original description: DSL editor, runner, in-memory history. */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var history = [];
  var SAMPLE = {
    version: '0.2',
    steps: [
      { action: 'screenshot' },
      { action: 'tap', selector: { text: 'Settings' } },
      { action: 'assert_visible', selector: { textContains: 'Wi' } }
    ]
  };
  $('dslBody').value = JSON.stringify(SAMPLE, null, 2);

  function chipClass(s) {
    s = String(s || '').toLowerCase();
    if (s === 'ok' || s === 'passed' || s === 'success') return 'ok';
    if (s === 'failed' || s === 'error' || s === 'fail') return 'failed';
    return 'skipped';
  }

  function show(r) {
    var out = $('dslResult');
    out.className = 'results';
    out.innerHTML = '';
    out.appendChild(PF.el('div', 'sum', r && r.passed ? 'PASSED' : 'FAILED')).style.color = r && r.passed ? 'var(--ok)' : 'var(--bad)';
    ((r && r.steps) || []).forEach(function (s) {
      var row = PF.el('div', 'step');
      row.appendChild(PF.el('span', 'muted', '#' + s.index));
      row.appendChild(PF.el('span', 'chip ' + chipClass(s.status), s.status || '?'));
      row.appendChild(PF.el('strong', '', s.action));
      if (s.matched) row.appendChild(PF.el('span', 'muted', 'matched [' + [s.matched.x, s.matched.y, s.matched.w, s.matched.h].join(',') + ']'));
      if (s.error) row.appendChild(PF.el('div', 'err', s.error));
      out.appendChild(row);
    });
  }

  function renderHistory() {
    var ul = $('dslHistory');
    ul.innerHTML = '';
    history.forEach(function (h) {
      var li = PF.el('li');
      li.appendChild(PF.el('span', 'chip ' + (h.result && h.result.passed ? 'ok' : 'failed'), h.result && h.result.passed ? 'pass' : 'fail'));
      li.appendChild(PF.el('span', '', h.time + ' - ' + ((h.result && h.result.steps) || []).length + ' steps'));
      li.onclick = function () { show(h.result); $('dslBody').value = h.body; };
      ul.appendChild(li);
    });
  }

  $('dslRun').addEventListener('click', function () {
    if (!PF.needDevice()) return;
    var body = $('dslBody').value;
    try { JSON.parse(body); } catch (e) { PF.toast('Invalid JSON: ' + e.message); return; }
    var out = $('dslResult');
    out.className = 'results muted'; out.textContent = 'Running...';
    var btn = $('dslRun'); btn.disabled = true;
    PF.api('POST', PF.dev('/dsl/execute'), body).then(function (r) {
      show(r);
      history.unshift({ time: new Date().toLocaleTimeString(), body: body, result: r });
      history = history.slice(0, 20);
      renderHistory();
    }).catch(function (e) { out.textContent = e.message; PF.toast(e.message); })
      .then(function () { btn.disabled = false; });
  });

  /* sub-tabs: Workspace | Quick run */
  Array.prototype.forEach.call(document.querySelectorAll('#testSubTabs .subtab'), function (b) {
    b.addEventListener('click', function () {
      var id = b.getAttribute('data-tsub');
      Array.prototype.forEach.call(document.querySelectorAll('#testSubTabs .subtab'), function (x) { x.classList.toggle('active', x === b); });
      $('wsView').hidden = id !== 'wsView'; $('quickView').hidden = id !== 'quickView';
      $('flowsView').hidden = id !== 'flowsView';
      if (id === 'flowsView' && PF.loadFlows) PF.loadFlows(); else if (PF.leaveFlows) PF.leaveFlows();
    });
  });
})();
