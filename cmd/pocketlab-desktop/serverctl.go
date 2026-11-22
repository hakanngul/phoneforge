package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"pocketlab/internal/brand"
	"pocketlab/internal/lifecycle"
	"pocketlab/internal/server"
)

// serverCtl owns the desktop app's relationship with the Pocketlab server:
// it can start/stop/restart an in-process server, notices when the server it
// shows (owned or attached) disappears, and then switches the window to a local
// fallback page with a Start button. The fallback page is served by a tiny
// loopback control server guarded by a random path token.
type serverCtl struct {
	app  *application.App
	win  *application.WebviewWindow
	opts server.Options

	mu         sync.Mutex
	srv        *server.Server // owned in-process server (nil if attached or stopped)
	attached   bool
	addr       string // address to listen on / attach to (actual address once bound)
	fallback   bool   // window is showing the fallback page
	lastErr    string
	expectStop bool // a stop was requested by the user: do not quit when the server goes away
	ctlURL     string
	quitting   bool
}

func newServerCtl(app *application.App, opts server.Options, addr string) *serverCtl {
	opts.StartedVia, opts.Owner = "desktop", "desktop"
	return &serverCtl{app: app, opts: opts, addr: addr}
}

func (c *serverCtl) base() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return "http://" + c.addr + "/"
}

// adopt records the server chosen at startup (owned if srv != nil, else attached).
func (c *serverCtl) adopt(srv *server.Server, addr string, attached bool) {
	c.mu.Lock()
	c.srv, c.addr, c.attached = srv, addr, attached
	c.mu.Unlock()
	if srv != nil {
		c.watch(srv)
	}
}

// watch reacts to the end of an owned server: a UI "Quit" quits the app; any
// other end (crash, serve error) shows the fallback page.
func (c *serverCtl) watch(srv *server.Server) {
	go func() {
		select {
		case <-srv.ShutdownRequested():
			c.mu.Lock()
			quit := !c.expectStop
			c.mu.Unlock()
			if quit {
				c.quit()
			}
		case <-srv.Done():
			c.mu.Lock()
			own := c.srv == srv
			c.mu.Unlock()
			if own {
				c.mu.Lock()
				c.srv = nil
				c.mu.Unlock()
				c.showFallback("The server stopped unexpectedly. See the history below.")
			}
		}
	}()
}

func (c *serverCtl) quit() {
	c.mu.Lock()
	c.quitting = true
	c.mu.Unlock()
	c.app.Quit()
}

