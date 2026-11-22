/*
 * Collapsible left (devices) and right (activity/logs) panels of the Automation
 * layout. Collapsing shrinks the aside to a slim rail holding an expand
 * button and the rotated panel name; the center column (flex:1) takes the freed space. State is kept in
 * localStorage (pf.layout.left / pf.layout.right). Shortcuts: Cmd/Ctrl+B (left),
 * Cmd/Ctrl+Alt+B (right), only while the Automation tab is visible.
 * Tap math reads the mirror's bounding rect at click time, so we only fire a
 * window 'resize' after the transition so listeners (e.g. picker highlight) redraw.
 */
(function () {
  'use strict';
  var PF = window.PF = window.PF || {};
  var pane = document.getElementById('automation-tab');
  if (!pane) return;
  var sides = {
    left:  { sel: '.col.devices',  cls: 'pf-l-collapsed', key: 'pf.layout.left',  label: 'devices panel',  title: 'Devices',  hide: 'Hide devices panel (\u2318B)',   show: 'Show devices panel (\u2318B)' },
    right: { sel: '.col.activity', cls: 'pf-r-collapsed', key: 'pf.layout.right', label: 'activity panel', title: 'Activity', hide: 'Hide activity panel (\u2325\u2318B)', show: 'Show activity panel (\u2325\u2318B)' }
  };
  var NS = 'http://www.w3.org/2000/svg';

  function load(k) { try { return localStorage.getItem(k) === '1'; } catch (e) { return false; } }
  function save(k, v) { try { localStorage.setItem(k, v ? '1' : '0'); } catch (e) { /* ignore */ } }
  var fired = 0;
  function notify() { clearTimeout(fired); fired = setTimeout(function () { window.dispatchEvent(new Event('resize')); }, 0); }

  // Sidebar glyph: rounded rect with a divider and a filled strip on the panel's side.
  function icon(name) {
    var svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('viewBox', '0 0 16 16'); svg.setAttribute('width', '16'); svg.setAttribute('height', '16');
    svg.setAttribute('aria-hidden', 'true'); svg.setAttribute('focusable', 'false');
    var x = name === 'left' ? 2 : 9;
    svg.innerHTML = '<rect x="1.5" y="2.5" width="13" height="11" rx="2" fill="none" stroke="currentColor" stroke-width="1.4"/>' +
      '<rect x="' + x + '" y="3.2" width="4.5" height="9.6" fill="currentColor" opacity=".55"/>' +
      '<line x1="' + (name === 'left' ? 6.5 : 9.5) + '" y1="2.5" x2="' + (name === 'left' ? 6.5 : 9.5) + '" y2="13.5" stroke="currentColor" stroke-width="1.4"/>';
    return svg;
  }
  function mkBtn(name, cls) {
    var b = document.createElement('button');
    b.type = 'button'; b.className = 'pf-collapse ' + cls;
    b.appendChild(icon(name));
    return b;
  }

  Object.keys(sides).forEach(function (name) {
    var s = sides[name], aside = pane.querySelector(s.sel);
    if (!aside) return;
    var head = aside.querySelector('.col-head');
    var id = aside.id || (aside.id = 'pf-side-' + name);
    var btn = mkBtn(name, 'pf-collapse-head');
    btn.setAttribute('aria-controls', id);
    if (head) {
      // left: after the refresh icon (right end of header); right: after the live indicator.
      head.appendChild(btn);
      if (name === 'left') {
        var refresh = head.querySelector('#refreshDevices');
        if (refresh) { var grp = document.createElement('span'); grp.className = 'pf-head-actions'; head.insertBefore(grp, refresh); grp.appendChild(refresh); grp.appendChild(btn); }
      } else {
        var live = head.querySelector('#live');
        if (live) { var grp2 = document.createElement('span'); grp2.className = 'pf-head-actions'; head.insertBefore(grp2, live); grp2.appendChild(live); grp2.appendChild(btn); }
      }
    } else { aside.appendChild(btn); }

    var rail = document.createElement('div');
    rail.className = 'pf-rail';
    var rbtn = mkBtn(name, 'pf-collapse-rail');
    rbtn.setAttribute('aria-controls', id);
    var lab = document.createElement('span');
    lab.className = 'pf-rail-label'; lab.textContent = s.title;
    rail.appendChild(rbtn); rail.appendChild(lab);
    aside.appendChild(rail);
    s.aside = aside; s.btn = btn;

    s.set = function (collapsed, persist) {
      s.collapsed = collapsed;
      pane.classList.toggle(s.cls, collapsed);
      btn.setAttribute('aria-label', 'Hide ' + s.label);
      btn.title = s.hide;
      btn.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
      rbtn.setAttribute('aria-label', 'Show ' + s.label);
      rbtn.title = s.show;
      rbtn.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
      rail.title = s.show;
      if (persist) save(s.key, collapsed);
      notify();
    };
    btn.addEventListener('click', function () { s.set(!s.collapsed, true); });
    rail.addEventListener('click', function () { if (s.collapsed) s.set(false, true); });
    aside.addEventListener('transitionend', function (e) { if (e.propertyName === 'width') notify(); });
    s.set(load(s.key), false);
  });

  document.addEventListener('keydown', function (e) {
    if (!(e.metaKey || e.ctrlKey) || e.shiftKey || (e.key !== 'b' && e.key !== 'B' && e.code !== 'KeyB')) return;
    if (pane.hidden) return;
    var s = e.altKey ? sides.right : sides.left;
    if (!s.set) return;
    e.preventDefault();
    s.set(!s.collapsed, true);
  });

  PF.layout = {
    toggle: function (name) { var s = sides[name]; if (s && s.set) s.set(!s.collapsed, true); },
    isCollapsed: function (name) { return !!(sides[name] && sides[name].collapsed); }
  };
})();
