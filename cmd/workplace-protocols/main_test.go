package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var example = filepath.Join("..", "..", "examples", "workplace-protocols.yaml")

func TestValidateTheShippedExample(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-f", example, "validate"}, &stdout, &stderr); err != nil {
		t.Fatalf("validate: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "is valid: 3 tools, 5 MCP servers, 2 services") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRejectsUnknownCommandsAndClients(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"-f", example, "deploy"}, &out, &out); err == nil {
		t.Fatal("an unknown command should fail")
	}
	if err := run([]string{"-f", example, "render", "vim"}, &out, &out); err == nil {
		t.Fatal("an unknown client should fail")
	}
}

func TestRenderClaudeWithValuesLeavesOutServersStillMissingOne(t *testing.T) {
	values := filepath.Join(t.TempDir(), "values.json")
	if err := os.WriteFile(values, []byte(`{"OTHER": "x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{"-f", example, "-values", values, "render", "claude"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "mailbox (needs MAILBOX_TOKEN)") {
		t.Fatalf("error = %v, want mailbox named as missing MAILBOX_TOKEN", err)
	}
	if strings.Contains(stdout.String(), "mailbox") || !strings.Contains(stdout.String(), "playwright") {
		t.Fatalf("stdout = %s", stdout.String())
	}

	if err := os.WriteFile(values, []byte(`{"MAILBOX_TOKEN": "s3cret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := run([]string{"-f", example, "-values", values, "render", "claude"}, &stdout, &stderr); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(stdout.String(), `"Bearer s3cret"`) {
		t.Fatalf("stdout = %s, want the value filled in", stdout.String())
	}
}

func TestListPrintsEveryEntry(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-f", example, "list", "-json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
}

func TestServersJSONSeparatesWhatClaudeNeeds(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-f", example, "servers", "-json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Name        string   `json:"name"`
		Needs       []string `json:"needs"`
		ClaudeNeeds []string `json:"claude_needs"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == "mailbox" && (len(r.Needs) != 1 || len(r.ClaudeNeeds) != 1) {
			t.Fatalf("mailbox = %+v", r)
		}
	}
}
