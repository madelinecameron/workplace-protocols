package sources

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// returningServer serves one allowlisted tool that always answers with result,
// so a test can pin exactly how a given shape of MCP result renders.
func returningServer(name string, result *mcp.CallToolResult) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "returning", Version: "0.0.1"}, nil)
	server.AddTool(readOnly(name), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return result, nil
	})
	return server
}

func callProbe(t *testing.T, result *mcp.CallToolResult, opts ...Option) (Result, error) {
	t.Helper()
	config := Server{ID: "probe", Command: "probe", Allow: []string{"read"}}
	dial := memoryDialer(t, map[string]*mcp.Server{"probe": returningServer("read", result)})
	set, err := open(t.Context(), Config{Servers: []Server{config}}, dial, newSettings(opts))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { closeSet(t, set) })
	return set.Call(t.Context(), "sources__probe__read", map[string]any{})
}

// TestRenderContract pins how every shape of MCP result reaches a caller. A
// change that starts dropping something new, or that stops reporting a drop,
// has to fail here rather than surprise a reader downstream.
func TestRenderContract(t *testing.T) {
	image := &mcp.ImageContent{Data: []byte("not-really-a-png"), MIMEType: "image/png"}
	audio := &mcp.AudioContent{Data: []byte("not-really-a-wav"), MIMEType: "audio/wav"}

	tests := []struct {
		name           string
		result         *mcp.CallToolResult
		wantText       string
		wantDropped    []string
		wantDisplaced  bool
		wantErrMessage string
	}{
		{
			name: "text blocks join with newlines",
			result: &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "first"},
				&mcp.TextContent{Text: "second"},
				&mcp.TextContent{Text: "third"},
			}},
			wantText: "first\nsecond\nthird",
		},
		{
			name: "an image between two text blocks is reported, not swallowed",
			result: &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "before"},
				image,
				&mcp.TextContent{Text: "after"},
			}},
			wantText:    "before\nafter",
			wantDropped: []string{"image"},
		},
		{
			name:        "an image alone leaves empty text and a visible gap",
			result:      &mcp.CallToolResult{Content: []mcp.Content{image}},
			wantText:    "",
			wantDropped: []string{"image"},
		},
		{
			name:        "repeated kinds are listed once, in the order first seen",
			result:      &mcp.CallToolResult{Content: []mcp.Content{audio, image, audio}},
			wantText:    "",
			wantDropped: []string{"audio", "image"},
		},
		{
			name: "structured content displaces text and says so",
			result: &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: "human readable"}},
				StructuredContent: map[string]any{"count": 3},
			},
			wantText:      `{"count":3}`,
			wantDisplaced: true,
		},
		{
			name: "structured content still reports non-text drops",
			result: &mcp.CallToolResult{
				Content:           []mcp.Content{image},
				StructuredContent: map[string]any{"count": 3},
			},
			wantText:    `{"count":3}`,
			wantDropped: []string{"image"},
		},
		{
			name: "structured content alone displaces nothing",
			result: &mcp.CallToolResult{
				StructuredContent: map[string]any{"count": 3},
			},
			wantText: `{"count":3}`,
		},
		{
			name:     "an empty result is empty with no omissions",
			result:   &mcp.CallToolResult{},
			wantText: "",
		},
		{
			name: "a tool error surfaces its text as the error",
			result: &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "the source said no"}},
			},
			wantErrMessage: "the source said no",
		},
		{
			name:           "a tool error with no text still says something",
			result:         &mcp.CallToolResult{IsError: true},
			wantErrMessage: "source tool reported an error",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := callProbe(t, test.result)

			if test.wantErrMessage != "" {
				if err == nil || err.Error() != test.wantErrMessage {
					t.Fatalf("error = %v, want %q", err, test.wantErrMessage)
				}
				if result.Omitted() || result.Text != "" {
					t.Errorf("an errored call returned %+v, want the zero Result", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if result.Text != test.wantText {
				t.Errorf("Text = %q, want %q", result.Text, test.wantText)
			}
			if !slices.Equal(result.DroppedKinds, test.wantDropped) {
				t.Errorf("DroppedKinds = %v, want %v", result.DroppedKinds, test.wantDropped)
			}
			if result.StructuredDisplacedText != test.wantDisplaced {
				t.Errorf("StructuredDisplacedText = %v, want %v", result.StructuredDisplacedText, test.wantDisplaced)
			}
			if result.Truncated {
				t.Error("Truncated set with no cap configured")
			}
			wantOmitted := len(test.wantDropped) > 0 || test.wantDisplaced
			if result.Omitted() != wantOmitted {
				t.Errorf("Omitted() = %v, want %v", result.Omitted(), wantOmitted)
			}
		})
	}
}

func TestMaxResultBytesIsOffByDefault(t *testing.T) {
	huge := strings.Repeat("x", 8<<20)
	result, err := callProbe(t, &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: huge}},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(result.Text) != len(huge) {
		t.Errorf("Text length = %d, want the whole %d bytes", len(result.Text), len(huge))
	}
	if result.Truncated {
		t.Error("Truncated set although no cap was configured")
	}
}

