package client

import (
	"errors"
	"net"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

// tcpTunnelWSHandler pumps dial-back WebSocket frames into the target conn.
type tcpTunnelWSHandler struct {
	gws.BuiltinEventHandler
	target net.Conn
}

func (h *tcpTunnelWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if message.Opcode != gws.OpcodeBinary {
		return
	}
	_, _ = h.target.Write(message.Bytes())
}

func (h *tcpTunnelWSHandler) OnClose(socket *gws.Conn, err error) {
	_ = h.target.Close()
}

// runTCPTunnel opens the dedicated raw WebSocket offered by the server in
// tcp_connect and splices it with target: one WS per stream, no framed
// control-channel forwarding. On success the caller must not touch target.
func (c *Client) runTCPTunnel(msg *protocol.Message, target net.Conn) error {
	base := c.tunnelBaseURL()
	if base == "" {
		return errors.New("no base URL for tunnel dial-back")
	}
	wsURL := base + msg.Tunnel
	handler := &tcpTunnelWSHandler{target: target}
	socket, _, err := gws.NewClient(handler, &gws.ClientOption{
		Addr:               wsURL,
		HandshakeTimeout:   10 * time.Second,
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		NewDialer:          websocketDialerFor(wsURL),
	})
	if err != nil {
		return err
	}

	go socket.ReadLoop()
	go func() {
		defer func() {
			_ = socket.WriteClose(1000, nil)
			_ = target.Close()
			c.mu.Lock()
			if c.forwards[msg.ForwardID] == target {
				delete(c.forwards, msg.ForwardID)
			}
			c.mu.Unlock()
		}()
		buf := make([]byte, 64*1024)
		for {
			n, err := target.Read(buf)
			if n > 0 {
				_ = socket.SetWriteDeadline(time.Now().Add(clientWriteWait))
				if werr := socket.WriteMessage(gws.OpcodeBinary, buf[:n]); werr != nil {
					return
				}
				_ = socket.SetWriteDeadline(time.Time{})
			}
			if err != nil {
				return
			}
		}
	}()
	return nil
}

// tunnelBaseURL derives the ws(s) base for dial-back tunnels: reuse the
// control connection's own endpoint when it is WebSocket (proven reachable,
// TLS state matches); otherwise fall back to the server's advertised HTTP
// host for tcp/kcp control links.
func (c *Client) tunnelBaseURL() string {
	c.mu.Lock()
	base := c.activeWSBase
	c.mu.Unlock()
	if base != "" {
		return base
	}
	if c.httpHost != "" {
		return "ws://" + c.httpHost
	}
	return ""
}
