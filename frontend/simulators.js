/* iOS Simulators modal: list, boot/shutdown, start/stop WebDriverAgent, slim/unslim, memory badge,
   clone/erase/delete, create. Booted simulators then appear in the normal device list (sim-<udid>). */
(function () {
  'use strict';
  var PF = window.PF;

  function fmtMB(mb) {
    if (!(mb > 0)) return '';
    if (mb < 1024) return Math.round(mb) + ' MB';
    return (mb / 1024).toFixed(1) + ' GB';
  }

  PF.openSimulators = function () {
    var timer = null, memTimer = null;
    function stopTimers() { clearInterval(timer); clearInterval(memTimer); timer = memTimer = null; }
    var m = PF.modal.open('iOS Simulators', { wide: true, onClose: stopTimers });
    var host = PF.el('div', 'cards');
    var formHost = PF.el('div', 'sim-newwrap');
    var note = PF.el('p', 'muted', 'Boot a simulator, then Start WDA for taps, typing and the UI tree. Live view uses MJPEG while WDA runs, screenshots otherwise.');
    var topRow = PF.el('div', 'btn-row');
    var newBtn = PF.btn('New simulator', 'primary sm', function () { toggleForm(); });
    topRow.appendChild(newBtn);
    m.body.appendChild(note);
    m.body.appendChild(topRow);
    m.body.appendChild(formHost);
    m.body.appendChild(host);

    var busy = {};      // udid -> label while an action is in flight
    var sims = {};      // udid -> last card data
    var built = false;
    var openMenu = null; // {close: fn}

    function refreshDevices() { var rb = document.getElementById('refreshDevices'); if (rb) rb.click(); }
    function errText(e) { return (e && e.message) || String(e); }
    function anyBusy() { for (var k in busy) if (busy.hasOwnProperty(k)) return true; return false; }

    // Run an async action for a card: marks it busy (spinner, disabled controls), toasts, reloads.
    function run(udid, label, fn, ok) {
      busy[udid] = label;
      rerender(udid);
      return fn().then(function () {
        if (ok) PF.toast(ok, true);
      }, function (e) { PF.toast(errText(e), false); }).then(function () {
        delete busy[udid];
        refreshDevices();
        return load();
      });
    }

    function rerender(udid) {
      var s = sims[udid]; if (!s) return;
      var old = host.querySelector('[data-udid="' + udid + '"].card');
      if (old && old.parentNode) old.parentNode.replaceChild(card(s), old);
    }

    function act(btn, udid, method, path, ok) {
      run(udid, 'Working…', function () { return PF.api(method, path); }, ok);
    }

    /* ---------- slim controls ---------- */
    function slimControls(s, base, isBusy) {
      var wrap = PF.el('div', 'sim-slim');
      var booted = s.state === 'Booted';
      var slimmed = !!s.slim;
      var sw = PF.el('button', 'sim-switch');
      sw.type = 'button';
      sw.setAttribute('role', 'switch');
      sw.setAttribute('aria-checked', slimmed ? 'true' : 'false');
      sw.setAttribute('aria-label', 'Slim ' + s.name);
      sw.appendChild(PF.el('span', 'sim-knob'));
      var lbl = PF.el('span', 'sim-slim-label', 'Slim');
      var sel = PF.el('select', 'sim-profile');
      sel.setAttribute('aria-label', 'Slim profile');
      var opts = [['minimal-safe', 'Minimal-safe'], ['aggressive', 'Aggressive']];
      var cur = slimmed ? s.slim.profile : 'minimal-safe';
      if (slimmed && cur !== 'minimal-safe' && cur !== 'aggressive') opts.push([cur, String(cur).charAt(0).toUpperCase() + String(cur).slice(1)]);
      opts.forEach(function (o) {
        var op = document.createElement('option'); op.value = o[0]; op.textContent = o[1];
        if (o[0] === cur) op.selected = true;
        sel.appendChild(op);
      });
      if (slimmed && (cur === 'custom' || cur === 'app')) {
        sel.options[sel.options.length - 1].disabled = true; // read-only label
      }
      var disabled = !booted || isBusy;
      sw.disabled = disabled; sel.disabled = disabled;
      if (!booted) { var tip = 'Boot the simulator to slim it'; wrap.title = tip; sw.title = tip; sel.title = tip; }
      else sw.title = slimmed ? 'Unslim (reboots the simulator)' : 'Slim this simulator';

      sw.addEventListener('click', function () {
        if (sw.disabled) return;
        if (slimmed) {
          run(s.udid, 'Rebooting…', function () { return PF.api('POST', base + '/unslim'); }, s.name + ' unslimmed');
        } else {
          run(s.udid, 'Slimming…', function () { return PF.api('POST', base + '/slim', { profile: sel.value }); }, s.name + ' slimmed');
        }
      });
      sel.addEventListener('change', function () {
        if (!slimmed) return; // profile is just the choice for the next slim
        run(s.udid, 'Applying…', function () { return PF.api('POST', base + '/slim', { profile: sel.value }); }, 'Profile applied');
      });
      wrap.appendChild(sw); wrap.appendChild(lbl); wrap.appendChild(sel);
      if (isBusy) {
        var sp = PF.el('span', 'sim-spinner'); sp.setAttribute('role', 'status'); sp.setAttribute('aria-label', busy[s.udid]);
        wrap.appendChild(sp);
        wrap.appendChild(PF.el('span', 'muted sim-busy-label', busy[s.udid]));
      }
      if (slimmed && s.slim.rebootNeeded) {
        var h = PF.el('span', 'muted sim-hint', 'Reboot needed to fully apply');
        h.title = 'Some background daemons could not be disabled or enabled while the simulator is running. Reboot it to fully apply the change.';
        wrap.appendChild(h);
      }
      return wrap;
    }

    /* ---------- memory badge ---------- */
    function setBadge(el, mb, slim) {
      el.innerHTML = '';
      var txt = fmtMB(mb);
      el.appendChild(document.createTextNode(txt || '…'));
      el.title = 'Memory in use';
      el.dataset.mb = mb > 0 ? String(mb) : '';
      if (slim && slim.baselineMB > 0) {
        el.title = 'was ' + fmtMB(slim.baselineMB);
        if (mb > 0 && mb < slim.baselineMB) {
          var pct = Math.round((1 - mb / slim.baselineMB) * 100);
          if (pct > 0) el.appendChild(PF.el('span', 'sim-mem-delta', ' −' + pct + '%'));
        }
      }
    }

    function refreshMemory() {
      if (!document.body.contains(host)) { stopTimers(); return; }
      var els = host.querySelectorAll('.sim-mem[data-udid]');
      Array.prototype.forEach.call(els, function (el) {
        var udid = el.getAttribute('data-udid');
        PF.api('GET', '/simulators/' + encodeURIComponent(udid) + '/memory').then(function (r) {
          if (!document.body.contains(el)) return;
          var mb = r && (r.footprintMB || r.rssMB);
          if (mb > 0) setBadge(el, mb, sims[udid] && sims[udid].slim);
        }, function () { /* keep last value */ });
      });
    }

    /* ---------- overflow menu ---------- */
    function closeMenu() { if (openMenu) { var o = openMenu; openMenu = null; o.close(); } }

    function overflow(s, base, isBusy) {
      var wrap = PF.el('div', 'sim-menu-wrap');
      var btn = PF.el('button', 'sim-menu-btn', '⋯');
      btn.type = 'button';
      btn.setAttribute('aria-label', 'More actions');
      btn.setAttribute('aria-haspopup', 'menu');
      btn.setAttribute('aria-expanded', 'false');
      btn.disabled = isBusy;
      wrap.appendChild(btn);
      function items() {
        var name = s.name, booted = s.state === 'Booted';
        return [
          ['Clone…', false, function () {
            var msg = (booted ? 'Cloning needs ' + name + ' shut down. Shut it down and clone it? WebDriverAgent on it will stop.' : 'Clone ' + name + '?') +
              (s.slim ? '\nThe clone will be slimmed with the same profile.' : '');
            PF.confirm(msg, function () {
              run(s.udid, 'Cloning…', function () {
                return PF.api('POST', base + '/clone', { name: name + ' clone', count: 1, shutdown: true });
              }, name + ' cloned');
            }, { title: 'Clone simulator?', okLabel: 'Clone' });
          }],
          ['Erase…', true, function () {
            PF.confirm('All apps, data and settings on ' + name + ' will be permanently erased.' + (booted ? '\n' + name + ' will be shut down first.' : '') + '\nSlimming is kept.', function () {
              run(s.udid, 'Erasing…', function () {
                return PF.api('POST', base + '/erase', { keepSlim: true, shutdown: true });
              }, name + ' erased');
            }, { title: 'Erase simulator?', okLabel: 'Erase', danger: true });
          }],
          ['Delete…', true, function () {
            PF.confirm(name + ' will be permanently deleted, with all its data. This can’t be undone.', function () {
              run(s.udid, 'Deleting…', function () {
                return PF.api('DELETE', base + '?shutdown=1');
              }, name + ' deleted');
            }, { title: 'Delete simulator?', okLabel: 'Delete', danger: true });
          }]
        ];
      }
      function open() {
        closeMenu();
        var menu = PF.el('div', 'sim-menu'); menu.setAttribute('role', 'menu');
        var els = [];
        items().forEach(function (it) {
          var b = PF.el('button', 'sim-menu-item' + (it[1] ? ' danger' : ''), it[0]);
          b.type = 'button'; b.setAttribute('role', 'menuitem');
          b.addEventListener('click', function () { closeMenu(); btn.focus(); it[2](); });
          menu.appendChild(b); els.push(b);
        });
        wrap.appendChild(menu);
        btn.setAttribute('aria-expanded', 'true');
        function onDoc(e) { if (!wrap.contains(e.target)) closeMenu(); }
        function onKey(e) {
          if (e.key === 'Escape') { e.stopPropagation(); e.preventDefault(); closeMenu(); btn.focus(); return; }
          var i = els.indexOf(document.activeElement);
          if (e.key === 'ArrowDown') { e.preventDefault(); els[(i + 1) % els.length].focus(); }
          else if (e.key === 'ArrowUp') { e.preventDefault(); els[(i - 1 + els.length) % els.length].focus(); }
          else if (e.key === 'Tab') closeMenu();
        }
        document.addEventListener('mousedown', onDoc, true);
        document.addEventListener('keydown', onKey, true);
        openMenu = { close: function () {
          document.removeEventListener('mousedown', onDoc, true);
          document.removeEventListener('keydown', onKey, true);
          btn.setAttribute('aria-expanded', 'false');
          if (menu.parentNode) menu.parentNode.removeChild(menu);
        } };
        els[0].focus();
      }
      btn.addEventListener('click', function () { if (openMenu) closeMenu(); else open(); });
      return wrap;
    }

    /* ---------- card ---------- */
    function card(s) {
      sims[s.udid] = s;
      var isBusy = !!busy[s.udid];
      var c = PF.el('div', 'card');
      c.setAttribute('data-udid', s.udid);
      var head = PF.el('div', 'card-head sim-head');
      head.appendChild(PF.el('strong', '', s.name));
      head.appendChild(PF.pill(s.runtime, ''));
      head.appendChild(PF.pill(s.state, s.state === 'Booted' ? 'ok' : ''));
      var wda = s.wda || {};
      if (s.state === 'Booted') head.appendChild(PF.pill('WDA: ' + (wda.state || 'stopped'), wda.state === 'running' ? 'ok' : ''));
      if (s.state === 'Booted' && s.platform === 'iOS') {
        var mem = PF.el('span', 'pill sim-mem'); mem.setAttribute('data-udid', s.udid);
        setBadge(mem, s.memoryMB, s.slim);
        head.appendChild(mem);
      }
      var base = '/simulators/' + encodeURIComponent(s.udid);
      head.appendChild(overflow(s, base, isBusy));
      c.appendChild(head);
      var row = PF.el('div', 'btn-row');
      if (s.state !== 'Booted') {
        var bb = PF.btn('Boot', 'primary sm', function (e) { act(e.currentTarget, s.udid, 'POST', base + '/boot', s.name + ' booted'); });
        bb.disabled = isBusy; row.appendChild(bb);
      } else {
        var sb = PF.btn('Shutdown', 'sm danger', function (e) { act(e.currentTarget, s.udid, 'POST', base + '/shutdown', s.name + ' shut down'); });
        sb.disabled = isBusy; row.appendChild(sb);
        if (s.platform === 'iOS') {
          if (wda.state === 'running') {
            var xb = PF.btn('Stop WDA', 'sm', function (e) { act(e.currentTarget, s.udid, 'POST', base + '/wda/stop', 'WDA stopped'); });
            xb.disabled = isBusy; row.appendChild(xb);
          } else {
            var b = PF.btn('Start WDA', 'primary sm', function (e) { act(e.currentTarget, s.udid, 'POST', base + '/wda/start', 'WDA running'); });
            b.disabled = !built || wda.state === 'starting' || isBusy;
            row.appendChild(b);
          }
        }
      }
      c.appendChild(row);
      if (s.platform === 'iOS') c.appendChild(slimControls(s, base, isBusy));
      else if (isBusy) {
        var w2 = PF.el('div', 'sim-slim'); w2.appendChild(PF.el('span', 'sim-spinner')); w2.appendChild(PF.el('span', 'muted', busy[s.udid])); c.appendChild(w2);
      }
      if (wda.error) c.appendChild(PF.note(wda.error, 'bad'));
      return c;
    }

    /* ---------- create form ---------- */
    function toggleForm() {
      if (formHost.firstChild) { formHost.innerHTML = ''; return; }
      var f = PF.el('div', 'card sim-form');
      var msg = PF.el('p', 'muted', 'Loading options…'); f.appendChild(msg);
      formHost.appendChild(f);
      PF.api('GET', '/simulators/catalog').then(function (cat) {
        if (!f.parentNode) return;
        f.innerHTML = '';
        var name = PF.input('text', 'Name'); name.value = 'Pocketlab sim';
        var dt = PF.el('select'), rt = PF.el('select');
        ((cat && cat.deviceTypes) || []).forEach(function (d) { var o = document.createElement('option'); o.value = d.id; o.textContent = d.name; dt.appendChild(o); });
        ((cat && cat.runtimes) || []).forEach(function (r) {
          if (r.available === false) return;
          var o = document.createElement('option'); o.value = r.id; o.textContent = r.name; rt.appendChild(o);
        });
        // prefer the newest runtime (last in list) and an iPhone device type
        if (rt.options.length) rt.selectedIndex = rt.options.length - 1;
        var slim = document.createElement('input'); slim.type = 'checkbox';
        var prof = PF.el('select', 'sim-profile');
        [['minimal-safe', 'Minimal-safe'], ['aggressive', 'Aggressive']].forEach(function (o) { var op = document.createElement('option'); op.value = o[0]; op.textContent = o[1]; prof.appendChild(op); });
        var boot = document.createElement('input'); boot.type = 'checkbox'; boot.checked = true;
        function chk(input, text, extra) {
          var l = PF.el('label', 'sim-check'); l.appendChild(input); l.appendChild(PF.el('span', '', text));
          if (extra) l.appendChild(extra); return l;
        }
        f.appendChild(PF.row('Name', name));
        f.appendChild(PF.row('Device type', dt));
        f.appendChild(PF.row('Runtime', rt));
        f.appendChild(chk(slim, 'Slim', prof));
        f.appendChild(chk(boot, 'Boot after creating'));
        var row = PF.el('div', 'btn-row');
        var status = PF.el('span', 'sim-slim');
        var create = PF.btn('Create', 'primary sm', function () {
          if (!name.value.trim() || !dt.value || !rt.value) { PF.toast('Name, device type and runtime are required', false); return; }
          var body = { name: name.value.trim(), deviceType: dt.value, runtime: rt.value, slim: slim.checked, profile: prof.value, boot: boot.checked };
          create.disabled = cancel.disabled = true;
          var sp = PF.el('span', 'sim-spinner'); status.appendChild(sp);
          status.appendChild(PF.el('span', 'muted', 'Creating… this can take a minute'));
          PF.api('POST', '/simulators', body).then(function () {
            PF.toast('Simulator created', true);
            formHost.innerHTML = '';
            refreshDevices();
            return load();
          }, function (e) {
            PF.toast(errText(e), false);
            create.disabled = cancel.disabled = false; status.innerHTML = '';
          });
        });
        var cancel = PF.btn('Cancel', 'sm', function () { formHost.innerHTML = ''; });
        row.appendChild(create); row.appendChild(cancel); row.appendChild(status);
        f.appendChild(row);
        name.focus();
      }, function (e) { f.innerHTML = ''; f.appendChild(PF.note('Could not load options: ' + errText(e), 'bad')); });
    }

    function load() {
      return PF.api('GET', '/simulators').then(function (r) {
        closeMenu();
        host.innerHTML = '';
        built = !!(r && r.wdaBuilt);
        if (!built) {
          var nb = PF.note('WDA is not built for the simulator yet. Pocketlab builds it on this Mac with Xcode (no signing needed, a few minutes the first time).', 'info');
          var bb = PF.btn('Build WDA', 'primary sm', function () {
            PF.confirm('Build WebDriverAgent for the iOS Simulator now?\nIt needs Xcode and takes a few minutes the first time.', function () {
              stopTimers();
              PF.wdaBuild.open({ mode: 'sim', onSuccess: function () { PF.toast('Simulator WDA built', true); PF.openSimulators(); }, onClose: PF.openSimulators });
            }, { title: 'Build WDA?', okLabel: 'Build' });
          });
          bb.style.marginTop = '6px'; bb.style.display = 'block';
          nb.appendChild(bb);
          host.appendChild(nb);
        }
        var list = (r && r.simulators) || [];
        sims = {};
        if (!list.length) host.appendChild(PF.el('p', 'muted', 'No simulators found (is Xcode installed?).'));
        list.forEach(function (s) { host.appendChild(card(s)); });
      }, function (e) { host.innerHTML = ''; host.appendChild(PF.note('Could not list simulators: ' + e.message, 'bad')); });
    }

    // Periodic reload must not destroy an open menu, a focused dropdown or an in-flight spinner.
    function periodic() {
      if (!document.body.contains(host)) { stopTimers(); return; }
      var ae = document.activeElement;
      if (openMenu || anyBusy() || (ae && ae.tagName === 'SELECT' && host.contains(ae))) return;
      load();
    }
    load();
    timer = setInterval(periodic, 5000);
    memTimer = setInterval(refreshMemory, 10000);
  };
})();
