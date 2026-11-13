package server

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"rdev/internal/protocol"
)

// Native sessions are private to a conversation. UI viewers keep their own
// connections; closing or changing an Agent source does not reconnect the UI.
type agentDesktopSession struct {
	id         string
	client     *ClientConn
	request    protocol.Message
	capture    *automationDesktop
	ready      *protocol.Message
	once       sync.Once
	closed     chan struct{}
	touched    atomic.Int64
	closeRoute func()
}

func (d *agentDesktopSession) close() { d.once.Do(d.closeRoute) }

func (t *computerTool) freshDesktopFrame(ctx context.Context, client *ClientConn, session *agentDesktopSession) ([]byte, error) {
	for {
		select {
		case <-session.capture.frames:
			continue
		default:
			goto drained
		}
	}
drained:
	if session.request.OnDemand {
		if err := client.Send(&protocol.Message{Type: protocol.MsgDesktopSnapshot, SessionID: session.id}); err != nil {
			return nil, err
		}
	}
	select {
	case msg := <-session.capture.messages:
		return nil, fmt.Errorf("desktop: %s", msg.Error)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-session.capture.done:
		return nil, fmt.Errorf("device disconnected")
	case frame := <-session.capture.frames:
		return frame, nil
	}
}

func (t *computerTool) openDesktop(ctx context.Context, client *ClientConn, source string) (*agentDesktopSession, error) {
	reusable := client.Desktop.AgentVersion >= 1
	if reusable {
		if d := t.state.desktop[client.ID]; d != nil {
			alive := true
			select {
			case <-d.closed:
				alive = false
			case <-d.capture.done:
				alive = false
			default:
			}
			select {
			case msg := <-d.capture.messages:
				if msg.Type == protocol.MsgDesktopClose || msg.Error != "" {
					alive = false
				}
			default:
			}
			if alive && d.client == client && d.request.Source == source {
				d.touched.Store(time.Now().UnixNano())
				return d, nil
			}
			d.close()
			delete(t.state.desktop, client.ID)
		}
	}
	id := generateID()
	d := &agentDesktopSession{id: id, client: client, closed: make(chan struct{}),
		request: protocol.Message{Type: protocol.MsgDesktopStart, SessionID: id, Format: "mjpeg", FPS: 2, Quality: 80, Source: source, Width: 1280, Height: 720, OnDemand: reusable},
		capture: &automationDesktop{messages: make(chan *protocol.Message, 8), frames: make(chan []byte, 1), done: make(chan struct{})}}
	d.closeRoute = func() {
		close(d.closed)
		t.srv.desktopMu.Lock()
		delete(t.srv.desktops, id)
		t.srv.desktopMu.Unlock()
		_ = client.Send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: id})
	}
	t.srv.desktopMu.Lock()
	if t.srv.desktops == nil {
		t.srv.desktops = make(map[string]*desktopRoute)
	}
	t.srv.desktops[id] = &desktopRoute{id: id, clientID: client.ID, automation: d.capture}
	t.srv.desktopMu.Unlock()
	if err := client.Send(&d.request); err != nil {
		d.close()
		return nil, err
	}
	for d.ready == nil {
		select {
		case <-ctx.Done():
			d.close()
			return nil, ctx.Err()
		case <-d.capture.done:
			d.close()
			return nil, fmt.Errorf("device disconnected")
		case msg := <-d.capture.messages:
			if msg.Error != "" || msg.Type == protocol.MsgDesktopClose {
				d.close()
				return nil, fmt.Errorf("desktop closed: %s", msg.Error)
			}
			if msg.Type == protocol.MsgDesktopReady {
				d.ready = msg
			}
		}
	}
	if reusable {
		if t.state.desktop == nil {
			t.state.desktop = make(map[string]*agentDesktopSession)
		}
		t.state.desktop[client.ID] = d
		d.touched.Store(time.Now().UnixNano())
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-d.closed:
					return
				case <-d.capture.done:
					d.close()
					return
				case <-ticker.C:
					if time.Since(time.Unix(0, d.touched.Load())) > 2*time.Minute {
						d.close()
						return
					}
				}
			}
		}()
	}
	return d, nil
}
