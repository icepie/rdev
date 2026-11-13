package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"rdev/internal/agent"
)

const (
	agentJSSourceLimit = 64 << 10
	agentJSOutputLimit = 64 << 10
	agentJSMemoryLimit = 32 << 20
	agentJSTimeLimit   = 30 * time.Second
)

type agentJSTool struct {
	tools []agent.Tool
}

func (*agentJSTool) Name() string { return "javascript" }
func (*agentJSTool) Description() string {
	return "Run a short JavaScript program to coordinate multiple Agent tools in one call. Use tool(name, args) to invoke registered tools sequentially; each call is schema-validated, individually approved when required, and shown in the tool timeline. Print results with print(...) or console.log(...). " + agentJSToolCatalogDescription()
}
func (*agentJSTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","maxLength":65536}},"required":["code"],"additionalProperties":false}`)
}
func (*agentJSTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", errors.New("javascript tool must be invoked by an Agent session")
}

func agentJSToolCatalogDescription() string {
	return "Available tool schemas and descriptions are exposed as toolSchemas and toolDescriptions. tool(name, args) calls a registered tool with normal validation and approval. fetch(url) performs a bounded server-side GET through web_fetch and returns url, body, and synchronous text() and json() helpers. The runtime has no direct network, filesystem, process, or module access."
}

func (t *agentJSTool) ExecuteWithTools(ctx context.Context, args json.RawMessage, invoke agent.ToolInvoker) (string, error) {
	var input struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("decode JavaScript input: %w", err)
	}
	if len(input.Code) > agentJSSourceLimit {
		return "", fmt.Errorf("JavaScript source exceeds %d bytes", agentJSSourceLimit)
	}
	ctx, cancel := context.WithTimeout(ctx, agentJSTimeLimit)
	defer cancel()

	program, err := moejs.CompileScript("agent-tool.js", input.Code)
	if err != nil {
		return "", fmt.Errorf("compile JavaScript: %w", err)
	}
	rt := moejs.NewRuntime(moejs.Options{
		DisableDynamicCode: true,
		MaxDynamicSource:   agentJSSourceLimit,
		MemoryLimit:        agentJSMemoryLimit,
	})
	defer rt.ReleaseCallData()

	var output strings.Builder
	appendOutput := func(text string) {
		left := agentJSOutputLimit - output.Len()
		if left <= 0 {
			return
		}
		if len(text) > left {
			text = text[:left]
		}
		output.WriteString(text)
	}
	callFn := moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, values []moejs.Value) (moejs.Value, error) {
		if len(values) != 2 {
			return moejs.Undefined(), errors.New("tool(name, args) requires exactly two arguments")
		}
		nameValue, err := rt.ToGo(values[0])
		if err != nil {
			return moejs.Undefined(), err
		}
		name, ok := nameValue.(string)
		if !ok {
			return moejs.Undefined(), errors.New("tool name must be a string")
		}
		toolArgs, err := rt.ToGo(values[1])
		if err != nil {
			return moejs.Undefined(), err
		}
		raw, err := json.Marshal(toolArgs)
		if err != nil {
			return moejs.Undefined(), fmt.Errorf("encode tool arguments: %w", err)
		}
		result, err := invoke(name, raw)
		if err != nil {
			return moejs.Undefined(), err
		}
		return moejs.String(result), nil
	})
	if err := rt.SetGlobal("tool", moejs.NativeFunc(callFn)); err != nil {
		return "", fmt.Errorf("install tool bridge: %w", err)
	}
	fetchFn := moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, values []moejs.Value) (moejs.Value, error) {
		if len(values) != 1 {
			return moejs.Undefined(), errors.New("fetch(url) requires exactly one URL")
		}
		converted, err := rt.ToGo(values[0])
		if err != nil {
			return moejs.Undefined(), err
		}
		url, ok := converted.(string)
		if !ok {
			return moejs.Undefined(), errors.New("fetch URL must be a string")
		}
		fetchArgs, _ := json.Marshal(map[string]string{"url": url})
		body, err := invoke("web_fetch", fetchArgs)
		if err != nil {
			return moejs.Undefined(), err
		}
		textFn := moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			return moejs.String(body), nil
		})
		jsonFn := moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			return rt.ParseJSONString(body)
		})
		return rt.FromGo(map[string]any{"url": url, "body": body, "text": textFn, "json": jsonFn})
	})
	if err := rt.SetGlobal("fetch", moejs.NativeFunc(fetchFn)); err != nil {
		return "", fmt.Errorf("install fetch: %w", err)
	}
	printFn := moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, values []moejs.Value) (moejs.Value, error) {
		parts := make([]string, 0, len(values))
		for _, value := range values {
			converted, err := rt.ToGo(value)
			if err != nil {
				return moejs.Undefined(), err
			}
			parts = append(parts, fmt.Sprint(converted))
		}
		appendOutput(strings.Join(parts, " ") + "\n")
		return moejs.Undefined(), nil
	})
	if err := rt.SetGlobal("print", moejs.NativeFunc(printFn)); err != nil {
		return "", fmt.Errorf("install print: %w", err)
	}
	if err := rt.SetGlobal("toolSchemas", agentJSToolSchemas(t.tools)); err != nil {
		return "", fmt.Errorf("install tool schemas: %w", err)
	}
	if err := rt.SetGlobal("toolDescriptions", agentJSToolDescriptions(t.tools)); err != nil {
		return "", fmt.Errorf("install tool descriptions: %w", err)
	}
	if err := rt.SetGlobal("console", map[string]any{"log": moejs.NativeFunc(printFn)}); err != nil {
		return "", fmt.Errorf("install console: %w", err)
	}

	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			rt.Interrupt(ctx.Err())
		case <-finished:
		}
	}()
	_, runErr := rt.RunScript(program)
	close(finished)
	if runErr != nil {
		return output.String(), fmt.Errorf("run JavaScript: %w", runErr)
	}
	if err := ctx.Err(); err != nil {
		return output.String(), err
	}
	if output.Len() == 0 {
		return "JavaScript completed without printed output.", nil
	}
	return output.String(), nil
}

func agentJSToolSchemas(tools []agent.Tool) map[string]any {
	out := make(map[string]any, len(tools))
	for _, tool := range tools {
		var schema any
		if json.Unmarshal(tool.Schema(), &schema) == nil {
			out[tool.Name()] = schema
		}
	}
	return out
}

func agentJSToolDescriptions(tools []agent.Tool) map[string]string {
	out := make(map[string]string, len(tools))
	for _, tool := range tools {
		out[tool.Name()] = tool.Description()
	}
	return out
}
