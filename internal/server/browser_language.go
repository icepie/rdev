package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/lxzan/gws"
)

// One browser-scoped CDP session owns locale overrides. Auto-attach pauses new
// pages/frames before their first request, including window.open and Agent tabs.
// Settings survive short-lived tool and DevTools connections.
type browserLanguage struct {
	gws.BuiltinEventHandler
	socket    *gws.Conn
	mu        sync.Mutex
	next      int
	pending   map[int]chan cdpReply
	sessions  map[string]bool
	settings  sync.Mutex
	lang      string
	userAgent string
	ctx       context.Context
	cancel    context.CancelFunc
}

func newBrowserLanguage(ctx context.Context, b *browserSession) (*browserLanguage, error) {
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		UserAgent            string `json:"User-Agent"`
	}
	if err := browserJSON(ctx, b, "GET", "/json/version", &version); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(ctx)
	l := &browserLanguage{ctx: life, cancel: cancel, pending: make(map[int]chan cdpReply), sessions: make(map[string]bool), lang: "en", userAgent: version.UserAgent}
	socket, _, err := gws.NewClient(l, &gws.ClientOption{Addr: version.WebSocketDebuggerURL, HandshakeTimeout: 5 * time.Second, ReadMaxPayloadSize: 8 << 20})
	if err != nil {
		cancel()
		return nil, err
	}
	l.socket = socket
	go socket.ReadLoop()
	if err = l.call(ctx, "", "Target.setAutoAttach", languageAutoAttach()); err != nil {
		l.close()
		return nil, err
	}
	return l, nil
}

func languageAutoAttach() map[string]any {
	return map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true, "filter": []map[string]any{{"type": "page"}, {"type": "iframe"}, {"exclude": true}}}
}
func (l *browserLanguage) OnClose(_ *gws.Conn, _ error) { l.cancel() }
func (l *browserLanguage) OnMessage(_ *gws.Conn, m *gws.Message) {
	defer m.Close()
	var msg struct {
		cdpReply
		Method string `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	if json.Unmarshal(m.Bytes(), &msg) != nil {
		return
	}
	l.mu.Lock()
	if msg.ID != 0 {
		ch := l.pending[msg.ID]
		l.mu.Unlock()
		if ch != nil {
			ch <- msg.cdpReply
		}
		return
	}
	id := msg.Params.SessionID
	switch msg.Method {
	case "Target.attachedToTarget":
		l.sessions[id] = true
		l.mu.Unlock()
		go l.attach(id)
	case "Target.detachedFromTarget":
		delete(l.sessions, id)
		l.mu.Unlock()
	default:
		l.mu.Unlock()
	}
}
func (l *browserLanguage) call(ctx context.Context, session, method string, params any, out ...any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	l.next++
	id := l.next
	ch := make(chan cdpReply, 1)
	l.pending[id] = ch
	l.mu.Unlock()
	defer func() { l.mu.Lock(); delete(l.pending, id); l.mu.Unlock() }()
	data, err := json.Marshal(map[string]any{"id": id, "sessionId": session, "method": method, "params": params})
	if err != nil {
		return err
	}
	if err = l.socket.WriteMessage(gws.OpcodeText, data); err != nil {
		return err
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return fmt.Errorf("%s: %s", method, r.Error.Message)
		}
		if len(out) != 0 {
			return json.Unmarshal(r.Result, out[0])
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-l.ctx.Done():
		return fmt.Errorf("browser language connection closed")
	}
}
func (l *browserLanguage) apply(ctx context.Context, id, lang string) error {
	locale, accept := "en-US", "en-US,en"
	if lang == "zh" {
		locale, accept = "zh-CN", "zh-CN,zh,en"
	}
	if err := l.call(ctx, id, "Emulation.setUserAgentOverride", map[string]any{"userAgent": l.userAgent, "acceptLanguage": accept}); err != nil {
		return err
	}
	err := l.call(ctx, id, "Emulation.setLocaleOverride", map[string]any{"locale": locale})
	// Same-process popups share ICU locale; verify inheritance when another
	// session already owns that renderer's override.
	if err != nil && strings.Contains(err.Error(), "Another locale override is already in effect") {
		var result struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if checkErr := l.call(ctx, id, "Runtime.evaluate", map[string]any{"expression": "new Intl.DateTimeFormat().resolvedOptions().locale", "returnByValue": true}, &result); checkErr != nil {
			return checkErr
		}
		if result.Result.Value == locale {
			return nil
		}
	}
	return err
}
func (l *browserLanguage) attach(id string) {
	l.settings.Lock()
	defer l.settings.Unlock()
	ctx, cancel := context.WithTimeout(l.ctx, 10*time.Second)
	defer cancel()
	err := l.apply(ctx, id, l.lang)
	if err == nil {
		err = l.call(ctx, id, "Target.setAutoAttach", languageAutoAttach())
	}
	// Do not let a failed configuration silently navigate with the wrong locale.
	// Terminate the managed browser on a live-target failure; closed tabs are normal.
	l.mu.Lock()
	alive := l.sessions[id]
	l.mu.Unlock()
	if err != nil && alive {
		log.Printf("browser language attachment: %v", err)
		l.cancel()
		return
	}
	_ = l.call(ctx, id, "Runtime.runIfWaitingForDebugger", map[string]any{})
}
func (l *browserLanguage) set(ctx context.Context, lang string) error {
	if lang != "zh" && lang != "en" {
		return fmt.Errorf("language must be zh or en")
	}
	l.settings.Lock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer l.settings.Unlock()
	l.mu.Lock()
	ids := make([]string, 0, len(l.sessions))
	for id := range l.sessions {
		ids = append(ids, id)
	}
	l.mu.Unlock()
	// Release previous renderer-wide ownership before assigning the new locale.
	for _, id := range ids {
		if err := l.call(ctx, id, "Emulation.setLocaleOverride", map[string]any{"locale": ""}); err != nil {
			l.mu.Lock()
			alive := l.sessions[id]
			l.mu.Unlock()
			if alive {
				return err
			}
		}
	}
	for _, id := range ids {
		if err := l.apply(ctx, id, lang); err != nil {
			l.mu.Lock()
			alive := l.sessions[id]
			l.mu.Unlock()
			if alive {
				return err
			}
		}
	}
	l.lang = lang
	return nil
}
func (l *browserLanguage) close() {
	l.cancel()
	if l.socket != nil {
		_ = l.socket.NetConn().Close()
	}
}
