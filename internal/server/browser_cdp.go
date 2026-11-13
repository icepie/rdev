package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/lxzan/gws"
)

// A connection belongs to one operation, never a process-wide active page.
// Calls are sequential; unsolicited CDP events are ignored without blocking.
type browserCDP struct {
	gws.BuiltinEventHandler
	socket  *gws.Conn
	replies chan cdpReply
	done    chan struct{}
	once    sync.Once
	next    int
}
type cdpReply struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *browserCDP) OnClose(_ *gws.Conn, _ error) { c.once.Do(func() { close(c.done) }) }
func (c *browserCDP) OnMessage(_ *gws.Conn, m *gws.Message) {
	defer m.Close()
	var reply cdpReply
	if json.Unmarshal(m.Bytes(), &reply) != nil || reply.ID == 0 {
		return
	}
	select {
	case c.replies <- reply:
	case <-c.done:
	}
}
func dialBrowserCDP(ctx context.Context, b *browserSession, target string) (*browserCDP, error) {
	c := &browserCDP{replies: make(chan cdpReply, 8), done: make(chan struct{})}
	endpoint := *b.endpoint
	endpoint.Scheme = "ws"
	endpoint.Path = "/devtools/page/" + target
	socket, _, err := gws.NewClient(c, &gws.ClientOption{Addr: endpoint.String(), HandshakeTimeout: 5 * time.Second, ReadMaxPayloadSize: 8 << 20})
	if err != nil {
		return nil, err
	}
	c.socket = socket
	go socket.ReadLoop()
	go func() {
		select {
		case <-ctx.Done():
			c.close()
		case <-c.done:
		}
	}()
	return c, nil
}
func (c *browserCDP) close() { c.once.Do(func() { close(c.done) }); _ = c.socket.NetConn().Close() }
func (c *browserCDP) call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.next++
	data, err := json.Marshal(map[string]any{"id": c.next, "method": method, "params": params})
	if err != nil {
		return err
	}
	_ = c.socket.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := c.socket.WriteMessage(gws.OpcodeText, data); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return fmt.Errorf("browser connection closed")
		case r := <-c.replies:
			if r.ID != c.next {
				continue
			}
			if r.Error != nil {
				return fmt.Errorf("%s: %s", method, r.Error.Message)
			}
			if out != nil {
				return json.Unmarshal(r.Result, out)
			}
			return nil
		}
	}
}
func browserJSON(ctx context.Context, b *browserSession, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, b.endpoint.String()+path, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: browserTransport, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Chrome returned %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out)
}
func browserNavigationURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("use an http(s) URL without embedded credentials")
	}
	return nil
}

func validBrowserTargetID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