// shutdownOwned stops the owned server (if any) recording reason.
func (c *serverCtl) shutdownOwned(reason string) {
	c.mu.Lock()
	srv := c.srv
	c.srv = nil
	c.mu.Unlock()
	if srv == nil {
		return
	}
	srv.SetStopReason(reason, "desktop")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// Start starts an in-process server (no-op if one answers at the address).
func (c *serverCtl) Start() error {
	c.mu.Lock()
	addr := c.addr
	owned := c.srv != nil
	c.mu.Unlock()
	if owned || isPocketlab(addr) {
		c.showUI()
		return nil
	}
	opts := c.opts
	opts.Addr = addr
	srv, err := server.Start(context.Background(), opts)
	if err != nil {
		msg := "Could not start the server: " + err.Error()
		if portBusy(addr) {
			msg = "Could not start the server: " + addr + " is in use by another program."
		}
		c.mu.Lock()
		c.lastErr = msg
		c.mu.Unlock()
		return errors.New(msg)
	}
	c.mu.Lock()
	c.srv, c.attached, c.addr, c.lastErr, c.expectStop = srv, false, srv.Addr(), "", false
	c.mu.Unlock()
	c.watch(srv)
	c.showUI()
	return nil
}

// Stop stops the server: the owned one directly, an attached one via its shutdown API.
func (c *serverCtl) Stop() error {
	c.mu.Lock()
	c.expectStop = true
	owned := c.srv != nil
	addr := c.addr
	c.mu.Unlock()
	if owned {
		c.shutdownOwned("stopped from the desktop app (Server menu)")
	} else if isPocketlab(addr) {
		if err := requestShutdown(addr, "desktop-menu"); err != nil {
			return err
		}
		for i := 0; i < 50 && isPocketlab(addr); i++ {
			time.Sleep(200 * time.Millisecond)
		}
	}
	c.showFallback("The server was stopped from this app.")
	return nil
}

// Restart stops and starts the in-process server.
func (c *serverCtl) Restart() error {
	if err := c.Stop(); err != nil {
		return err
	}
	return c.Start()
}

func (c *serverCtl) showUI() {
	c.mu.Lock()
	c.fallback = false
	url := "http://" + c.addr + "/"
	c.mu.Unlock()
	c.win.SetURL(url)
}

func (c *serverCtl) showFallback(why string) {
	c.mu.Lock()
	if c.lastErr == "" {
		c.lastErr = why
	}
	c.fallback = true
	u := c.ctlURL
	c.mu.Unlock()
	if u == "" {
		return
	}
	c.win.SetURL(u)
	c.win.Show()
	c.win.Focus()
}

// monitor watches the server shown in the window. An attached server that
// reports shuttingDown quits the app (the in-app Quit semantics); one that
// vanishes, or any owned server that stopped answering, shows the fallback.
func (c *serverCtl) monitor() {
	down := 0
	for {
		time.Sleep(time.Second)
		c.mu.Lock()
		addr, fb, owned, expect, quitting := c.addr, c.fallback, c.srv != nil, c.expectStop, c.quitting
		c.mu.Unlock()
		if quitting {
			return
		}
		if fb {
			if isPocketlab(addr) { // someone (CLI, another app) started a server: reconnect
				c.mu.Lock()
				c.attached, c.expectStop, c.lastErr = !owned, false, ""
				c.mu.Unlock()
				down = 0
				c.showUI()
			}
			continue
		}
		up, stopping := probeHealth(addr)
		switch {
		case up && stopping && !owned && !expect:
			c.quit()
			return
		case up:
			down = 0
		default:
			down++
			if down >= 3 {
				down = 0
				c.mu.Lock()
				c.attached = false
				c.mu.Unlock()
				if owned {
					go c.shutdownOwned("stopped by the desktop app: the server stopped answering /health")
				}
				c.showFallback("The server this app was showing is no longer running.")
			}
		}
	}
}

// ---- helpers ----

func probeHealth(addr string) (up, shuttingDown bool) {
	c := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Get("http://" + addr + "/api/v1/health")
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	var body struct {
		Service      string `json:"service"`
		ShuttingDown bool   `json:"shuttingDown"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil || (body.Service != "pocketlab" && body.Service != "phoneforge") {
		return false, false
	}
	return true, body.ShuttingDown
}

func requestShutdown(addr, origin string) error {
	req, _ := http.NewRequest("POST", "http://"+addr+"/api/v1/server/shutdown", nil)
	req.Header.Set("X-Pocketlab-Action", "shutdown")
	req.Header.Set("X-Pocketlab-Origin", origin)
	// Older servers only know the pre-rename header names.
	req.Header.Set(brand.LegacyActionHeader, "shutdown")
	req.Header.Set(brand.LegacyOriginHeader, origin)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("shutdown request: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ---- fallback control server ----

// startControl serves the fallback page on a random loopback port.
func (c *serverCtl) startControl() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	tb := make([]byte, 16)
	_, _ = rand.Read(tb)
	tok := hex.EncodeToString(tb)
	host := ln.Addr().String()
	c.mu.Lock()
	c.ctlURL = "http://" + host + "/" + tok + "/"
	c.mu.Unlock()

	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Host != host {
				http.Error(w, "bad host", http.StatusForbidden)
				return
			}
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+host {
				http.Error(w, "bad origin", http.StatusForbidden)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			h(w, r)
		}
	}
	action := func(fn func() error) http.HandlerFunc {
		return guard(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := fn(); err != nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		})
	}
	mux := http.NewServeMux()
	p := "/" + tok + "/"
	mux.HandleFunc(p, guard(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != p {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fallbackPage))
	}))
	mux.HandleFunc(p+"state", guard(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		addr, owned, attached, lastErr := c.addr, c.srv != nil, c.attached, c.lastErr
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"up": isPocketlab(addr), "addr": addr, "owned": owned, "attached": attached,
			"message": lastErr, "baseURL": "http://" + addr + "/",
			"events": lifecycle.ReadHistory(server.PocketlabHome(), 15),
		})
	}))
	mux.HandleFunc(p+"start", action(c.Start))
	mux.HandleFunc(p+"restart", action(c.Restart))
	mux.HandleFunc(p+"quit", action(func() error { go func() { time.Sleep(100 * time.Millisecond); c.quit() }(); return nil }))
	log.Printf("fallback control page at %s", c.ctlURL)
	go func() {
		if err := (&http.Server{Handler: mux}).Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("control server: %v", err)
		}
	}()
	return nil
}

const fallbackPage = `<!doctype html><html><head><meta charset="utf-8"><title>Pocketlab</title>
<style>
:root{--bg:#0e0d14;--panel:#16151f;--line:#2a2839;--fg:#e8e6f2;--mut:#8b87a3;--acc:#8b5cf6;--ok:#34d399;--bad:#f87171}
body{margin:0;background:var(--bg);color:var(--fg);font:13px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
.wrap{max-width:860px;margin:0 auto;padding:36px 24px}
h1{font-size:20px;margin:0 0 4px} .sub{color:var(--mut);margin:0 0 16px}
.msg{border:1px solid var(--bad);background:#3b1219;color:#fecaca;border-radius:8px;padding:9px 12px;margin:12px 0}
.msg.ok{border-color:var(--ok);background:#0b2a22;color:#a7f3d0}
button{background:#1d1b29;color:var(--fg);border:1px solid var(--line);border-radius:6px;padding:7px 14px;cursor:pointer;font:inherit;margin-right:8px}
button.p{background:var(--acc);border-color:var(--acc);color:#fff} button:disabled{opacity:.5;cursor:default}
table{width:100%;border-collapse:collapse;margin-top:18px;border:1px solid var(--line);border-radius:8px}
th{text-align:left;color:var(--mut);font-size:11px;text-transform:uppercase;padding:6px 8px;border-bottom:1px solid var(--line)}
td{padding:6px 8px;border-bottom:1px solid var(--line);vertical-align:top;word-break:break-word}
tr.bad td{background:rgba(248,113,113,.09)} .tag{font-size:11px;border-radius:10px;padding:0 8px;background:var(--line)}
.tag.bad{background:#4c1d1d;color:var(--bad)} .tag.ok{background:#064e3b;color:var(--ok)} .mut{color:var(--mut)}
pre{margin:4px 0 0;font:11px/1.4 ui-monospace,Menlo,monospace;background:#0b0a10;padding:6px;border-radius:6px;white-space:pre-wrap;max-height:140px;overflow:auto}
</style></head><body><div class="wrap">
<h1>Pocketlab server is not running</h1>
<p class="sub" id="sub">Devices cannot be controlled until a server is started.</p>
<div id="msg" class="msg" hidden></div>
<div><button class="p" id="start">Start server</button><button id="quit">Quit Pocketlab</button></div>
<table><thead><tr><th>Time</th><th>Event</th><th>PID</th><th>Reason</th></tr></thead><tbody id="rows"></tbody></table>
<script>
var BAD={unexpected_exit:1,crash:1,listen_failed:1},LAB={start:'started',stop:'stopped',unexpected_exit:'unexpected exit',crash:'crashed',listen_failed:'listen failed'};
var busy=false;
function el(t,c,x){var e=document.createElement(t);if(c)e.className=c;if(x!=null)e.textContent=x;return e}
function msg(t,ok){var m=document.getElementById('msg');m.hidden=!t;m.textContent=t||'';m.className='msg'+(ok?' ok':'')}
function post(a){return fetch(a,{method:'POST'}).then(function(r){return r.json()})}
function draw(ev){var tb=document.getElementById('rows');tb.innerHTML='';
 ev.forEach(function(e){var tr=el('tr',BAD[e.event]?'bad':'');tr.appendChild(el('td','',new Date(e.time).toLocaleString()));
  var t=el('td');t.appendChild(el('span','tag '+(BAD[e.event]?'bad':e.event=='start'?'ok':''),LAB[e.event]||e.event));tr.appendChild(t);
  tr.appendChild(el('td','',String(e.pid||'')));var r=el('td');r.appendChild(el('div','',e.reason||''));
  var x=[];if(e.origin)x.push('origin: '+e.origin);if(e.startedVia)x.push('via '+e.startedVia);if(x.length)r.appendChild(el('div','mut',x.join(' · ')));
  if(e.logTail&&e.logTail.length){var d=el('details');d.appendChild(el('summary','mut','last log lines'));d.appendChild(el('pre','',e.logTail.join('\n')));r.appendChild(d)}
  tr.appendChild(r);tb.appendChild(tr)});
 if(!ev.length){var tr=el('tr'),td=el('td','mut','No lifecycle events recorded yet.');td.colSpan=4;tr.appendChild(td);tb.appendChild(tr)}}
function poll(){fetch('state').then(function(r){return r.json()}).then(function(s){
  if(s.up){location.href=s.baseURL;return}
  if(!busy)msg(s.message);draw(s.events||[]);
 }).catch(function(){}).then(function(){setTimeout(poll,1500)})}
document.getElementById('start').onclick=function(){var b=this;busy=true;b.disabled=true;msg('Starting server...',true);
 post('start').then(function(r){busy=false;b.disabled=false;if(!r.ok)msg(r.error);else msg('Server started, reconnecting...',true)})};
document.getElementById('quit').onclick=function(){post('quit')};
poll();
</script></div></body></html>`
