/*
 * Pocketlab H.264 live player (WebCodecs).
 *
 * Pipeline: fetch(stream.h264) -> ReadableStream reader -> Annex-B NAL splitter ->
 * access-unit grouping -> VideoDecoder -> <canvas>.
 *
 * NAL SPLITTING
 *   The byte stream is appended to a buffer that always begins at a start code
 *   (00 00 01 or 00 00 00 01). A NAL unit is complete once the NEXT start code is seen;
 *   its payload is the bytes between the two (trailing zero bytes stripped). Bytes before
 *   the first start code are discarded (resync). Only the unfinished tail is rescanned per chunk.
 *
 * ACCESS UNITS
 *   NAL types: 1 slice, 5 IDR slice, 6 SEI, 7 SPS, 8 PPS, 9 AUD. A new access unit starts on
 *   AUD/SEI/SPS/PPS when the current one already holds a slice, or on a slice whose
 *   first_mb_in_slice == 0 (first bit of the slice data, i.e. bit 7 of byte 1) when the current
 *   one already holds a slice. Because the last NAL of a frame is only known to be complete when
 *   the next frame starts, an idle timer (40 ms without new bytes) flushes a trailing slice NAL and
 *   the pending access unit so a static screen does not hold back its last frame.
 *   Trade-off: if the network stalls mid-NAL for > 40 ms the NAL is decoded truncated; the decoder
 *   then errors, we reconnect, and after 2 consecutive failures without a frame we fall back.
 *
 * DECODING
 *   Codec string = 'avc1.' + hex(profile_idc, constraint flags, level_idc) from SPS bytes 1..3.
 *   VideoDecoder is configured on the first SPS with optimizeForLatency (and re-configured if the
 *   codec string changes). Keyframes (contain NAL 5) are sent as 'key' with SPS+PPS prepended as
 *   Annex-B (unless the AU already carries them), everything else as 'delta'. Timestamps are a counter.
 *   If decodeQueueSize > MAX_QUEUE a delta frame is dropped and further deltas are skipped until the
 *   next keyframe (deltas on a broken reference chain would only produce garbage).
 *
 * FALLBACK / RECONNECT
 *   No VideoDecoder, HTTP 404/501/400, missing body, decoder or configure error twice in a row, or 3
 *   failed connects -> opts.onFallback(reason) and the player stops (the caller starts screenshot polling).
 *   A stream that ends cleanly (or a transient error after frames flowed) reconnects after 1 s.
 */
