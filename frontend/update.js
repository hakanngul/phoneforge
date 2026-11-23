/* Desktop auto-update UI: slim banner + "Updates" modal. The endpoint exists only inside the
 * desktop app; on 404 / network error this file does nothing at all. Server text is only ever
 * placed with textContent. */
(function (w) {
  'use strict';
  var PF = w.PF = w.PF || {};
  var BASE = (PF.API || '/api/v1') + '/update';
  var st = null, banner = null, timer = null, inflight = false, dead = false, started = false;
  var dismissed = '', flash = '', settingsView = null;

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }
  function btn(text, cls, fn) {
    var b = el('button', 'btn ' + (cls || ''), text); b.type = 'button';
    b.addEventListener('click', fn); return b;
  }

  function req(method, path, body) {
    var o = { method: method, headers: {} };
    if (body !== undefined) { o.headers['Content-Type'] = 'application/json'; o.body = JSON.stringify(body); }
    return fetch(BASE + path, o).then(function (r) {
      return r.text().then(function (t) {
        var j = null; try { j = t ? JSON.parse(t) : null; } catch (e) { /* not json */ }
        if (!r.ok) {
          var err = new Error(r.status === 403 ? 'Updates can only be managed on this Mac' : ((j && j.error) || ('HTTP ' + r.status)));
          err.status = r.status; throw err;
        }
        return j;
      });
    });
  }

  function remindActive(s) { return s.remindUntil && new Date(s.remindUntil).getTime() > Date.now(); }
  function busyState(s) { return s && (s.state === 'downloading' || s.state === 'installing' || s.state === 'checking'); }
  function fmtTime(t) { var d = new Date(t); return isNaN(d) ? '' : d.toLocaleString(); }

  /* ---------- polling ---------- */
  function schedule() {
    clearTimeout(timer);
    if (dead) return;
    timer = setTimeout(poll, busyState(st) ? 1000 : 60000);
  }
  function poll() {
    if (dead) return;
    if (document.hidden) { schedule(); return; }
    if (inflight) return;
    inflight = true;
    req('GET', '').then(function (s) { inflight = false; apply(s); schedule(); }, function (e) {
      inflight = false;
      if (!st) { dead = true; return; } // 404 / network on first contact: not the desktop app
      schedule();
    });
  }
  function apply(s) {
    if (!s) return;
    st = s;
    render();
    if (settingsView) settingsView.refresh();
  }
  function act(method, path, body) {
    flash = '';
    return req(method, path, body).then(function (s) { apply(s); schedule(); return s; }, function (e) {
      flash = e.message; render(); if (PF.toast) PF.toast(e.message); schedule();
    });
  }

  /* ---------- banner ---------- */
  function ensureBanner() {
    if (banner) return banner;
    banner = el('div', 'upd-banner'); banner.hidden = true; banner.setAttribute('role', 'status');
    var main = document.querySelector('main');
    document.body.insertBefore(banner, main || document.body.firstChild);
    return banner;
  }
  function key(s) { return s.state + ':' + (s.available ? s.available.version : '') + ':' + (s.rollback ? s.rollback.to : ''); }

  function render() {
    var b = ensureBanner();
    b.textContent = '';
    b.hidden = true;
    if (!st || st.disabled) return;
    var s = st, msg = null, bar = null, actions = [], dismissable = true;
    var ver = s.available ? s.available.version : '';

    if (s.rollback) {
      msg = 'Pocketlab ' + s.rollback.to + ' crashed before it finished starting. Roll back to ' + s.rollback.from + '?';
      actions.push(btn('Roll back', 'primary', function () { act('POST', '/rollback'); }));
      actions.push(btn('Keep', '', function () { dismissed = key(s); render(); }));
      dismissable = false;
    } else if (s.state === 'downloading') {
      var pct = s.progress && s.progress.total > 0 ? Math.round(100 * s.progress.done / s.progress.total) : null;
      msg = 'Downloading Pocketlab ' + ver + (pct != null ? ' (' + pct + '%)' : '…');
      bar = el('progress', 'upd-bar'); bar.max = 100; if (pct != null) bar.value = pct;
      dismissable = false;
    } else if (s.state === 'installing') {
      msg = 'Installing… Pocketlab will restart';
      dismissable = false;
    } else if (s.state === 'ready') {
      msg = 'Update downloaded: Pocketlab ' + ver + ' is ready to install';
      actions.push(btn('Install & Restart', 'primary', function () { act('POST', '/install'); }));
    } else if (s.state === 'error' && s.error && s.available) {
      msg = 'Update failed: ' + s.error;
      actions.push(btn('Retry', 'primary', function () { act('POST', '/install'); }));
    } else if (s.state === 'available' && s.available && !s.skipped && !remindActive(s)) {
      msg = 'Pocketlab ' + ver + ' is available (you have ' + s.current + ')';
      if (s.supported === false) {
        msg += '. In-place install is not possible here; install it manually.';
        actions.push(btn('Download', 'primary', function () { if (s.manualUrl) w.open(s.manualUrl, '_blank', 'noopener'); }));
      } else {
        actions.push(btn('Install & Restart', 'primary', function () { act('POST', '/install'); }));
      }
      actions.push(btn('Release notes', '', function () { openNotes(); }));
      actions.push(btn('Remind me later', '', function () { act('POST', '/remind', { hours: 24 }); }));
      actions.push(btn('Skip this version', '', function () { act('POST', '/skip', { version: ver }); }));
    }
    if (!msg) return;
    if (dismissable && dismissed === key(s)) return;

    b.appendChild(el('span', 'upd-msg', msg));
    if (bar) b.appendChild(bar);
    if (flash) b.appendChild(el('span', 'upd-flash', flash));
    var row = el('span', 'upd-actions');
    actions.forEach(function (a) { row.appendChild(a); });
    var gear = btn('⚙', 'upd-icon', function () { PF.openUpdates(false); });
    gear.title = 'Update settings'; gear.setAttribute('aria-label', 'Update settings');
    row.appendChild(gear);
    if (dismissable) {
      var x = btn('✕', 'upd-icon', function () { dismissed = key(s); render(); });
      x.title = 'Dismiss'; x.setAttribute('aria-label', 'Dismiss');
      row.appendChild(x);
    }
    b.appendChild(row);
    b.hidden = false;
  }

  /* ---------- modals (PF.modal is the shared single overlay; Escape closes it) ---------- */
  function openNotes() {
    if (!st || !st.available || !PF.modal) return;
    var a = st.available;
    var m = PF.modal.open('Pocketlab ' + a.version + ' release notes');
    var pre = el('div', 'upd-notes', a.notesMd || 'No release notes.');
    m.body.appendChild(pre);
    if (a.notesUrl) {
      var p = el('p'), l = el('a', '', 'Full release notes');
      l.href = /^https?:\/\//i.test(a.notesUrl) ? a.notesUrl : '#'; l.target = '_blank'; l.rel = 'noopener noreferrer';
      p.appendChild(l); m.body.appendChild(p);
    }
  }

  function row(label, ctl) {
    var r = el('label', 'upd-row'); r.appendChild(ctl); r.appendChild(el('span', '', label)); return r;
  }

  PF.openUpdates = function (check) {
    if (!PF.modal) return;
    var m = PF.modal.open('Updates', { onClose: function () { settingsView = null; } });
    var result = el('div', 'upd-result'); result.setAttribute('role', 'status');
    var info = el('div', 'upd-info');
    var autoCheck = el('input'); autoCheck.type = 'checkbox';
    var autoDl = el('input'); autoDl.type = 'checkbox';
    var channel = el('select'); ['stable', 'beta'].forEach(function (c) { var o = el('option', '', c); o.value = c; channel.appendChild(o); });
    var checkBtn = btn('Check now', '', function () { runCheck(); });
    var chRow = el('label', 'upd-row'); chRow.appendChild(el('span', '', 'Channel')); chRow.appendChild(channel);

    m.body.appendChild(info);
    m.body.appendChild(row('Check for updates automatically', autoCheck));
    m.body.appendChild(chRow);
    m.body.appendChild(row('Download updates automatically (installing always asks)', autoDl));
    var line = el('div', 'upd-line'); line.appendChild(checkBtn); line.appendChild(result);
    m.body.appendChild(line);

    function save() {
      act('PUT', '/settings', { autoCheck: autoCheck.checked, channel: channel.value, autoDownload: autoDl.checked });
    }
    [autoCheck, autoDl, channel].forEach(function (c) { c.addEventListener('change', save); });

    function summary() {
      if (!st) return '';
      if (st.disabled) return 'Updates are disabled in this build.';
      if (st.state === 'checking') return 'Checking…';
      if (st.state === 'error' && st.error) return 'Error: ' + st.error;
      if (st.state === 'available' || st.state === 'downloading' || st.state === 'ready' || st.state === 'installing')
        return 'Pocketlab ' + (st.available ? st.available.version : '') + ' is available.';
      if (st.state === 'up-to-date') return 'Pocketlab is up to date.';
      return '';
    }
    var view = settingsView = {
      refresh: function () {
        if (!st) return;
        info.textContent = 'Current version: ' + st.current + (st.lastCheck ? ' · Last checked: ' + fmtTime(st.lastCheck) : ' · Never checked');
        if (st.settings) {
          if (document.activeElement !== autoCheck) autoCheck.checked = !!st.settings.autoCheck;
          if (document.activeElement !== autoDl) autoDl.checked = !!st.settings.autoDownload;
          if (document.activeElement !== channel) channel.value = st.settings.channel || 'stable';
        }
        checkBtn.disabled = busyState(st);
        result.textContent = flash || summary();
      }
    };
    function runCheck() {
      result.textContent = 'Checking…'; checkBtn.disabled = true;
      act('POST', '/check').then(function () { view.refresh(); });
    }
    if (st) view.refresh();
    else req('GET', '').then(apply, function (e) { result.textContent = e.message; });
    if (check !== false) runCheck();
    checkBtn.focus();
  };

  /* ---------- start ---------- */
  function start() {
    if (started) return; started = true;
    document.addEventListener('visibilitychange', function () { if (!document.hidden && !dead) { clearTimeout(timer); poll(); } });
    poll();
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start); else start();
})(window);
