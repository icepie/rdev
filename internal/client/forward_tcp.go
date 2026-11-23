package client

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	"rdev/internal/protocol"
)

type pendingTCPDial struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (c *Client) handleBinTCPData(forwardID string, data []byte) {
	c.mu.Lock()
	conn, ok := c.forwards[forwardID]
	c.mu.Unlock()
	if !ok || len(data) == 0 {
		return
	}
	conn.Write(data)
}

// --- TCP forwarding (-L) ---

func (c *Client) handleTCPConnect(msg *protocol.Message) {
	addr := net.JoinHostPort(msg.Host, fmt.Sprintf("%d", msg.Port))
	log.Printf("forward: connecting to %s (id=%s)", addr, msg.ForwardID)

	c.mu.Lock()
	if c.tcpDials == nil {
		c.tcpDials = make(map[string]*pendingTCPDial)
	}
	if len(c.tcpDials) >= 64 || c.tcpDials[msg.ForwardID] != nil || c.forwards[msg.ForwardID] != nil {
		c.mu.Unlock()
		c.send(&protocol.Message{Type: protocol.MsgTCPFail, ForwardID: msg.ForwardID, Error: "TCP dial capacity or duplicate forward"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pending := &pendingTCPDial{ctx: ctx, cancel: cancel}
	c.tcpDials[msg.ForwardID] = pending
	c.mu.Unlock()
	go c.finishTCPConnect(msg, addr, pending)
}

func (c *Client) finishTCPConnect(msg *protocol.Message, addr string, pending *pendingTCPDial) {
	defer pending.cancel()
	conn, err := (&net.Dialer{}).DialContext(pending.ctx, "tcp", addr)
	c.mu.Lock()
	current := c.tcpDials[msg.ForwardID] == pending
	if c.tcpDials[msg.ForwardID] == pending {
		delete(c.tcpDials, msg.ForwardID)
	}
	if current && err == nil {
		c.forwards[msg.ForwardID] = conn
	}
	c.mu.Unlock()
	if !current {
		if conn != nil {
			conn.Close()
		}
		return
	}
	if err != nil {
		log.Printf("forward: connect to %s failed: %v", addr, err)
		c.send(&protocol.Message{
			Type:      protocol.MsgTCPFail,
			ForwardID: msg.ForwardID,
			Error:     err.Error(),
		})
		return
	}

	// Fast path: dedicated raw WebSocket for this stream. On any dial-back
	// failure fall through to the framed control-channel path.
	if msg.Tunnel != "" {
		if err := c.runTCPTunnel(msg, conn); err != nil {
			log.Printf("forward: tunnel dial-back failed (id=%s), using framed path: %v", msg.ForwardID, err)
		} else {
			log.Printf("forward: connected to %s via tunnel (id=%s)", addr, msg.ForwardID)
			return
		}
	}

	c.mu.Lock()
	active := c.forwards[msg.ForwardID] == conn
	c.mu.Unlock()
	if !active {
		conn.Close()
		return
	}
	c.send(&protocol.Message{Type: protocol.MsgTCPOpen, ForwardID: msg.ForwardID})

	// Read TCP response → binary frames
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				c.sendBinary(protocol.BinTCPData, msg.ForwardID, buf[:n])
			}
			if err != nil {
				c.send(&protocol.Message{Type: protocol.MsgTCPClose, ForwardID: msg.ForwardID})
				c.mu.Lock()
				if c.forwards[msg.ForwardID] == conn {
					delete(c.forwards, msg.ForwardID)
				}
				c.mu.Unlock()
				return
			}
		}
	}()

	log.Printf("forward: connected to %s (id=%s)", addr, msg.ForwardID)
}

func (c *Client) handleTCPListen(msg *protocol.Message) {
	addr := net.JoinHostPort(msg.Host, fmt.Sprintf("%d", msg.Port))
	log.Printf("forward listen: %s (listenID=%s)", addr, msg.ListenID)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		c.send(&protocol.Message{Type: protocol.MsgTCPListenOK, ListenID: msg.ListenID, Error: err.Error()})
		return
	}
	_, portText, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portText)

	c.mu.Lock()
	if old := c.listeners[msg.ListenID]; old != nil {
		old.Close()
	}
	c.listeners[msg.ListenID] = ln
	c.mu.Unlock()
	c.send(&protocol.Message{Type: protocol.MsgTCPListenOK, ListenID: msg.ListenID, Port: port})

	go func() {
		defer func() {
			c.mu.Lock()
			if c.listeners[msg.ListenID] == ln {
				delete(c.listeners, msg.ListenID)
			}
			c.mu.Unlock()
		}()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			forwardID := generateClientForwardID()
			openCh := make(chan struct{})
			c.mu.Lock()
			c.forwards[forwardID] = conn
			c.forwardOpen[forwardID] = openCh
			c.mu.Unlock()
			c.send(&protocol.Message{
				Type:       protocol.MsgTCPAccept,
				ListenID:   msg.ListenID,
				ForwardID:  forwardID,
				SourceAddr: conn.RemoteAddr().String(),
			})
			go func() {
				select {
				case <-openCh:
					c.readTCPForward(forwardID, conn)
				case <-time.After(10 * time.Second):
					c.handleTCPClose(&protocol.Message{ForwardID: forwardID})
				}
			}()
		}
	}()
}

func (c *Client) handleTCPOpen(msg *protocol.Message) {
	c.mu.Lock()
	openCh := c.forwardOpen[msg.ForwardID]
	delete(c.forwardOpen, msg.ForwardID)
	c.mu.Unlock()
	if openCh != nil {
		safeCloseForwardOpen(openCh)
	}
}

func (c *Client) readTCPForward(forwardID string, conn net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			c.sendBinary(protocol.BinTCPData, forwardID, buf[:n])
		}
		if err != nil {
			c.send(&protocol.Message{Type: protocol.MsgTCPClose, ForwardID: forwardID})
			c.mu.Lock()
			if c.forwards[forwardID] == conn {
				delete(c.forwards, forwardID)
			}
			delete(c.forwardOpen, forwardID)
			c.mu.Unlock()
			return
		}
	}
}

func (c *Client) handleTCPClose(msg *protocol.Message) {
	if msg.ListenID != "" {
		c.mu.Lock()
		ln, ok := c.listeners[msg.ListenID]
		if ok {
			delete(c.listeners, msg.ListenID)
		}
		c.mu.Unlock()
		if ok {
			ln.Close()
			log.Printf("forward listen: closed %s", msg.ListenID)
		}
		return
	}
	c.mu.Lock()
	if pending := c.tcpDials[msg.ForwardID]; pending != nil {
		pending.cancel()
		delete(c.tcpDials, msg.ForwardID)
	}
	conn, ok := c.forwards[msg.ForwardID]
	if ok {
		delete(c.forwards, msg.ForwardID)
	}
	openCh := c.forwardOpen[msg.ForwardID]
	delete(c.forwardOpen, msg.ForwardID)
	c.mu.Unlock()
	if openCh != nil {
		safeCloseForwardOpen(openCh)
	}
	if ok {
		conn.Close()
		log.Printf("forward: closed %s", msg.ForwardID)
	}
}

func safeCloseForwardOpen(ch chan struct{}) {
	defer func() { recover() }()
	close(ch)
}

func generateClientForwardID() string {
	return fmt.Sprintf("cf-%d-%d", time.Now().UnixNano(), os.Getpid())
}
