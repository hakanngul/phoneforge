/* PhoneForge UI kit: one reusable modal system + small form/table/copy helpers. */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;

  /* ---------- modal (single overlay, reused by every dialog) ---------- */
  var current = null;
  function close() {
    var bg = $('modal'); bg.hidden = true; $('modalBody').innerHTML = '';
    var cb = current && current.onClose; current = null;
    if (cb) cb();
  }
  // PF.modal.open(title, {wide, onClose}) -> {body, close}; fill `body` with nodes.
  PF.modal = {
    open: function (title, opts) {
      if (current) close();
      opts = opts || {};
      current = { onClose: opts.onClose };
      $('modalTitle').textContent = title;
      $('modalBox').className = 'modal' + (opts.wide ? ' wide' : '');
      var body = $('modalBody'); body.innerHTML = '';
      $('modal').hidden = false;
      return { body: body, close: close };
    },
    close: close
  };
  $('modalClose').addEventListener('click', close);
  $('modal').addEventListener('mousedown', function (e) { if (e.target === this) close(); });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !$('modal').hidden) close(); });

  /* ---------- clipboard ---------- */
  PF.copyFallback = function (s) {
    var t = document.createElement('textarea'); t.value = s; document.body.appendChild(t);
    t.select(); try { document.execCommand('copy'); } catch (e) { /* ignore */ } document.body.removeChild(t);
  };
  PF.copyText = function (s, label) {
    var done = function () { PF.toast('Copied ' + (label || 'to clipboard'), true); };
    if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(s).then(done, function () { PF.copyFallback(s); done(); });
    else { PF.copyFallback(s); done(); }
  };

  /* ---------- form helpers ---------- */
  // input(type, placeholder). Secret-ish inputs are never prefilled and never autofilled.
  PF.input = function (type, ph, cls) {
    var i = document.createElement('input');
    i.type = type || 'text'; if (ph) i.placeholder = ph;
    i.autocomplete = type === 'password' ? 'new-password' : 'off';
    i.spellcheck = false;
    if (cls) i.className = cls;
    return i;
  };
  PF.row = function (label, input, hint) {
    var r = PF.el('label', 'frow');
    r.appendChild(PF.el('span', 'flabel', label));
    r.appendChild(input);
    if (hint) r.appendChild(PF.el('span', 'fhint', hint));
    return r;
  };
  PF.btn = function (text, cls, fn) {
    var b = PF.el('button', 'btn' + (cls ? ' ' + cls : ''), text);
    b.type = 'button';
    if (fn) b.addEventListener('click', fn);
    return b;
  };
  PF.pill = function (text, kind) { return PF.el('span', 'pill ' + (kind || ''), text); };
  PF.note = function (text, kind) { return PF.el('div', 'note ' + (kind || ''), text); };
  PF.section = function (title) { return PF.el('h4', 'sec', title); };

  // table(headers, rows): rows are arrays of string | Node. Text is always set via textContent.
  PF.table = function (headers, rows, emptyText) {
    var wrap = PF.el('div', 'tbl-wrap'), t = PF.el('table', 'tbl'), thead = PF.el('thead'), hr = PF.el('tr');
    headers.forEach(function (h) { hr.appendChild(PF.el('th', '', h)); });
    thead.appendChild(hr); t.appendChild(thead);
    var tb = PF.el('tbody');
    if (!rows.length) {
      var er = PF.el('tr'), td = PF.el('td', 'muted', emptyText || 'Nothing here yet.');
      td.colSpan = headers.length; er.appendChild(td); tb.appendChild(er);
    }
    rows.forEach(function (cells) {
      var tr = PF.el('tr');
      cells.forEach(function (c) {
        var td = PF.el('td');
        if (c instanceof Node) td.appendChild(c); else td.textContent = c == null ? '' : String(c);
        tr.appendChild(td);
      });
      tb.appendChild(tr);
    });
    t.appendChild(tb); wrap.appendChild(t);
    return wrap;
  };

  PF.codeBlock = function (text, copyLabel) {
    var w = PF.el('div', 'codeblock');
    w.appendChild(PF.el('pre', '', text));
    w.appendChild(PF.btn('Copy', 'sm', function () { PF.copyText(text, copyLabel); }));
    return w;
  };

  // run an async action on a button: disables it, toasts failures.
  PF.busy = function (btn, promise) {
    btn.disabled = true;
    return promise.catch(function (e) { PF.toast(e.message); }).then(function (v) { btn.disabled = false; return v; });
  };
})();
