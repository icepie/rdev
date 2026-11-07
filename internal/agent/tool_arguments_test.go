package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type schemaTool struct {
	fakeTool
	schema string
}

func (t *schemaTool) Schema() json.RawMessage { return json.RawMessage(t.schema) }

const writeArgumentsSchema = `{
	"type":"object",
	"properties":{"path":{"type":"string"},"content":{"type":"string"}},
	"required":["path","content"],"additionalProperties":false
}`

func TestToolArgumentValidationPrecedesApprovalAndExecution(t *testing.T) {
	for _, args := range []string{
		`{"path":"/file"}`, `{"path":"/file","content":null}`,
		`{"path":"/file","content":42}`, `null`, `[]`,
		`{"path":"/file","content":"","unexpected":true}`,
	} {
		t.Run(args, func(t *testing.T) {
			tool := &schemaTool{fakeTool: fakeTool{name: "write_file", dangerous: true}, schema: writeArgumentsSchema}
			approvals := 0
			sess := New(Config{Approve: func(ToolCall, json.RawMessage) (bool, error) { approvals++; return true, nil }})
			result, failed := sess.executeTool(context.Background(), tool, ToolCall{Name: tool.Name()}, json.RawMessage(args))
			if !failed || !strings.Contains(result, "invalid arguments") || tool.ran || approvals != 0 {
				t.Fatalf("failed=%v ran=%v approvals=%d result=%s", failed, tool.ran, approvals, result)
			}
		})
	}
}

func TestInvalidToolArgumentsRecoverOnNextModelRound(t *testing.T) {
	provider, hits := newFakeProvider(t,
		sseChunk("", "", sseToolCall("missing", "write_file", `{"path":"/file"}`))+sseDone,
		sseChunk("", "", sseToolCall("corrected", "write_file", `{"path":"/file","content":""}`))+sseDone,
		sseChunk("finished", "")+sseDone,
	)
	tool := &schemaTool{fakeTool: fakeTool{name: "write_file", dangerous: true}, schema: writeArgumentsSchema}
	cfg := testConfig(provider.URL, []Tool{tool})
	approvals := 0
	cfg.Approve = func(_ ToolCall, args json.RawMessage) (bool, error) {
		approvals++
		if string(args) != `{"path":"/file","content":""}` {
			t.Fatalf("arguments changed before approval: %s", args)
		}
		return true, nil
	}
	sess := New(cfg)
	emit, events := collectEvents()
	answer, err := sess.Run(context.Background(), "write empty file", emit)
	if err != nil || answer != "finished" || hits.Load() != 3 || !tool.ran || approvals != 1 {
		t.Fatalf("answer=%q error=%v hits=%d ran=%v approvals=%d", answer, err, hits.Load(), tool.ran, approvals)
	}
	history := sess.History()
	if len(history) != 6 || history[2].ToolCallID != "missing" || !strings.Contains(history[2].Content, "content") ||
		history[4].ToolCallID != "corrected" || history[4].Content != "tool output" {
		t.Fatalf("invalid tool/result history: %+v", history)
	}
	ends := eventsOfType(*events, EventToolEnd)
	if len(ends) != 2 || !ends[0].IsError || ends[1].IsError {
		t.Fatalf("wrong tool lifecycle: %+v", ends)
	}
}

func TestToolSchemaConstraintsAndLocalReferences(t *testing.T) {
	schema := `{"type":"object","properties":{"jobs":{"type":"array","minItems":1,"items":{"$ref":"#/$defs/job"}}},
		"required":["jobs"],"$defs":{"job":{"type":"object","properties":{
		"action":{"enum":["read","wait"]},"timeout":{"type":"integer","minimum":1,"maximum":30}},
		"required":["action"],"additionalProperties":false}}}`
	tool := &schemaTool{schema: schema}
	sess := New(Config{})
	for _, args := range []string{
		`{"jobs":[]}`, `{"jobs":[{}]}`, `{"jobs":[{"action":"delete"}]}`,
		`{"jobs":[{"action":"wait","timeout":0}]}`, `{"jobs":[{"action":"wait","timeout":31}]}`,
		`{"jobs":[{"action":"wait","timeout":1.5}]}`,
	} {
		if err := sess.toolArguments.Validate(tool, json.RawMessage(args)); err == nil {
			t.Errorf("accepted invalid args: %s", args)
		}
	}
	if err := sess.toolArguments.Validate(tool, json.RawMessage(`{"jobs":[{"action":"wait","timeout":30}]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidOrExternalToolSchemaCannotExecute(t *testing.T) {
	for _, schema := range []string{`{`, `{"type":"not-a-type"}`, `{"$ref":"file:///tmp/rdev-schema.json"}`, `{"$ref":"https://example.com/schema.json"}`} {
		tool := &schemaTool{fakeTool: fakeTool{name: "invalid"}, schema: schema}
		sess := New(Config{})
		result, failed := sess.executeTool(context.Background(), tool, ToolCall{Name: tool.Name()}, json.RawMessage(`{}`))
		if !failed || tool.ran || !strings.Contains(result, "invalid tool schema") {
			t.Fatalf("schema=%s failed=%v ran=%v result=%s", schema, failed, tool.ran, result)
		}
	}
}
