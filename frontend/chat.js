/*
 * Collapsible agent chat column (right of the phone in the Automation tab).
 * Moves nothing: only adds a header collapse button and a slim rail, and remembers the state in
 * localStorage (pf.layout.chat). Collapsing is only meaningful in the side-column layout (wide
 * single mode); in Multi mode / narrow windows CSS shows the chat as a bottom row instead.
 */
(function () {
  'use strict';
  var PF = window.PF = window.PF || {};
  var pane = document.getElementById('automation-tab'), col = document.getElementById('agentCol');
  if (!pane || !col) return;
  var KEY = 'pf.layout.chat', NS = 'http://www.w3.org/2000/svg';
  function load() { try { return localStorage.getItem(KEY) === '1'; } catch (e) { return false; } }
  function save(v) { try { localStorage.setItem(KEY, v ? '1' : '0'); } catch (e) { /* ignore */ } }
  function icon() {
    var svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('viewBox', '0 0 16 16'); svg.setAttribute('width', '16'); svg.setAttribute('height', '16');
    svg.setAttribute('aria-hidden', 'true');
    svg.innerHTML = '<rect x="1.5" y="2.5" width="13" height="11" rx="2" fill="none" stroke="currentColor" stroke-width="1.4"/>' +
      '<rect x="9" y="3.2" width="4.5" height="9.6" fill="currentColor" opacity=".55"/><line x1="9.5" y1="2.5" x2="9.5" y2="13.5" stroke="currentColor" stroke-width="1.4"/>';
    return svg;
  }
  function mk(cls) { var b = document.createElement('button'); b.type = 'button'; b.className = 'pf-collapse ' + cls; b.appendChild(icon()); b.setAttribute('aria-controls', 'agentCol'); return b; }
  var btn = mk('pf-collapse-head chat-collapse');
  col.querySelector('.agent-head').appendChild(btn);
  var rail = document.createElement('div'); rail.className = 'pf-rail';
  var rbtn = mk('pf-collapse-rail'), lab = document.createElement('span');
  lab.className = 'pf-rail-label'; lab.textContent = 'Chat';
  rail.appendChild(rbtn); rail.appendChild(lab); col.appendChild(rail);
  var collapsed = false, t = 0;
  function notify() { clearTimeout(t); t = setTimeout(function () { window.dispatchEvent(new Event('resize')); }, 0); }
  function set(c, persist) {
    collapsed = c;
    pane.classList.toggle('pf-c-collapsed', c);
    btn.title = 'Hide chat'; btn.setAttribute('aria-label', 'Hide chat'); btn.setAttribute('aria-expanded', c ? 'false' : 'true');
    rbtn.title = rail.title = 'Show chat'; rbtn.setAttribute('aria-label', 'Show chat'); rbtn.setAttribute('aria-expanded', c ? 'false' : 'true');
    if (persist) save(c);
    notify();
  }
  btn.addEventListener('click', function () { set(!collapsed, true); });
  rail.addEventListener('click', function () { if (collapsed) set(false, true); });
  col.addEventListener('transitionend', function (e) { if (e.propertyName === 'width') notify(); });
  set(load(), false);
  PF.chatPanel = { toggle: function () { set(!collapsed, true); }, isCollapsed: function () { return collapsed; } };
})();