func TestMaxResultBytesTruncatesAndFlags(t *testing.T) {
	huge := strings.Repeat("x", 8<<20)
	result, err := callProbe(t,
		&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: huge}}},
		WithMaxResultBytes(1024),
	)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(result.Text) != 1024 {
		t.Errorf("Text length = %d, want 1024", len(result.Text))
	}
	if !result.Truncated {
		t.Error("Truncated not set on a capped read")
	}
	if !result.Omitted() {
		t.Error("Omitted() false on a truncated read")
	}
	if strings.Contains(result.Text, "truncat") {
		t.Error("truncation was written into the text a source controls")
	}
}

func TestMaxResultBytesLeavesAShortResultAlone(t *testing.T) {
	result, err := callProbe(t,
		&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "short"}}},
		WithMaxResultBytes(1024),
	)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.Text != "short" || result.Truncated {
		t.Errorf("result = %+v, want the text intact and untruncated", result)
	}
}

func TestMaxResultBytesCapsAnErrorMessage(t *testing.T) {
	_, err := callProbe(t,
		&mcp.CallToolResult{IsError: true, Content: []mcp.Content{
			&mcp.TextContent{Text: strings.Repeat("e", 4096)},
		}},
		WithMaxResultBytes(64),
	)
	if err == nil {
		t.Fatal("Call succeeded on an error result")
	}
	if len(err.Error()) != 64 {
		t.Errorf("error length = %d, want it capped to 64", len(err.Error()))
	}
}

func TestTruncateKeepsRunesWhole(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		max           int
		want          string
		wantTruncated bool
	}{
		{name: "no cap", text: "hello", max: 0, want: "hello"},
		{name: "negative cap", text: "hello", max: -1, want: "hello"},
		{name: "cap above length", text: "hello", max: 99, want: "hello"},
		{name: "cap at length", text: "hello", max: 5, want: "hello"},
		{name: "ascii cut", text: "hello", max: 3, want: "hel", wantTruncated: true},
		{name: "cut inside a multibyte rune", text: "héllo", max: 2, want: "h", wantTruncated: true},
		{name: "cut on a rune boundary", text: "héllo", max: 3, want: "hé", wantTruncated: true},
		{name: "cut before any whole rune", text: "é", max: 1, want: "", wantTruncated: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, truncated := truncate(test.text, test.max)
			if got != test.want || truncated != test.wantTruncated {
				t.Errorf("truncate(%q, %d) = %q, %v; want %q, %v",
					test.text, test.max, got, truncated, test.want, test.wantTruncated)
			}
			if !isValidUTF8(got) {
				t.Errorf("truncate(%q, %d) produced a broken rune", test.text, test.max)
			}
		})
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestOriginalBytesRecordsWhatWasElided(t *testing.T) {
	tests := []struct {
		name              string
		text              string
		opts              []Option
		wantText          int
		wantOriginalBytes int
		wantTruncated     bool
	}{
		{
			name:              "no cap records the full length",
			text:              strings.Repeat("x", 5000),
			wantText:          5000,
			wantOriginalBytes: 5000,
		},
		{
			name:              "cap above the text leaves it whole",
			text:              strings.Repeat("x", 100),
			opts:              []Option{WithMaxResultBytes(4096)},
			wantText:          100,
			wantOriginalBytes: 100,
		},
		{
			name:              "cap below the text records both lengths",
			text:              strings.Repeat("x", 5000),
			opts:              []Option{WithMaxResultBytes(1024)},
			wantText:          1024,
			wantOriginalBytes: 5000,
			wantTruncated:     true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := callProbe(t,
				&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: test.text}}},
				test.opts...,
			)
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if len(result.Text) != test.wantText {
				t.Errorf("len(Text) = %d, want %d", len(result.Text), test.wantText)
			}
			if result.OriginalBytes != test.wantOriginalBytes {
				t.Errorf("OriginalBytes = %d, want %d", result.OriginalBytes, test.wantOriginalBytes)
			}
			if result.Truncated != test.wantTruncated {
				t.Errorf("Truncated = %v, want %v", result.Truncated, test.wantTruncated)
			}
			if elided := result.OriginalBytes - len(result.Text); elided != test.wantOriginalBytes-test.wantText {
				t.Errorf("elided = %d, want %d", elided, test.wantOriginalBytes-test.wantText)
			}
		})
	}
}

func TestOriginalBytesCountsBeforeTheRuneBoundaryBackup(t *testing.T) {
	// Two bytes of a three byte rune sit across the cap, so the cut lands below
	// it and the elided count has to come out of OriginalBytes, not the cap.
	result, err := callProbe(t,
		&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ab€cd"}}},
		WithMaxResultBytes(3),
	)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.Text != "ab" {
		t.Errorf("Text = %q, want %q", result.Text, "ab")
	}
	if result.OriginalBytes != len("ab€cd") {
		t.Errorf("OriginalBytes = %d, want %d", result.OriginalBytes, len("ab€cd"))
	}
	if !result.Truncated {
		t.Error("Truncated not set")
	}
}
