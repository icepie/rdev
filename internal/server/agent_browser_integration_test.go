package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

// Opt-in: exercises real server Chrome, the device TCP tunnel, CDP operations,
// vision output, and two independent conversation targets. No external sites.
func TestBrowserAgentChromeIntegration(t *testing.T) {
	if os.Getenv("RDEV_TEST_CHROME") == "" {
		t.Skip("set RDEV_TEST_CHROME=1 to run real Chrome integration")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/language" {
			fmt.Fprintf(w, "<!doctype html><p id=accepted>%s</p>", r.Header.Get("Accept-Language"))
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Device route fixture</title><input id="name"><button id="go" onclick="document.querySelector('#out').textContent=document.querySelector('#name').value">Apply</button><p id="out">Waiting</p><select id="choice"><option value="a">Alpha</option><option value="b">Beta</option></select><button id="hover" onmouseover="this.textContent='Hovered'">Hover me</button>`)
	}))
	defer site.Close()
	s, client, transport := newProxyTestServer(t, "exit-device", "")
	s.BrowserEnabled = true
	tunnel := httptest.NewServer(http.HandlerFunc(s.HandleTCPTunnel))
	defer tunnel.Close()
	var routed atomic.Int32
	transport.onSend = func(m *protocol.Message) {
		if m.Type != protocol.MsgTCPConnect {
			return
		}
		go func() {
			if m.Host != "agent-route.invalid" {
				s.handleClientMessage(client, &protocol.Message{Type: protocol.MsgTCPFail, ForwardID: m.ForwardID, Error: "test destination refused"})
				return
			}
			routed.Add(1)
			target, err := net.DialTimeout("tcp", strings.TrimPrefix(site.URL, "http://"), time.Second)
			if err != nil {
				s.handleClientMessage(client, &protocol.Message{Type: protocol.MsgTCPFail, ForwardID: m.ForwardID, Error: err.Error()})
				return
			}
			socket, _, err := gws.NewClient(&testTunnelWSHandler{target: target}, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(tunnel.URL, "http") + m.Tunnel})
			if err != nil {
				target.Close()
				return
			}
			go socket.ReadLoop()
			go func() {
				defer target.Close()
				defer socket.NetConn().Close()
				buf := make([]byte, 64*1024)
				for {
					n, err := target.Read(buf)
					if n > 0 {
						if e := socket.WriteMessage(gws.OpcodeBinary, buf[:n]); e != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}()
	}
	defer s.CloseBrowsers()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	first := &browserTool{testAutomation(t, s, "@server", "a")}
	second := &browserTool{testAutomation(t, s, "@server", "b")}
	run := func(tool *browserTool, args map[string]any) string {
		t.Helper()
		args["device"] = client.ID
		raw, _ := json.Marshal(args)
		result, err := tool.Execute(ctx, raw)
		if err != nil {
			t.Fatalf("%s: %v", args["action"], err)
		}
		return result
	}
	run(first, map[string]any{"action": "open", "url": "http://agent-route.invalid/"})
	if routed.Load() == 0 {
		t.Fatal("browser bypassed selected client")
	}
	snapshot := run(first, map[string]any{"action": "snapshot"})
	if !strings.Contains(snapshot, "#name") || !strings.Contains(snapshot, "Device route fixture") {
		t.Fatalf("bad snapshot: %s", snapshot)
	}
	run(first, map[string]any{"action": "wait_for", "selector": "#choice"})
	run(first, map[string]any{"action": "select", "selector": "#choice", "text": "b"})
	run(first, map[string]any{"action": "hover", "selector": "#hover"})
	observed := run(first, map[string]any{"action": "snapshot"})
	if !strings.Contains(observed, "Hovered") || !strings.Contains(observed, `"value":"b","label":"Beta","selected":true`) {
		t.Fatalf("select/hover failed: %s", observed)
	}
	run(first, map[string]any{"action": "fill", "selector": "#name", "text": "你好🌏 'quoted'"})
	run(first, map[string]any{"action": "click", "selector": "#go"})
	snapshot = run(first, map[string]any{"action": "snapshot"})
	if !strings.Contains(snapshot, "你好🌏 'quoted'") {
		t.Fatalf("fill/click failed: %s", snapshot)
	}
	shot := run(first, map[string]any{"action": "screenshot"})
	if !strings.HasPrefix(shot, "[image preview]\ndata:image/jpeg;base64,") {
		t.Fatal("missing vision envelope")
	}
	run(second, map[string]any{"action": "open", "url": "http://agent-route.invalid/"})
	if first.state.pages[client.ID].target == second.state.pages[client.ID].target {
		t.Fatal("conversations share a tab")
	}
	if strings.Contains(run(second, map[string]any{"action": "snapshot"}), "你好") {
		t.Fatal("second tab inherited first form")
	}
	if !strings.Contains(run(first, map[string]any{"action": "snapshot"}), "你好") {
		t.Fatal("first tab lost its state")
	}
	c, err := dialBrowserCDP(ctx, first.state.pages[client.ID].browser, first.state.pages[client.ID].target)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	checkCursor := func(expression string) {
		t.Helper()
		value, err := browserEvaluate(ctx, c, expression)
		if err != nil || value != "true" {
			t.Fatalf("cursor invariant: %s: %s, %v", expression, value, err)
		}
	}
	languageBrowser := first.state.pages[client.ID].browser
	setLanguage := func(lang string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, "/api/browser/view/"+languageBrowser.capability+"/language", strings.NewReader(`{"lang":"`+lang+`"}`))
		response := httptest.NewRecorder()
		s.HandleBrowserView(response, request)
		if response.Code != 204 {
			t.Fatalf("language update: %d %s", response.Code, response.Body.String())
		}
	}
	setLanguage("zh")
	checkCursor(`navigator.language==='zh-CN' && navigator.languages[1]==='zh' && new Intl.DateTimeFormat().resolvedOptions().locale==='zh-CN' && document.querySelector('#name').value.includes('你好')`)
	// A popup's very first request must carry the preference, not just pages
	// subsequently visited through the Agent tool.
	if _, err := browserEvaluate(ctx, c, `window.open('http://agent-route.invalid/language','language-popup');true`); err != nil {
		t.Fatal(err)
	}
	var popup *browserCDP
	popupDeadline := time.NewTimer(5 * time.Second)
	defer popupDeadline.Stop()
	popupPoll := time.NewTicker(20 * time.Millisecond)
	defer popupPoll.Stop()
	for popup == nil {
		var languageTargets []browserTarget
		if err := browserJSON(ctx, languageBrowser, http.MethodGet, "/json/list", &languageTargets); err != nil {
			t.Fatal(err)
		}
		for _, target := range languageTargets {
			if strings.Contains(target.URL, "/language") {
				popup, err = dialBrowserCDP(ctx, languageBrowser, target.ID)
				if err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		if popup == nil {
			select {
			case <-popupDeadline.C:
				t.Fatal("language popup not found")
			case <-popupPoll.C:
			}
		}
	}
	defer popup.close()
	if err := waitBrowserDocument(ctx, popup); err != nil {
		t.Fatal(err)
	}
	value, err := browserEvaluate(ctx, popup, `navigator.language==='zh-CN' && document.querySelector('#accepted').textContent.startsWith('zh-CN')`)
	if err != nil || value != "true" {
		t.Fatalf("popup language: %s %v", value, err)
	}
	setLanguage("en")
	checkCursor(`navigator.language==='en-US' && new Intl.DateTimeFormat().resolvedOptions().locale==='en-US' && document.querySelector('#name').value.includes('你好')`)
	value, err = browserEvaluate(ctx, popup, `fetch('/language').then(r=>r.text()).then(s=>s.includes('en-US'))`)
	if err != nil || value != "true" {
		t.Fatalf("updated request language: %s %v", value, err)
	}
	_ = popup.call(ctx, "Page.close", map[string]any{}, nil)
	// Resolved selector coordinates must match the rendered pointer after motion.
	checkCursor(`(()=>{const p=document.querySelector('rdev-agent-cursor'),r=document.querySelector('#go').getBoundingClientRect();return !!p&&parseInt(p.style.left)===Math.floor((r.left+r.right)/2)&&document.elementFromPoint(parseInt(p.style.left),parseInt(p.style.top)).id==='go'})()`)
	run(first, map[string]any{"action": "navigate", "url": "http://agent-route.invalid/?cursor-navigation"})
	checkCursor(`!!window.__rdevAgentCursor && !document.querySelector('rdev-agent-cursor')`)
	run(first, map[string]any{"action": "hover", "x": 123, "y": 150})
	checkCursor(`document.querySelector('rdev-agent-cursor').style.left==='123px' && document.querySelector('rdev-agent-cursor').style.top==='150px'`)
	if _, err := browserEvaluate(ctx, c, `window.cursorMoves=[];document.addEventListener('mousemove',e=>cursorMoves.push([e.clientX,e.clientY]));window.cursorClicks=0;document.addEventListener('click',()=>cursorClicks++)`); err != nil {
		t.Fatal(err)
	}
	run(first, map[string]any{"action": "hover", "x": 600, "y": 400})
	checkCursor(`cursorMoves.some(([x,y])=>x>123&&x<600&&y>150&&y<400) && cursorMoves.at(-1)[0]===600 && cursorMoves.at(-1)[1]===400`)
	cancelMove, stopMove := context.WithTimeout(ctx, time.Millisecond)
	rawMove, _ := json.Marshal(map[string]any{"device": client.ID, "action": "click", "x": 20, "y": 20})
	if _, err := first.Execute(cancelMove, rawMove); err == nil {
		t.Fatal("canceled trajectory completed a click")
	}
	stopMove()
	checkCursor(`cursorClicks===0 && !document.querySelector('rdev-agent-cursor')`)
	// A cross-origin frame cannot bubble mouse events to the parent. CDP-driven
	// placement still follows the coordinates and doesn't intercept its click.
	if _, err := browserEvaluate(ctx, c, `(()=>{const f=document.createElement('iframe');f.src='data:text/html,<button>Frame</button>';f.style='position:fixed;left:0;top:100px;width:250px;height:150px';document.body.append(f)})()`); err != nil {
		t.Fatal(err)
	}
	run(first, map[string]any{"action": "click", "x": 100, "y": 160})
	checkCursor(`document.querySelector('rdev-agent-cursor').style.top==='160px' && document.elementFromPoint(100,160).tagName==='IFRAME'`)
	s.finishAutomationTurn(first.state)
	checkCursor(`!window.__rdevAgentCursor && !document.querySelector('rdev-agent-cursor')`)
	run(first, map[string]any{"action": "navigate", "url": "http://agent-route.invalid/?after-cleanup"})
	checkCursor(`!window.__rdevAgentCursor`)
	run(first, map[string]any{"action": "hover", "selector": "#hover"})
	checkCursor(`!!document.querySelector('rdev-agent-cursor')`)
	if err = c.call(ctx, "Rdev.nonexistent", map[string]any{}, nil); err == nil {
		t.Fatal("CDP errors ignored")
	}
	// The visible inspector activates its page on pointer focus before resizing.
	if err := c.call(ctx, "Page.bringToFront", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{"width": 390, "height": 844, "deviceScaleFactor": 1, "mobile": false}, nil); err != nil {
		t.Fatal(err)
	}
	resized := run(first, map[string]any{"action": "snapshot"})
	if !strings.Contains(resized, `"width":390,"height":844`) {
		t.Fatalf("viewport was not applied: %s", resized)
	}
	run(first, map[string]any{"action": "fill", "selector": "#name", "text": "resized"})
	if _, err = browserEvaluate(ctx, c, `setTimeout(()=>{const e=document.createElement('button');e.id='delayed';e.textContent='Ready';document.body.append(e)},200);'scheduled'`); err != nil {
		t.Fatal(err)
	}
	run(first, map[string]any{"action": "wait_for", "selector": "#delayed"})
	waitCtx, stopWait := context.WithTimeout(ctx, 150*time.Millisecond)
	rawWait, _ := json.Marshal(map[string]any{"device": client.ID, "action": "wait_for", "selector": "#never-present"})
	if _, err := first.Execute(waitCtx, rawWait); err == nil {
		t.Fatal("wait_for ignored timeout")
	}
	stopWait()
	run(first, map[string]any{"action": "snapshot"}) // A timed-out wait must release the conversation lock.
	rawSelect, _ := json.Marshal(map[string]any{"device": client.ID, "action": "select", "selector": "#choice", "text": "missing"})
	if _, err := first.Execute(ctx, rawSelect); err == nil {
		t.Fatal("select accepted missing option")
	}
	c.close()
	s.retireAutomation(first.state)
	s.agentWG.Wait()
	if !strings.Contains(run(second, map[string]any{"action": "snapshot"}), "Device route fixture") {
		t.Fatal("retiring first conversation closed second target")
	}
}
