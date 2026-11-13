package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"rdev/internal/agent"
)

type agentJSTestTool struct{}

func (*agentJSTestTool) Name() string        { return "echo" }
func (*agentJSTestTool) Description() string { return "echo test payload" }
func (*agentJSTestTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`)
}
func (*agentJSTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "unexpected direct call", nil
}

func TestAgentJSToolInvokesRegisteredToolsAndFetch(t *testing.T) {
	tool := &agentJSTestTool{}
	js := &agentJSTool{tools: []agent.Tool{tool}}
	var names []string
	out, err := js.ExecuteWithTools(context.Background(), json.RawMessage(`{"code":"const result = tool('echo', {value: 'hello'}); const response = fetch('https://example.test/data'); print(response.url, response.text(), result); print(JSON.stringify(toolSchemas.echo));"}`), func(name string, args json.RawMessage) (string, error) {
		names = append(names, name)
		switch name {
		case "echo":
			var input struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal(args, &input); err != nil {
				return "", err
			}
			return input.Value, nil
		case "web_fetch":
			var input struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(args, &input); err != nil {
				return "", err
			}
			if input.URL != "https://example.test/data" {
				t.Fatalf("fetch URL = %q", input.URL)
			}
			return `{"ok":true}`, nil
		default:
			t.Fatalf("unexpected tool %q", name)
			return "", nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `https://example.test/data {"ok":true} hello`) || !strings.Contains(out, `"required":["value"]`) {
		t.Fatalf("JavaScript output missing delegated results/schema: %s", out)
	}
	if len(names) != 2 || names[0] != "echo" || names[1] != "web_fetch" {
		t.Fatalf("invoked tools = %v", names)
	}
}

func TestAgentJSToolDisablesDynamicCodeAndBoundsSource(t *testing.T) {
	js := &agentJSTool{}
	for _, code := range []string{
		`eval("1+1")`,
		`new Function("return 1")()`,
	} {
		_, err := js.ExecuteWithTools(context.Background(), mustAgentJSArgs(code), func(string, json.RawMessage) (string, error) {
			t.Fatal("unexpected tool invocation")
			return "", nil
		})
		if err == nil {
			t.Fatalf("dynamic code was accepted: %s", code)
		}
	}
	_, err := js.ExecuteWithTools(context.Background(), mustAgentJSArgs(strings.Repeat(" ", agentJSSourceLimit+1)), func(string, json.RawMessage) (string, error) {
		t.Fatal("unexpected tool invocation")
		return "", nil
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized source error = %v", err)
	}
}

func mustAgentJSArgs(code string) json.RawMessage {
	args, _ := json.Marshal(map[string]string{"code": code})
	return args
}
