/* AI Configuration panel: external coding-agent CLIs, agent types, settings, and a run console.
   PF.renderAIConfig(container) renders into any element; PF.openAIConfig() shows it in the modal. */
(function () {
  'use strict';
  var PF = window.PF;

  var TYPES = [
    { id: 'automation', label: 'Automation', hint: 'Acts on the selected device through the MCP tools.' },
    { id: 'testing', label: 'Testing', hint: 'Writes, runs and fixes .mob test scripts.' }
  ];

  // Pull readable text out of a stream-json line (Claude/Cursor); plain lines pass through.
  function lineText(line) {
    var j;
    try { j = JSON.parse(line); } catch (e) { return line; }
    if (!j || typeof j !== 'object') return line;
    if (j.type === 'assistant' && j.message && Array.isArray(j.message.content)) {
      return j.message.content.map(function (c) {
        if (c.type === 'text') return c.text;
        if (c.type === 'tool_use') return '> ' + c.name + ' ' + JSON.stringify(c.input || {});
        return '';
      }).filter(Boolean).join('\n');
    }
    if (j.type === 'result') return '[result] ' + (j.result || j.subtype || '');
    if (j.type === 'system') return '[system] ' + (j.subtype || '') + (j.model ? ' ' + j.model : '');
    return null; // noisy event (tool results, user echoes): skip
  }

  PF.renderAIConfig = function (root) {
    root.innerHTML = '';
    var state = { agents: [], settings: null, runId: null, es: null };
    var cardsBox = PF.el('div'), setBox = PF.el('div'), runBox = PF.el('div');
    root.appendChild(PF.el('p', 'muted', 'Choose which coding agent drives the phone. External agents connect to PhoneForge through its MCP server; connecting only happens when you click the button.'));
    root.appendChild(PF.section('Agents'));
    root.appendChild(cardsBox);
    root.appendChild(PF.section('Agent types & settings'));
    root.appendChild(setBox);
    root.appendChild(PF.section('Run console'));
    root.appendChild(runBox);

    function agentLabel(n) {
      for (var i = 0; i < state.agents.length; i++) if (state.agents[i].name === n) return state.agents[i].label;
      return n;
    }

    /* ---------- agent cards ---------- */
    function loadAgents(refresh) {
      cardsBox.innerHTML = '';
      cardsBox.appendChild(PF.el('p', 'muted', 'Detecting agents…'));
      return PF.api('GET', '/ai/agents' + (refresh ? '?refresh=1' : '')).then(function (r) {
        state.agents = (r && r.agents) || [];
        drawCards(r && r.mcpUrl);
        drawSettings();
      }, function (e) { cardsBox.innerHTML = ''; cardsBox.appendChild(PF.note('Could not detect agents: ' + e.message, 'bad')); });
    }

    function drawCards(mcpUrl) {
      cardsBox.innerHTML = '';
      var rows = state.agents.map(function (a) {
        var status = a.builtin ? PF.pill('built in', 'ok')
          : PF.pill(a.installed ? 'installed' : 'not installed', a.installed ? 'ok' : '');
        var conn = a.builtin ? '' : PF.pill(a.connected ? 'MCP connected' + (a.scope ? ' (' + a.scope + ')' : '') : 'MCP not connected', a.connected ? 'ok' : '');
        var acts = PF.el('div', 'btn-row');
        if (!a.builtin && a.installed) {
          if (a.connected) {
            var dis = PF.btn('Disconnect', 'sm', function () {
              PF.busy(dis, PF.api('POST', '/ai/agents/' + encodeURIComponent(a.name) + '/disconnect').then(function () { PF.toast('Disconnected ' + a.label, true); return loadAgents(true); }));
            });
            acts.appendChild(dis);
          } else {
            var con = PF.btn('Connect', 'sm primary', function () {
              if (!window.confirm('Register the PhoneForge MCP server in ' + a.label + '?' + (a.configPath ? '\nYour config (' + a.configPath + ') is backed up first.' : ''))) return;
              PF.busy(con, PF.api('POST', '/ai/agents/' + encodeURIComponent(a.name) + '/connect').then(function () { PF.toast('Connected ' + a.label, true); return loadAgents(true); }));
            });
            acts.appendChild(con);
          }
        }
        var name = PF.el('div'); name.appendChild(PF.el('strong', '', a.label));
        if (a.path) name.appendChild(PF.el('div', 'muted', a.path));
        if (a.ide) name.appendChild(PF.el('div', 'muted', 'IDE: connect only, no headless runs'));
        if (!a.installed && !a.builtin && (a.installHint || a.installUrl)) {
          var hint = PF.el('div', 'muted', 'Install: ' + (a.installHint || '') + ' ');
          if (a.installUrl) {
            var lk = document.createElement('a'); lk.href = a.installUrl; lk.target = '_blank'; lk.rel = 'noopener'; lk.textContent = a.installUrl;
            hint.appendChild(lk);
          }
          name.appendChild(hint);
        }
        return [name, status, a.version || '', conn, acts];
      });
      cardsBox.appendChild(PF.table(['Agent', 'Status', 'Version', 'MCP', ''], rows, 'No agents.'));
      if (mcpUrl) cardsBox.appendChild(PF.el('p', 'muted', 'MCP endpoint: ' + mcpUrl));
      var refreshBtn = PF.btn('Re-detect', 'sm', function () { PF.busy(refreshBtn, loadAgents(true)); });
      cardsBox.appendChild(refreshBtn);
    }

    /* ---------- settings ---------- */
    function drawSettings() {
      if (!state.settings) {
        PF.api('GET', '/ai/settings').then(function (s) { state.settings = s; drawSettings(); drawRun(); },
          function (e) { setBox.innerHTML = ''; setBox.appendChild(PF.note('Could not load settings: ' + e.message, 'bad')); });
        return;
      }
      var s = state.settings;
      setBox.innerHTML = '';
      var sels = {}, models = {};
      TYPES.forEach(function (t) {
        var sel = document.createElement('select');
        state.agents.forEach(function (a) {
          if (a.ide) return; // IDEs (Kiro) can be connected but have no headless mode
          var o = document.createElement('option'); o.value = a.name;
          o.textContent = a.label + (a.installed || a.builtin ? '' : ' (not installed)');
          sel.appendChild(o);
        });
        sel.value = (s[t.id] && s[t.id].agent) || 'builtin';
        var mi = PF.input('text', 'model (blank = agent default; Claude: haiku, sonnet, opus, fable; Kiro CLI: free text, e.g. auto)');
        mi.value = (s[t.id] && s[t.id].model) || '';
        sels[t.id] = sel; models[t.id] = mi;
        setBox.appendChild(PF.row(t.label + ' agent', sel, t.hint));
        setBox.appendChild(PF.row(t.label + ' model', mi));
      });
      var auto = document.createElement('input'); auto.type = 'checkbox'; auto.checked = !!s.fullAccess;
      var extra = document.createElement('textarea'); extra.rows = 4; extra.value = s.extraInstructions || '';
      extra.placeholder = 'Extra instructions appended to the agent system prompt';
      var ar = PF.row('Full access', auto, 'Run Claude Code with --dangerously-skip-permissions (and Kiro CLI with --trust-all-tools) so it can also use Bash and file tools on this Mac without prompts. Off by default. PhoneForge tools are always allowed.');
      ar.className += ' inline'; setBox.appendChild(ar);
      setBox.appendChild(PF.row('Extra instructions', extra));
      var save = PF.btn('Save settings', 'primary', function () {
        var body = {
          automation: { agent: sels.automation.value, model: models.automation.value.trim() },
          testing: { agent: sels.testing.value, model: models.testing.value.trim() },
          maxSteps: s.maxSteps || 50, fullAccess: auto.checked, version: s.version, extraInstructions: extra.value
        };
        PF.busy(save, PF.api('PUT', '/ai/settings', body).then(function (r) { state.settings = r; PF.toast('AI settings saved', true); drawRun(); if (PF.refreshAgentSettings) PF.refreshAgentSettings(); }));
      });
      var row = PF.el('div', 'btn-row'); row.appendChild(save); setBox.appendChild(row);
    }

    /* ---------- run console ---------- */
    function drawRun() {
      if (state.es) return; // do not redraw mid-run
      runBox.innerHTML = '';
      var typeSel = document.createElement('select');
      TYPES.forEach(function (t) { var o = document.createElement('option'); o.value = t.id; o.textContent = t.label; typeSel.appendChild(o); });
      var who = PF.el('span', 'muted');
      function updWho() {
        var ts = state.settings && state.settings[typeSel.value];
        who.textContent = 'Agent: ' + agentLabel(ts ? ts.agent : 'builtin');
      }
      typeSel.addEventListener('change', updWho); updWho();
      var dev = PF.el('span', 'muted', PF.selectedId ? 'Device: ' + PF.selectedId : 'No device selected');
      var prompt = document.createElement('textarea'); prompt.rows = 3; prompt.placeholder = 'What should the agent do?';
      prompt.style.width = '100%';
      var out = PF.el('pre', 'codeblock'); out.style.cssText = 'max-height:300px;overflow:auto;white-space:pre-wrap;min-height:60px;margin:8px 0';
      var runBtn = PF.btn('Run', 'primary'), stopBtn = PF.btn('Cancel', 'danger');
      stopBtn.disabled = true;
      var head = PF.el('div', 'btn-row');
      [typeSel, who, dev].forEach(function (n) { head.appendChild(n); });

      function append(s) {
        if (!s) return;
        out.textContent += s + '\n';
        out.scrollTop = out.scrollHeight;
      }
      function finish() {
        if (state.es) { state.es.close(); state.es = null; }
        state.runId = null; runBtn.disabled = false; stopBtn.disabled = true;
      }
      runBtn.addEventListener('click', function () {
        var p = prompt.value.trim();
        if (!p) { PF.toast('Enter a prompt'); return; }
        out.textContent = '';
        runBtn.disabled = true;
        PF.api('POST', '/ai/runs', { type: typeSel.value, deviceId: PF.selectedId || '', prompt: p }).then(function (r) {
          if (r && r.builtin) {
            // Built-in agent: hand off to the existing agent chat when the integrator wired one.
            if (typeof PF.runBuiltinAgent === 'function') {
              stopBtn.disabled = false;
              var h = PF.runBuiltinAgent(p, typeSel.value, append, function () { state.builtin = null; runBtn.disabled = false; stopBtn.disabled = true; append('[done]'); });
              state.builtin = h;
            } else {
              runBtn.disabled = false;
              append('The built-in agent is selected for this type. Run it from the Agent panel, or pick an external agent above.');
            }
            return;
          }
          state.runId = r.id; stopBtn.disabled = false;
          var es = state.es = new EventSource(PF.API + '/ai/runs/' + encodeURIComponent(r.id) + '/events');
          ['start', 'stdout', 'stderr', 'error', 'done'].forEach(function (t) {
            es.addEventListener(t, function (ev) {
              var e; try { e = JSON.parse(ev.data); } catch (x) { return; }
              if (t === 'start') append('[start] ' + e.data);
              else if (t === 'stdout') append(lineText(e.data));
              else if (t === 'stderr') append('[stderr] ' + e.data);
              else if (t === 'error') append('[error] ' + e.data);
              else { append('[done] ' + e.data); finish(); }
            });
          });
          es.onerror = function () { if (state.es === es && es.readyState === 2) finish(); };
        }, function (e) { runBtn.disabled = false; PF.toast(e.message); });
      });
      stopBtn.addEventListener('click', function () {
        if (state.builtin) { state.builtin.stop(); return; }
        if (state.runId) PF.busy(stopBtn, PF.api('POST', '/ai/runs/' + encodeURIComponent(state.runId) + '/cancel'));
      });
      runBox.appendChild(head);
      runBox.appendChild(prompt);
      var br = PF.el('div', 'btn-row'); br.appendChild(runBtn); br.appendChild(stopBtn);
      runBox.appendChild(br);
      runBox.appendChild(out);
    }

    // The Automation box changed agent/model: reload settings so this panel shows them.
    var refreshModel = PF.refreshAIConfigModel = function () { if (!root.isConnected) return; state.settings = null; drawSettings(); };
    loadAgents();
    return { destroy: function () { if (state.es) state.es.close(); state.es = null; if (PF.refreshAIConfigModel === refreshModel) PF.refreshAIConfigModel = null; } };
  };

  PF.openAIConfig = function () {
    var ctl = null;
    var m = PF.modal.open('AI Configuration', { wide: true, onClose: function () { if (ctl) ctl.destroy(); } });
    ctl = PF.renderAIConfig(m.body);
  };
})();
