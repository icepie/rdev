package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	autojsMaxScriptBytes = 65536
	autojsMaxTimeoutMS   = 25000
	autojsMaxResultBytes = 128 << 10
)

type autojsTool struct{ automationTool }

func (*autojsTool) Name() string                       { return "autojs" }
func (*autojsTool) Summary(raw json.RawMessage) string { return automationSummary(raw) }
func (*autojsTool) Description() string {
	return "Run background Rhino JavaScript with the genuine AutoJs6 engine embedded in the selected Android device, never on the server. status reports engine/version and accessibility readiness. run executes script (maximum 65536 UTF-8 bytes) and returns the last expression as text, bounded logs and exceptions; use JSON.stringify for structured values. UI-mode and external-engine scripts are not supported. Enable RDev AutoJs Scripts in Android accessibility settings for UI automation. timeoutMs defaults to 25000 and larger values are capped at 25000. Each call exclusively claims the desktop for this conversation; user takeover cancels execution. In a server conversation, select device explicitly using list_devices."
}
func (*autojsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"},"action":{"type":"string","enum":["status","run"]},"script":{"type":"string","maxLength":65536},"timeoutMs":{"type":"integer","minimum":1}},"required":["action"],"additionalProperties":false}`)
}

func (t *autojsTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Device    string `json:"device"`
		Action    string `json:"action"`
		Script    string `json:"script"`
		TimeoutMS *int   `json:"timeoutMs"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	if args.Action != "status" && args.Action != "run" {
		return "", fmt.Errorf("action must be status or run")
	}
	if args.Action == "run" && strings.TrimSpace(args.Script) == "" {
		return "", fmt.Errorf("run requires a nonempty script")
	}
	if args.Action == "status" && args.Script != "" {
		return "", fmt.Errorf("script is only accepted for run")
	}
	if len(args.Script) > autojsMaxScriptBytes {
		return "", fmt.Errorf("script exceeds %d UTF-8 bytes", autojsMaxScriptBytes)
	}
	timeoutMS := autojsMaxTimeoutMS
	if args.TimeoutMS != nil {
		if *args.TimeoutMS < 1 {
			return "", fmt.Errorf("timeoutMs must be a positive integer")
		}
		timeoutMS = min(*args.TimeoutMS, autojsMaxTimeoutMS)
	}
	if t.thread.deviceScope != nil {
		scoped, release, scopeErr := t.thread.deviceScope.begin(ctx, args.Device)
		if scopeErr != nil {
			return "", scopeErr
		}
		defer release()
		ctx = scoped
	}
	client, err := t.client(args.Device)
	if err != nil {
		return "", err
	}
	if client.OS != "android" && (client.Desktop == nil || client.Desktop.Platform != "android") {
		return "", fmt.Errorf("autojs requires an Android device")
	}
	ctx, finish, err := t.begin(ctx)
	if err != nil {
		return "", err
	}
	defer finish()
	if err := t.claim(client.ID); err != nil {
		return "", err
	}
	t.publish(automationView{Kind: "desktop", Device: client.ID, Action: "autojs " + args.Action})
	d, err := t.srv.newGPUAgentDesktop(ctx, client)
	if err != nil {
		return "", err
	}
	defer d.close()
	stopClose := context.AfterFunc(ctx, d.close)
	defer stopClose()

	// Negotiate before sending: older clients otherwise ignore AutoJs forever.
	capsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = d.wait(capsCtx, "AgentCapabilities")
	cancel()
	if err != nil {
		return "", fmt.Errorf("Auto.js capabilities unavailable; update the Android client: %w", err)
	}
	if !d.autojs {
		return "", fmt.Errorf("update the Android client to enable its embedded Auto.js engine")
	}
	request := struct {
		Action    string `json:"action"`
		Script    string `json:"script,omitempty"`
		TimeoutMS int    `json:"timeoutMs"`
	}{args.Action, args.Script, timeoutMS}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := d.send(map[string]any{"AutoJs": request}); err != nil {
		return "", err
	}
	result, err := d.wait(ctx, "AutoJsResult")
	if err != nil {
		return "", err
	}
	if len(result) > autojsMaxResultBytes {
		return "", fmt.Errorf("Auto.js result exceeds %d bytes", autojsMaxResultBytes)
	}
	var outcome struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(result, &outcome); err != nil || string(result) == "null" {
		return "", fmt.Errorf("invalid Auto.js result")
	}
	out := string(result)
	if outcome.Status == "error" || outcome.Status == "timeout" || outcome.Status == "canceled" || (args.Action == "run" && outcome.Status != "finished") {
		// The Agent consumer discards output on error, so keep the complete
		// bounded JSON (including logs and exception) in the error itself.
		return out, errors.New(out)
	}
	return out, nil
}
