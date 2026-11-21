/* Web tool: drive Chrome / WebView pages on the selected Android device over CDP (REST /web/devices/{id}/...). */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var pop = $('webPop'), btn = $('webBtn');
  var built = false, F = {};

  function base() { return '/web/devices/' + encodeURIComponent(PF.selectedId); }
  function target() { return F.targets.value; }
  function withTarget(o) { var t = target(); if (t) o.targetId = t; return o; }
  function out(v, isErr) {
    var s = typeof v === 'string' ? v : JSON.stringify(v, null, 2);
    F.out.textContent = s; F.out.style.color = isErr ? 'var(--bad)' : '';
    F.out.hidden = false;
  }
  function call(button, method, path, body, label) {
    if (!PF.needDevice()) return;
    PF.busy(button, PF.api(method, base() + path, body).then(function (r) {
      out(r == null ? (label || 'ok') : r, false);
      return r;
    }).catch(function (e) { out(e.message, true); throw e; }));
  }

  function loadTargets() {
    if (!PF.needDevice()) return;
    var keep = F.targets.value;
    PF.busy(F.refresh, PF.api('GET', base() + '/targets').then(function (ts) {
      F.targets.innerHTML = '';
      var d = document.createElement('option'); d.value = ''; d.textContent = ts && ts.length ? '(first page)' : '(no pages found)';
      F.targets.appendChild(d);
      (ts || []).forEach(function (t) {
        var o = document.createElement('option'); o.value = t.id || '';
        o.textContent = (t.type || 'page') + ': ' + (t.title || t.url || t.id || '').slice(0, 60) + (t.url && t.title ? ' - ' + t.url.slice(0, 50) : '');
        F.targets.appendChild(o);
      });
      if (keep) F.targets.value = keep;
      if (F.targets.selectedIndex < 0) F.targets.selectedIndex = 0;
      out((ts || []).length + ' target(s)', false);
    }).catch(function (e) {
      F.targets.innerHTML = ''; out(e.message + ' (needs an Android device with a debuggable Chrome/WebView open)', true);
    }));
  }

  function row() { return PF.el('div', 'webrow'); }

  function build() {
    built = true;
    pop.innerHTML = '';
    var top = row();
    F.targets = document.createElement('select'); F.targets.className = 'grow';
    F.refresh = PF.btn('Targets', 'sm', loadTargets);
    top.appendChild(F.targets); top.appendChild(F.refresh); top.appendChild(PF.btn('Close', 'sm', function () { pop.hidden = true; }));
    pop.appendChild(top);

    var r1 = row();
    F.url = PF.input('text', 'https://example.com', 'grow');
    var go = PF.btn('Go', 'sm primary', function () {
      var u = F.url.value.trim(); if (!u) { PF.toast('Enter a URL first', true); return; }
      call(go, 'POST', '/navigate', withTarget({ url: u }), 'navigated');
    });
    F.url.addEventListener('keydown', function (e) { if (e.key === 'Enter') go.click(); });
    r1.appendChild(F.url); r1.appendChild(go); pop.appendChild(r1);

    var r2 = row();
    F.sel = PF.input('text', 'CSS selector, e.g. #login', 'grow');
    F.txt = PF.input('text', 'Text to type', 'grow');
    var click = PF.btn('Click', 'sm', function () {
      var s = F.sel.value.trim(); if (!s) { PF.toast('Enter a CSS selector first', true); return; }
      call(click, 'POST', '/click', withTarget({ selector: s }), 'clicked');
    });
    var type = PF.btn('Type', 'sm', function () {
      var s = F.sel.value.trim(); if (!s) { PF.toast('Enter a CSS selector first', true); return; }
      call(type, 'POST', '/type', withTarget({ selector: s, text: F.txt.value }), 'typed');
    });
    [F.sel, F.txt, click, type].forEach(function (n) { r2.appendChild(n); });
    pop.appendChild(r2);

    var r3 = row(); r3.style.alignItems = 'flex-start';
    F.js = document.createElement('textarea'); F.js.rows = 2; F.js.className = 'grow'; F.js.spellcheck = false;
    F.js.placeholder = 'JavaScript, e.g. document.title';
    var ev = PF.btn('Eval', 'sm', function () {
      var s = F.js.value.trim(); if (!s) { PF.toast('Enter a script first', true); return; }
      call(ev, 'POST', '/eval', withTarget({ script: s }));
    });
    r3.appendChild(F.js); r3.appendChild(ev); pop.appendChild(r3);

    F.out = PF.el('pre', 'codeblock web-out'); F.out.hidden = true; pop.appendChild(F.out);
  }

  btn.addEventListener('click', function () {
    if (!pop.hidden) { pop.hidden = true; return; }
    $('ocrPop').hidden = true; $('clipPop').hidden = true;
    if (!PF.needDevice()) return;
    var d = PF.selectedDevice();
    if (d && d.platform !== 'android') { PF.toast('Web automation needs an Android device', true); return; }
    if (!built) build();
    pop.hidden = false;
    loadTargets();
  });
  PF.hooks.onSelect.push(function () { pop.hidden = true; if (built) { F.targets.innerHTML = ''; F.out.hidden = true; } });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !pop.hidden && $('modal').hidden) pop.hidden = true; });
})();
