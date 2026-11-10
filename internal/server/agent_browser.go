package server

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type browserTool struct{ automationTool }

func (*browserTool) Name() string { return "browser" }
func (*browserTool) Description() string {
	return "Use server-hosted Chrome through the selected device's network (not its installed browser or login profile). Each conversation owns a separate tab. Use snapshot for page text and CSS selectors, screenshot for vision, then click/fill/select/hover/scroll/key. Use wait_for with a selector before interacting with asynchronously loaded content. Select uses the option value in text. Re-snapshot after actions to verify their effect. Never infer page state from an earlier conversation. Device is required in server mode."
}
func (*browserTool) Summary(raw json.RawMessage) string { return automationSummary(raw) }
func (*browserTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"},"action":{"type":"string","enum":["status","open","navigate","snapshot","screenshot","click","fill","select","hover","wait_for","scroll","key","close"]},"url":{"type":"string"},"selector":{"type":"string"},"text":{"type":"string","maxLength":20000},"key":{"type":"string","enum":["Enter","Tab","Escape","Backspace","Delete","ArrowUp","ArrowDown","ArrowLeft","ArrowRight","Home","End","PageUp","PageDown"]},"x":{"type":"integer","minimum":0},"y":{"type":"integer","minimum":0},"deltaX":{"type":"integer","minimum":-2000,"maximum":2000},"deltaY":{"type":"integer","minimum":-2000,"maximum":2000}},"required":["action"],"additionalProperties":false}`)
}

type browserAction struct {
	Device, Action, URL, Selector, Text, Key string
	X, Y, DeltaX, DeltaY                     int
}