(function (w) {
  'use strict';
  var PF = w.PF;
  var MAX_QUEUE = 4, FRAME_US = 33333, IDLE_MS = 40, RECONNECT_MS = 1000;
  var supported = typeof w.VideoDecoder === 'function' && typeof w.EncodedVideoChunk === 'function';
  var cur = null;

  function concat(a, b) {
    if (!a.length) return b;
    var o = new Uint8Array(a.length + b.length);
    o.set(a, 0); o.set(b, a.length);
    return o;
  }
  function hex2(n) { return (n < 16 ? '0' : '') + n.toString(16); }

  // First start code at index >= from: {pos, len}. pos includes the extra leading zero of 4-byte codes.
  function findStart(buf, from) {
    var n = buf.length;
    for (var i = from; i + 2 < n; i++) {
      var c = buf[i + 2];
      if (c > 1) { i += 2; continue; } // no start code can begin at i, i+1 or i+2
      if (c === 1 && buf[i] === 0 && buf[i + 1] === 0) {
        if (i > 0 && buf[i - 1] === 0) return { pos: i - 1, len: 4 };
        return { pos: i, len: 3 };
      }
    }
    return null;
  }

  function newAU() { return { nals: [], slice: false, key: false, params: false }; }

  function newPlayer(opts) {
    return {
      opts: opts, dead: false, gen: 0, abort: null, decoder: null, ctx: null,
      buf: new Uint8Array(0), scanFrom: 0, au: newAU(), sps: null, pps: null, codec: null,
      configured: false, needKey: true, ts: 0, frames: 0, total: 0, bytes: 0, gotFrame: false,
      fails: 0, connects: 0, idle: null, fpsTimer: null, retry: null
    };
  }

  function stopPlayer(P) {
    P.dead = true;
    clearTimeout(P.idle); clearTimeout(P.retry); clearInterval(P.fpsTimer);
    try { if (P.abort) P.abort.abort(); } catch (e) { /* ignore */ }
    closeDecoder(P);
  }
  function closeDecoder(P) {
    var d = P.decoder; P.decoder = null; P.configured = false;
    if (d) { try { if (d.state !== 'closed') d.close(); } catch (e) { /* ignore */ } }
  }

  function giveUp(P, reason) {
    if (P.dead) return;
    var cb = P.opts.onFallback;
    stopPlayer(P);
    if (cur === P) cur = null;
    if (cb) cb(reason);
  }

  // Decoder / parse failure: reconnect once, give up when it repeats without any rendered frame.
  function fail(P, reason, fatal) {
    if (P.dead) return;
    P.fails++;
    if (fatal || P.fails >= 2) { giveUp(P, reason); return; }
    try { if (P.abort) P.abort.abort(); } catch (e) { /* ignore */ }
    scheduleRun(P, 300);
  }

  function onOutput(P, frame) {
    if (P.dead) { frame.close(); return; }
    var cv = P.opts.canvas, w0 = frame.displayWidth, h0 = frame.displayHeight;
    if (cv.width !== w0 || cv.height !== h0) { cv.width = w0; cv.height = h0; }
    if (!P.ctx) P.ctx = cv.getContext('2d');
    try { P.ctx.drawImage(frame, 0, 0); } catch (e) { /* ignore draw errors */ }
    frame.close();
    P.frames++; P.total++; P.fails = 0; P.connects = 0;
    if (!P.gotFrame) { P.gotFrame = true; if (P.opts.onFrame) P.opts.onFrame(); }
  }

  function makeDecoder(P) {
    closeDecoder(P);
    P.decoder = new w.VideoDecoder({
      output: function (f) { onOutput(P, f); },
      error: function (e) { fail(P, 'decoder error: ' + (e && e.message), false); }
    });
    P.configured = false; P.codec = null; P.needKey = true;
  }

  function configure(P, sps) {
    if (sps.length < 4 || !P.decoder) return;
    var codec = 'avc1.' + hex2(sps[1]) + hex2(sps[2]) + hex2(sps[3]);
    if (codec === P.codec && P.configured) return;
    P.codec = codec;
    try {
      P.decoder.configure({ codec: codec, optimizeForLatency: true });
      P.configured = true; P.needKey = true;
    } catch (e) { fail(P, 'configure failed (' + codec + '): ' + e.message, true); }
  }

  function flushAU(P) {
    var au = P.au; P.au = newAU();
    if (!au.slice) return;
    decodeAU(P, au);
  }

  function decodeAU(P, au) {
    var dec = P.decoder;
    if (!dec || !P.configured || dec.state !== 'configured') return;
    if (au.key) {
      if (!au.params && !(P.sps && P.pps)) return;
      P.needKey = false;
    } else {
      if (P.needKey) return;
      if (dec.decodeQueueSize > MAX_QUEUE) { P.needKey = true; return; } // drop + wait for keyframe
    }
    var parts = [], total = 0, i, n;
    if (au.key && !au.params) { parts.push(P.sps, P.pps); }
    for (i = 0; i < au.nals.length; i++) parts.push(au.nals[i]);
    for (i = 0; i < parts.length; i++) total += parts[i].length + 4;
    var data = new Uint8Array(total), o = 0;
    for (i = 0; i < parts.length; i++) {
      n = parts[i];
      data[o] = 0; data[o + 1] = 0; data[o + 2] = 0; data[o + 3] = 1; o += 4;
      data.set(n, o); o += n.length;
    }
    try {
      dec.decode(new w.EncodedVideoChunk({ type: au.key ? 'key' : 'delta', timestamp: P.ts, data: data }));
      P.ts += FRAME_US;
    } catch (e) { fail(P, 'decode failed: ' + e.message, false); }
  }

  function emitNal(P, nal) {
    var end = nal.length;
    while (end > 0 && nal[end - 1] === 0) end--;
    if (end < 1) return;
    if (end !== nal.length) nal = nal.subarray(0, end);
    var t = nal[0] & 31, au = P.au;
    if (t === 9 || t === 6 || t === 7 || t === 8 || (t >= 14 && t <= 18)) {
      if (au.slice) { flushAU(P); au = P.au; }
    } else if (t === 1 || t === 5) {
      if (au.slice && nal.length > 1 && (nal[1] & 0x80)) { flushAU(P); au = P.au; }
    } else return; // ignore other types
    if (t === 7) { P.sps = nal; configure(P, nal); au.nals.push(nal); au.params = true; }
    else if (t === 8) { P.pps = nal; au.nals.push(nal); }
    else if (t === 1 || t === 5) { au.nals.push(nal); au.slice = true; if (t === 5) au.key = true; }
  }

  function parse(P) {
    var buf = P.buf, s = findStart(buf, 0);
    if (!s) { P.buf = buf.subarray(Math.max(0, buf.length - 3)); P.scanFrom = 0; return; }
    var pos = s.pos, hdr = s.len, first = s.pos === 0;
    for (;;) {
      var from = pos + hdr;
      if (first && P.scanFrom > from) from = P.scanFrom;
      first = false;
      var nx = findStart(buf, from);
      if (!nx) break;
      emitNal(P, buf.subarray(pos + hdr, nx.pos));
      if (P.dead) return;
      pos = nx.pos; hdr = nx.len;
    }
    P.buf = buf.subarray(pos);
    P.scanFrom = Math.max(hdr, P.buf.length - 3);
  }

  // No new bytes for IDLE_MS: treat the trailing slice NAL as complete and flush the access unit.
  function idleFlush(P) {
    if (P.dead) return;
    var s = P.buf.length ? findStart(P.buf, 0) : null;
    if (s && s.pos === 0 && P.buf.length > s.len + 1) {
      var t = P.buf[s.len] & 31;
      if (t === 1 || t === 5) {
        var nal = P.buf.subarray(s.len);
        P.buf = new Uint8Array(0); P.scanFrom = 0;
        emitNal(P, nal);
      }
    }
    if (!P.dead && P.au.slice) flushAU(P);
  }

  function feed(P, chunk) {
    P.buf = concat(P.buf, chunk);
    parse(P);
    clearTimeout(P.idle);
    if (!P.dead) P.idle = setTimeout(function () { idleFlush(P); }, IDLE_MS);
  }

  function scheduleRun(P, ms) {
    clearTimeout(P.retry);
    P.retry = setTimeout(function () { run(P); }, ms);
  }

  function run(P) {
    if (P.dead) return;
    var gen = ++P.gen;
    P.buf = new Uint8Array(0); P.scanFrom = 0; P.au = newAU(); P.sps = null; P.pps = null;
    P.gotConn = false;
    try { makeDecoder(P); } catch (e) { giveUp(P, 'cannot create decoder: ' + e.message); return; }
    P.abort = new AbortController();
    var sawBytes = false;

    function again(why) {
      if (P.dead || gen !== P.gen) return;
      if (sawBytes && P.gotFrame) P.connects = 0; else P.connects++;
      if (P.connects > 3) { giveUp(P, why); return; }
      scheduleRun(P, RECONNECT_MS);
    }

    fetch(P.opts.url, { signal: P.abort.signal, cache: 'no-store', headers: { Accept: 'video/h264, application/octet-stream' } }).then(function (res) {
      if (P.dead || gen !== P.gen) return;
      if (!res.ok) {
        if (res.status === 404 || res.status === 501 || res.status === 400) { giveUp(P, 'stream not available (' + res.status + ')'); return; }
        throw new Error('HTTP ' + res.status);
      }
      if (!res.body || !res.body.getReader) { giveUp(P, 'streaming body not supported'); return; }
      var reader = res.body.getReader();
      function pump() {
        return reader.read().then(function (r) {
          if (P.dead || gen !== P.gen) { try { reader.cancel(); } catch (e) { /* ignore */ } return; }
          if (r.done) { again('stream ended'); return; }
          if (r.value && r.value.length) { sawBytes = true; P.bytes += r.value.length; feed(P, r.value); }
          return pump();
        });
      }
      return pump();
    }).catch(function (e) {
      if (P.dead || gen !== P.gen || (e && e.name === 'AbortError')) return;
      again('stream error: ' + (e && e.message));
    });
  }

  PF.video = {
    supported: supported,
    // opts: {url, canvas, onFrame(), onFps(n), onFallback(reason)}
    start: function (opts) {
      PF.video.stop();
      if (!supported) { if (opts.onFallback) setTimeout(function () { opts.onFallback('WebCodecs VideoDecoder unavailable'); }, 0); return; }
      var P = cur = newPlayer(opts);
      P.fpsTimer = setInterval(function () {
        var n = P.frames; P.frames = 0;
        if (!P.dead && opts.onFps) opts.onFps(n);
      }, 1000);
      run(P);
    },
    stop: function () { if (cur) { stopPlayer(cur); cur = null; } },
    // cumulative counters of the running player: decoded/drawn frames and bytes received
    stats: function () { return cur ? { frames: cur.total, bytes: cur.bytes } : { frames: 0, bytes: 0 }; }
  };

  /*
   * MJPEG (multipart/x-mixed-replace) parser, push-based. feed(Uint8Array) calls onFrame(Uint8Array jpeg) per part.
   * Each part: "--boundary\r\n" headers "\r\n\r\n" body. Body length comes from Content-Length when present,
   * otherwise from the next "\r\n--boundary" delimiter. If no boundary is known, JPEG SOI (FFD8) .. next SOI
   * / EOI (FFD9 at end of data) scanning is used. Memory use is bounded (buffer is trimmed to the unparsed tail).
   */
  function indexOfSeq(buf, seq, from) {
    var n = buf.length - seq.length, f = seq[0];
    outer: for (var i = from; i <= n; i++) {
      if (buf[i] !== f) continue;
      for (var j = 1; j < seq.length; j++) if (buf[i + j] !== seq[j]) continue outer;
      return i;
    }
    return -1;
  }
  function ascii(s) { var a = new Uint8Array(s.length); for (var i = 0; i < s.length; i++) a[i] = s.charCodeAt(i); return a; }
  function mjpegParser(boundary, onFrame) {
    var buf = new Uint8Array(0), bnd = boundary ? ascii('--' + boundary.replace(/^--/, '')) : null;
    var CRLF2 = ascii('\r\n\r\n'), MAXBUF = 32 * 1024 * 1024;
    function cat(a, b) { if (!a.length) return b; var o = new Uint8Array(a.length + b.length); o.set(a, 0); o.set(b, a.length); return o; }
    function step() {
      if (bnd) {
        var b = indexOfSeq(buf, bnd, 0);
        if (b < 0) { buf = buf.subarray(Math.max(0, buf.length - bnd.length)); return false; }
        var he = indexOfSeq(buf, CRLF2, b);
        if (he < 0) { buf = buf.subarray(b); return false; }
        var head = String.fromCharCode.apply(null, buf.subarray(b, he));
        var m = /content-length:\s*(\d+)/i.exec(head), start = he + 4, len;
        if (m) {
          len = parseInt(m[1], 10);
          if (buf.length < start + len) { buf = buf.subarray(b); return false; }
          onFrame(buf.slice(start, start + len));
          buf = buf.subarray(start + len);
          return true;
        }
        var nx = indexOfSeq(buf, bnd, start);
        if (nx < 0) { buf = buf.subarray(b); return false; }
        var end = nx; if (end >= 2 && buf[end - 2] === 13 && buf[end - 1] === 10) end -= 2;
        if (end > start) onFrame(buf.slice(start, end));
        buf = buf.subarray(nx);
        return true;
      }
      // no boundary: raw SOI/EOI scanning
      var s = indexOfSeq(buf, [0xFF, 0xD8, 0xFF], 0);
      if (s < 0) { buf = buf.subarray(Math.max(0, buf.length - 2)); return false; }
      var n2 = indexOfSeq(buf, [0xFF, 0xD8, 0xFF], s + 3);
      if (n2 < 0) { buf = buf.subarray(s); return false; }
      var e2 = n2; while (e2 > s + 2 && !(buf[e2 - 2] === 0xFF && buf[e2 - 1] === 0xD9)) e2--; // drop trailing multipart junk
      if (e2 > s + 2) onFrame(buf.slice(s, e2));
      buf = buf.subarray(n2);
      return true;
    }
    return {
      feed: function (chunk) {
        buf = cat(buf, chunk);
        while (step()) { /* drain */ }
        if (buf.length > MAXBUF) buf = new Uint8Array(0); // runaway garbage guard
      }
    };
  }
  PF.mjpegParser = mjpegParser;
  if (typeof module !== 'undefined' && module.exports) module.exports = { mjpegParser: mjpegParser };
})(typeof window !== 'undefined' ? window : { PF: {} });
