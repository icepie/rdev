package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

//go:embed browser_cursor.js
var browserCursorScript string

type browserCursor struct {
	c      *browserCDP
	script string
}

// The registration belongs to a CDP session: retain it across individual tool
// calls, then remove it before closing the session at the end of the turn.
func newBrowserCursor(ctx context.Context, b *browserSession, target string) (*browserCursor, error) {
	// Tool contexts end after each action; the cursor is owned by the turn.
	// Handshake has its own five-second timeout; all commands still use ctx.
	c, err := dialBrowserCDP(context.Background(), b, target)
	if err != nil {
		return nil, err
	}
	if err = c.call(ctx, "Page.enable", map[string]any{}, nil); err != nil {
		c.close()
		return nil, err
	}
	var result struct {
		Identifier string `json:"identifier"`
	}
	if err = c.call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": browserCursorScript}, &result); err != nil {
		c.close()
		return nil, err
	}
	cursor := &browserCursor{c: c, script: result.Identifier}
	if _, err = browserEvaluate(ctx, c, browserCursorScript); err != nil {
		cursor.close()
		return nil, err
	}
	return cursor, nil
}

func (cursor *browserCursor) place(ctx context.Context, x, y int, click bool) error {
	// Reinstall idempotently for an already-loaded document, including navigation
	// races; coordinates come from the same resolved point as the native input.
	_, err := browserEvaluate(ctx, cursor.c, browserCursorScript+fmt.Sprintf("\nwindow.__rdevAgentCursor.place(%d,%d,%t)", x, y, click))
	return err
}

// Move the real CDP pointer and its overlay together along a bounded quadratic
// curve. The control point stays inside the endpoint rectangle: no overshoot
// outside the viewport and no randomized click destination.
func (cursor *browserCursor) move(ctx context.Context, x, y int, click bool) error {
	// Chrome can defer input acknowledgments on a background page. Activate
	// this conversation's target before sending its native movement sequence.
	if err := cursor.c.call(ctx, "Page.bringToFront", map[string]any{}, nil); err != nil {
		return err
	}
	start, err := browserEvaluate(ctx, cursor.c, browserCursorScript+"\n"+`(()=>{const p=document.querySelector('rdev-agent-cursor');return {x:p?parseFloat(p.style.left):0,y:p?parseFloat(p.style.top):0}})()`)
	if err != nil {
		return err
	}
	var from struct{ X, Y float64 }
	if err = json.Unmarshal([]byte(start), &from); err != nil {
		return err
	}
	dx, dy := float64(x)-from.X, float64(y)-from.Y
	distance := math.Hypot(dx, dy)
	steps := min(24, max(1, int(distance/25)))
	cx, cy := from.X+dx*(0.2+rand.Float64()*0.6), from.Y+dy*(0.2+rand.Float64()*0.6)
	for i := 1; i <= steps; i++ {
		if err = browserCursorWait(ctx, time.Duration(12+rand.IntN(13))*time.Millisecond); err != nil {
			return err
		}
		u := float64(i) / float64(steps)
		u = u * u * (3 - 2*u)
		px := (1-u)*(1-u)*from.X + 2*(1-u)*u*cx + u*u*float64(x)
		py := (1-u)*(1-u)*from.Y + 2*(1-u)*u*cy + u*u*float64(y)
		if err = cursor.c.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": px, "y": py}, nil); err != nil {
			return err
		}
		if _, err = browserEvaluate(ctx, cursor.c, fmt.Sprintf("window.__rdevAgentCursor.place(%f,%f,false)", px, py)); err != nil {
			return err
		}
	}
	if click {
		return browserCursorWait(ctx, time.Duration(40+rand.IntN(81))*time.Millisecond)
	}
	return nil
}

func browserCursorWait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func (cursor *browserCursor) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cursor.c.call(ctx, "Page.removeScriptToEvaluateOnNewDocument", map[string]any{"identifier": cursor.script}, nil)
	_, _ = browserEvaluate(ctx, cursor.c, `window.__rdevAgentCursor?.dispose()`)
	cursor.c.close()
}

// Caller holds opMu: no input can race disposal or recreate a retired cursor.
func clearBrowserCursors(state *agentAutomation) {
	for device, page := range state.pages {
		if page.cursor != nil {
			page.cursor.close()
			page.cursor = nil
			state.pages[device] = page
		}
	}
}