func (t *browserTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a browserAction
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if !t.srv.BrowserEnabled {
		return "", fmt.Errorf("server browser is disabled")
	}
	if a.Action == "open" || a.Action == "navigate" {
		if err := browserNavigationURL(a.URL); err != nil {
			return "", err
		}
	}
	client, err := t.client(a.Device)
	if err != nil {
		return "", err
	}
	ctx, finish, err := t.begin(ctx)
	if err != nil {
		return "", err
	}
	defer finish()
	if a.Action == "status" {
		t.srv.browserMu.Lock()
		b := t.srv.browsers[client.ID]
		t.srv.browserMu.Unlock()
		page := t.state.pages[client.ID]
		running := b != nil && b.client == client
		if page.browser != b {
			page.target = ""
		}
		return marshalJobResult(map[string]any{"device": client.ID, "runtime": "server Chrome", "network": "selected client", "running": running, "target": page.target})
	}
	b, err := t.srv.browserFor(ctx, client)
	if err != nil {
		return "", err
	}
	page := t.state.pages[client.ID]
	if page.browser != b {
		page = automationPage{browser: b}
	}
	if page.target == "" {
		if a.Action == "close" {
			return "No browser tab is open.", nil
		}
		var target browserTarget
		if err := browserJSON(ctx, b, http.MethodPut, "/json/new?about:blank", &target); err != nil {
			return "", err
		}
		page.target = target.ID
		t.state.pages[client.ID] = page
	}
	t.publish(automationView{Kind: "browser", Device: client.ID, Target: page.target, Base: "/api/browser/view/" + b.capability, Action: a.Action})
	c, err := dialBrowserCDP(ctx, b, page.target)
	if err != nil {
		delete(t.state.pages, client.ID)
		return "", err
	}
	defer c.close()
	switch a.Action {
	case "open", "navigate":
		var result struct {
			ErrorText string `json:"errorText"`
		}
		err = c.call(ctx, "Page.navigate", map[string]any{"url": a.URL}, &result)
		if err == nil && result.ErrorText != "" {
			err = fmt.Errorf("navigation failed: %s", result.ErrorText)
		}
		if err == nil {
			err = waitBrowserDocument(ctx, c)
		}
	case "snapshot":
		return browserEvaluate(ctx, c, browserSnapshotScript)
	case "screenshot":
		var result struct {
			Data string `json:"data"`
		}
		err = c.call(ctx, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 75, "captureBeyondViewport": false}, &result)
		if err != nil {
			return "", err
		}
		data, err := base64.StdEncoding.DecodeString(result.Data)
		if err != nil {
			return "", err
		}
		return automationImage(data)
	case "fill":
		if a.Selector == "" {
			return "", fmt.Errorf("fill requires a selector from snapshot")
		}
		selector, _ := json.Marshal(a.Selector)
		text, _ := json.Marshal(a.Text)
		return browserEvaluate(ctx, c, fmt.Sprintf(`(()=>{const e=document.querySelector(%s);if(!e)throw Error('Element not found');e.focus();if(e instanceof HTMLInputElement || e instanceof HTMLTextAreaElement){const p=e instanceof HTMLInputElement?HTMLInputElement.prototype:HTMLTextAreaElement.prototype;Object.getOwnPropertyDescriptor(p,'value').set.call(e,%s)}else if(e.isContentEditable){e.textContent=%s}else{throw Error('Element is not editable')}e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));return 'Text entered'})()`, selector, text, text))
	case "wait_for":
		if a.Selector == "" {
			return "", fmt.Errorf("wait_for requires a selector")
		}
		selector, _ := json.Marshal(a.Selector)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			result, e := browserEvaluate(ctx, c, fmt.Sprintf(`(()=>{const e=document.querySelector(%s);return !!e&&e.getClientRects().length>0&&getComputedStyle(e).visibility!=='hidden'})()`, selector))
			if e != nil {
				return "", e
			}
			if result == "true" {
				break
			}
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("waiting for element %q: %w", a.Selector, ctx.Err())
			case <-ticker.C:
			}
		}
	case "select":
		if a.Selector == "" {
			return "", fmt.Errorf("select requires a selector from snapshot")
		}
		selector, _ := json.Marshal(a.Selector)
		value, _ := json.Marshal(a.Text)
		return browserEvaluate(ctx, c, fmt.Sprintf(`(()=>{const e=document.querySelector(%s);if(!(e instanceof HTMLSelectElement))throw Error('Select element not found');if(e.disabled)throw Error('Element is disabled');const o=[...e.options].find(o=>o.value===%s);if(!o)throw Error('Option value not found');if(o.disabled||o.parentElement.disabled)throw Error('Option is disabled');e.value=o.value;e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));return 'Option selected'})()`, selector, value))
	case "click", "hover":
		x, y := a.X, a.Y
		if a.Selector != "" {
			selector, _ := json.Marshal(a.Selector)
			rect, e := browserEvaluate(ctx, c, fmt.Sprintf(browserTargetScript, selector))
			if e != nil {
				return "", e
			}
			var point struct{ X, Y int }
			if e = json.Unmarshal([]byte(rect), &point); e != nil {
				return "", e
			}
			x, y = point.X, point.Y
		} else {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			if fields["x"] == nil || fields["y"] == nil {
				return "", fmt.Errorf("click/hover requires a selector or x/y from a recent screenshot")
			}
		}
		if a.Action == "hover" {
			err = c.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y}, nil)
			break
		}
		down := map[string]any{"type": "mousePressed", "x": x, "y": y, "button": "left", "clickCount": 1}
		up := map[string]any{"type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 1}
		err = browserInputPair(ctx, c, b, page.target, "Input.dispatchMouseEvent", down, up)
	case "scroll":
		err = c.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": a.X, "y": a.Y, "deltaX": a.DeltaX, "deltaY": a.DeltaY}, nil)
	case "key":
		code := browserKeyCode(a.Key)
		if code == 0 {
			return "", fmt.Errorf("unsupported key")
		}
		down := map[string]any{"type": "keyDown", "key": a.Key, "windowsVirtualKeyCode": code}
		up := map[string]any{"type": "keyUp", "key": a.Key, "windowsVirtualKeyCode": code}
		if a.Key == "Enter" {
			down["text"] = "\r"
		}
		err = browserInputPair(ctx, c, b, page.target, "Input.dispatchKeyEvent", down, up)
	case "close":
		err = c.call(ctx, "Page.close", map[string]any{}, nil)
		delete(t.state.pages, client.ID)
		t.publish(automationView{})
	default:
		return "", fmt.Errorf("unsupported browser action %q", a.Action)
	}
	if err != nil {
		return "", err
	}
	return marshalJobResult(map[string]string{"device": client.ID, "target": page.target, "action": a.Action, "status": "completed"})
}
func browserEvaluate(ctx context.Context, c *browserCDP, expression string) (string, error) {
	var result struct {
		Result struct {
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if err := c.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}, &result); err != nil {
		return "", err
	}
	if len(result.Exception) > 0 {
		return "", fmt.Errorf("page evaluation failed: %s", result.Result.Description)
	}
	var text string
	if json.Unmarshal(result.Result.Value, &text) == nil {
		return text, nil
	}
	return string(result.Result.Value), nil
}
func waitBrowserDocument(ctx context.Context, c *browserCDP) error {
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		state, err := browserEvaluate(ctx, c, `document.readyState`)
		if err == nil && (state == "interactive" || state == "complete") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func browserKeyCode(key string) int {
	return map[string]int{"Enter": 13, "Tab": 9, "Escape": 27, "Backspace": 8, "Delete": 46, "ArrowUp": 38, "ArrowDown": 40, "ArrowLeft": 37, "ArrowRight": 39, "Home": 36, "End": 35, "PageUp": 33, "PageDown": 34}[key]
}

//go:embed browser_snapshot.js
var browserSnapshotScript string

//go:embed browser_target.js
var browserTargetScript string

// CDP disconnects do not release held buttons/keys. On cancellation, use a
// short independent connection to release before acknowledging user takeover.
func browserInputPair(ctx context.Context, c *browserCDP, b *browserSession, target, method string, down, up any) error {
	pressErr := c.call(ctx, method, down, nil)
	releaseErr := c.call(ctx, method, up, nil)
	if releaseErr != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if release, err := dialBrowserCDP(cleanup, b, target); err == nil {
			_ = release.call(cleanup, method, up, nil)
			release.close()
		}
	}
	if pressErr != nil {
		return pressErr
	}
	return releaseErr
}
