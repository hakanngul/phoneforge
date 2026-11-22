/* Mirror button rails: hug the phone frame in single mode; in Multi mode or below 900px wide
   both rails move into a compact horizontal bar above the mirror (DOM move only, same buttons/handlers). */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;
  var left = $('railLeft'), right = $('railRight'), bar = $('railBar'), area = $('singleArea');
  var phone = area.querySelector('.phone');
  var mq = window.matchMedia('(max-width: 899px)');
  var flat = null;

  function apply() {
    var wantFlat = !!PF.multi || mq.matches;
    if (wantFlat === flat) return;
    flat = wantFlat;
    if (flat) {
      bar.appendChild(left); bar.appendChild(right);
    } else {
      area.insertBefore(left, phone);
      area.insertBefore(right, phone.nextSibling);
    }
    bar.hidden = !flat;
    left.classList.toggle('flat', flat); right.classList.toggle('flat', flat);
  }
  $('multiToggle').addEventListener('change', function () { setTimeout(apply, 0); });
  if (mq.addEventListener) mq.addEventListener('change', apply); else mq.addListener(apply);
  apply();
  // PF.multi may be restored later (localStorage): setMulti toggles singleArea.hidden
  new MutationObserver(apply).observe(area, { attributes: true, attributeFilter: ['hidden'] });
})();
