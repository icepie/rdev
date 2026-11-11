package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSkillListsPackagesAndResourcesAndLoadsCUAGuidance(t *testing.T) {
	tool := &skillTool{}
	listed, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed, `"cua-driver"`) {
		t.Fatalf("listed packages = %s", listed)
	}
	files, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"list","skill":"cua-driver"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files, `"SKILL.md"`) || !strings.Contains(files, `"WORKFLOW.md"`) {
		t.Fatalf("listed resources = %s", files)
	}
	for file, marker := range map[string]string{
		"SKILL.md":    "version: 0.47.1",
		"WORKFLOW.md": "Cursor visibility",
	} {
		request := json.RawMessage(`{"action":"read","skill":"cua-driver","file":"` + file + `"}`)
		got, err := tool.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(got, marker) {
			t.Errorf("%s missing %q", file, marker)
		}
	}
}

func TestSkillRejectsUnknownPackagesAndPathTraversal(t *testing.T) {
	tool := &skillTool{}
	for _, request := range []string{
		`{"action":"read","skill":"missing","file":"SKILL.md"}`,
		`{"action":"read","skill":"cua-driver","file":"../../etc/passwd"}`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(request)); err == nil {
			t.Errorf("accepted unsafe or unknown request %s", request)
		}
	}
}
