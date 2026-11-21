/* Integrations dropdown -> modals (AI Tools, API Server, Cloud Agents; others live in their own files). */
(function () {
  'use strict';
  var PF = window.PF, $ = PF.$;

  /* ---------- AI Tools: LLM provider settings via the secrets API (values write-only) ---------- */
  var AI_KEYS = [
    ['ANTHROPIC_API_KEY', 'Anthropic API key', true],
    ['ANTHROPIC_MODEL', 'Anthropic model', false, 'e.g. claude-sonnet-4-5'],
    ['OLLAMA_HOST', 'Ollama host', false, 'e.g. http://127.0.0.1:11434'],
    ['OLLAMA_MODEL', 'Ollama model', false, 'e.g. llama3.1']
  ];
  function openAI() {
    var m = PF.modal.open('AI Tools');
    m.body.appendChild(PF.note('Saved settings are used by the agent after a server restart. Values are write-only and are never displayed; only whether each one is set.', 'info'));
    var host = PF.el('div');
    m.body.appendChild(host);
    function load() {
      PF.api('GET', '/credentials/secrets').then(function (r) { return (r && r.names) || []; }, function (e) {
        PF.toast('Could not load settings: ' + e.message); return null;
      }).then(function (names) {
        host.innerHTML = '';
        AI_KEYS.forEach(function (k) {
          var isSet = names ? names.indexOf(k[0]) >= 0 : false;
          var c = PF.el('div', 'card');
          var head = PF.el('div', 'card-head');
          head.appendChild(PF.el('strong', '', k[0]));
          head.appendChild(PF.pill(names ? (isSet ? 'set' : 'not set') : 'unknown', isSet ? 'ok' : ''));
          c.appendChild(head);
          var i = PF.input(k[2] ? 'password' : 'text', isSet ? 'set — enter to replace' : (k[3] || k[1]));
          c.appendChild(PF.row(k[1], i));
          var row = PF.el('div', 'btn-row');
          var save = PF.btn('Save', 'primary sm', function () {
            if (!i.value.trim()) { PF.toast('Enter a value first', true); return; }
            PF.busy(save, PF.api('PUT', '/credentials/secrets/' + encodeURIComponent(k[0]), { value: i.value.trim() }).then(function () {
              i.value = ''; PF.toast(k[0] + ' saved. Restart the server to apply.', true); load();
            }));
          });
          var clr = PF.btn('Clear', 'sm danger', function () {
            PF.busy(clr, PF.api('DELETE', '/credentials/secrets/' + encodeURIComponent(k[0])).then(function () { PF.toast(k[0] + ' cleared', true); load(); }));
          });
          clr.disabled = !isSet;
          row.appendChild(save); row.appendChild(clr); c.appendChild(row);
          host.appendChild(c);
        });
      });
    }
    load();
  }

  /* ---------- API Server ---------- */
  function openAPI() {
    var m = PF.modal.open('API Server');
    var base = location.protocol + '//' + location.host;
    var rest = base + '/api/v1', mcp = base + '/mcp';
    var b = m.body;
    b.appendChild(PF.section('REST base URL')); b.appendChild(PF.codeBlock(rest, 'REST URL'));
    b.appendChild(PF.section('MCP URL')); b.appendChild(PF.codeBlock(mcp, 'MCP URL'));
    b.appendChild(PF.section('MCP client config'));
    b.appendChild(PF.codeBlock(JSON.stringify({ mcpServers: { phoneforge: { type: 'http', url: mcp } } }, null, 2), 'MCP config'));
    var tok = PF.el('p', 'muted', 'Checking token requirement...');
    b.appendChild(PF.section('Authentication')); b.appendChild(tok);
    PF.api('GET', '/health').then(function (h) {
      var req = h.authRequired != null ? h.authRequired : h.tokenRequired != null ? h.tokenRequired : h.auth;
      if (req === true || req === 'required') tok.textContent = 'A token is required. Send it as "Authorization: Bearer <token>".';
      else if (req === false || req === 'none') tok.textContent = 'No token required. Only bind the server to a non-loopback address together with -token.';
      else tok.textContent = 'The server does not report its auth mode. This page works without a token; if you start phoneforged with -token <token>, clients must send "Authorization: Bearer <token>".';
    }, function () { tok.textContent = 'Could not read /health.'; });
  }

  /* ---------- Cloud Agents ---------- */
  function openCloud() {
    var m = PF.modal.open('Cloud Agents');
    m.body.appendChild(PF.note('CI-based cloud agents — coming soon', 'info'));
    m.body.appendChild(PF.el('p', 'muted', 'Cloud agents will run PhoneForge automation from your CI pipeline against cloud device farms, so scheduled and pull-request test runs do not need a machine with phones attached. Nothing here is available yet.'));
  }

  var ACTIONS = { aiconfig: function () { PF.openAIConfig(); }, ai: openAI, api: openAPI, webhooks: function () { PF.openWebhooks(); }, farms: function () { PF.openFarms(); }, sims: function () { PF.openSimulators(); },
    creds: function () { PF.openCredentials(); }, cloud: openCloud };
  var menu = $('intMenu');
  $('intBtn').addEventListener('click', function (e) { e.stopPropagation(); menu.hidden = !menu.hidden; });
  document.addEventListener('click', function () { menu.hidden = true; });
  Array.prototype.forEach.call(menu.querySelectorAll('button'), function (b) {
    b.addEventListener('click', function () { var f = ACTIONS[b.getAttribute('data-int')]; if (f) f(); });
  });
})();
