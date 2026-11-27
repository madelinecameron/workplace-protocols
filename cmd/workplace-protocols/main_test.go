package main

import (
	"bytes"
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
