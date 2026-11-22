/* Pocketlab shared helpers: DOM, toast, API wrapper. */
(function (w) {
  'use strict';
  var PF = w.PF = { API: '/api/v1', selectedId: null, devices: [] };
  var toastTimer;

  PF.$ = function (id) { return document.getElementById(id); };

  PF.toast = function (msg, info) {
    var t = PF.$('toast');
    t.textContent = msg;
    t.className = 'toast' + (info ? ' info' : '');
    t.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { t.hidden = true; }, 4500);
  };

  PF.errMsg = function (res, text) {
    var m = text;
    try { var j = JSON.parse(text); m = j.error || j.message || text; } catch (e) { /* plain */ }
    return res.status + (m ? ': ' + String(m).slice(0, 200) : '');
  };

  // kind: 'json' (default) | 'blob' | 'text'. body: string sent raw, object sent as JSON.
  PF.api = function (method, path, body, kind) {
    var opts = { method: method, headers: {} };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = typeof body === 'string' ? body : JSON.stringify(body);
    }
    return fetch(PF.API + path, opts).then(function (res) {
      if (!res.ok) {
        return res.text().then(function (t) {
          var e = new Error(method + ' ' + path + ' -> ' + PF.errMsg(res, t));
          e.status = res.status;
          try { var j = JSON.parse(t); e.code = j.code; e.serverMessage = j.error || j.message; e.hint = j.hint; e.lines = j.lines; } catch (x) { /* plain text */ }
          throw e;
        });
      }
      if (kind === 'blob') return res.blob();
      return res.text().then(function (t) { return kind === 'text' ? t : (t ? JSON.parse(t) : null); });
    }, function (err) { throw new Error('Network error: ' + err.message); });
  };

  PF.guard = function (p) { return p.catch(function (e) { PF.toast(e.message); }); };

  // Like guard, but UNSUPPORTED (501) shows the server's own message as a calm info toast.
  PF.guardDev = function (p) {
    return p.catch(function (e) {
      if (e && (e.code === 'UNSUPPORTED' || e.status === 501)) PF.toast(e.serverMessage || 'Not supported on this device', true);
      else PF.toast(e.message);
    });
  };

  // multipart upload with progress. onProgress(fraction, phase) phase: 'up' | 'sent'.
  PF.upload = function (path, field, file, onProgress) {
    return new Promise(function (resolve, reject) {
      var x = new XMLHttpRequest();
      x.open('POST', PF.API + path);
      x.upload.onprogress = function (ev) { if (ev.lengthComputable && onProgress) onProgress(ev.loaded / ev.total, 'up'); };
      x.upload.onload = function () { if (onProgress) onProgress(1, 'sent'); };
      x.onerror = function () { reject(new Error('Network error')); };
      x.onload = function () {
        var j = null; try { j = JSON.parse(x.responseText); } catch (e) { /* not json */ }
        if (x.status >= 200 && x.status < 300) { resolve(j); return; }
        var m = j ? (j.error || j.message) : x.responseText;
        var err = new Error('POST ' + path + ' -> ' + x.status + (m ? ': ' + String(m).slice(0, 200) : ''));
        err.status = x.status; if (j) { err.code = j.code; err.serverMessage = j.error || j.message; }
        reject(err);
      };
      var fd = new FormData(); fd.append(field, file, file.name);
      x.send(fd);
    });
  };

  PF.saveBlob = function (blob, filename) {
    var u = URL.createObjectURL(blob), a = document.createElement('a');
    a.href = u; a.download = filename; document.body.appendChild(a); a.click();
    document.body.removeChild(a);
    setTimeout(function () { URL.revokeObjectURL(u); }, 10000);
  };

  PF.dev = function (suffix) { return '/devices/' + encodeURIComponent(PF.selectedId) + suffix; };

  PF.needDevice = function () {
    if (!PF.selectedId) { PF.toast('Select a device first', true); return false; }
    return true;
  };

  PF.selectedDevice = function () {
    for (var i = 0; i < PF.devices.length; i++) if (PF.devices[i].id === PF.selectedId) return PF.devices[i];
    return null;
  };

  PF.el = function (tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };

  PF.hooks = { onSelect: [], onDevices: [] };
})(window);
