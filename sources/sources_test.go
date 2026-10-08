package sources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const helperVariable = "SOURCES_TEST_HELPER"

// TestMain doubles as the stdio server used by the environment-forwarding test.
// The plane launches this same binary as a child, and only reaches the helper
// branch if the variable that selects it was actually forwarded.
func TestMain(m *testing.M) {
	if os.Getenv(helperVariable) == "1" {
		runHelper()
		return
	}
	os.Exit(m.Run())
}

func runHelper() {
	server := mcp.NewServer(&mcp.Implementation{Name: "helper", Version: "0.0.1"}, nil)
	server.AddTool(readOnly("report_environment"), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report := map[string]string{}
		for _, name := range []string{"SOURCES_TEST_FORWARDED", "SOURCES_TEST_WITHHELD", "PATH"} {
			report[name] = os.Getenv(name)
		}
		body, err := json.Marshal(report)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}

func closeSet(t *testing.T, set *Set) {
	t.Helper()
	if err := set.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

func readOnly(name string) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Description: name + " description",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}
}

func writeCapable(name string) *mcp.Tool {
	tool := readOnly(name)
	tool.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: false}
	return tool
}

func unannotated(name string) *mcp.Tool {
	tool := readOnly(name)
	tool.Annotations = nil
	return tool
}

func newServer(tools []*mcp.Tool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	for _, tool := range tools {
		name := tool.Name
		server.AddTool(tool, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "served " + name}}}, nil
		})
	}
	return server
}

// The secret stands in for anything a real dial error might carry: a command
// line, a bearer token, an internal hostname.
const dialSecret = "gateway --token=hunter2 https://internal.example/mcp"

// memoryDialer serves each named server over the SDK's in-memory transport and
// fails, with a secret-bearing error, for any server id it does not know.
func memoryDialer(t *testing.T, servers map[string]*mcp.Server) dialer {
	t.Helper()
	return func(ctx context.Context, config Server, _ settings) (session, error) {
		server, ok := servers[config.ID]
		if !ok {
			return nil, errors.New("dial failed: " + dialSecret)
		}
		clientSide, serverSide := mcp.NewInMemoryTransports()
		serverSession, err := server.Connect(ctx, serverSide, nil)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = serverSession.Close() })
		client := mcp.NewClient(&mcp.Implementation{Name: ReservedID, Version: "test"}, nil)
		return client.Connect(ctx, clientSide, nil)
	}
}

func TestOpenVerifiesTheAllowlist(t *testing.T) {
	tests := []struct {
		name       string
		tools      []*mcp.Tool
		config     Server
		wantTools  []string
		wantTrust  TrustBasis
		wantReason string
	}{
		{
			name:      "read only tool under annotations",
			tools:     []*mcp.Tool{readOnly("operations_overview")},
			config:    Server{ID: "operations", Command: "operations", Allow: []string{"operations_overview"}},
			wantTools: []string{"sources__operations__operations_overview"},
			wantTrust: TrustAnnotations,
		},
		{
			name:      "allowlist is exact, not a prefix",
			tools:     []*mcp.Tool{readOnly("operations_overview"), readOnly("operations_secrets")},
			config:    Server{ID: "operations", Command: "operations", Allow: []string{"operations_overview"}},
			wantTools: []string{"sources__operations__operations_overview"},
			wantTrust: TrustAnnotations,
		},
		{
			name:       "allowlisted tool the server does not offer",
			tools:      []*mcp.Tool{readOnly("operations_overview")},
			config:     Server{ID: "operations", Command: "operations", Allow: []string{"operations_ghost"}},
			wantReason: `does not expose the allowlisted tool "operations_ghost"`,
		},
		{
			name:       "tool that declares it can write",
			tools:      []*mcp.Tool{writeCapable("operations_apply")},
			config:     Server{ID: "operations", Command: "operations", Allow: []string{"operations_apply"}},
			wantReason: `"operations_apply" does not declare that it only reads`,
		},
		{
			name:       "tool with no annotations at all",
			tools:      []*mcp.Tool{unannotated("operations_overview")},
			config:     Server{ID: "operations", Command: "operations", Allow: []string{"operations_overview"}},
			wantReason: `"operations_overview" does not declare that it only reads`,
		},
		{
			name:  "unannotated tool accepted under grants",
			tools: []*mcp.Tool{unannotated("github_list_repos")},
			config: Server{
				ID: "gateway", Command: "gateway",
				Allow: []string{"github_list_repos"}, Trust: TrustGrants,
			},
			wantTools: []string{"sources__gateway__github_list_repos"},
			wantTrust: TrustGrants,
		},
		{
			name:  "annotated tool still records grants as the basis",
			tools: []*mcp.Tool{readOnly("github_list_repos")},
			config: Server{
				ID: "gateway", Command: "gateway",
				Allow: []string{"github_list_repos"}, Trust: TrustGrants,
			},
			wantTools: []string{"sources__gateway__github_list_repos"},
			wantTrust: TrustGrants,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dial := memoryDialer(t, map[string]*mcp.Server{test.config.ID: newServer(test.tools)})
			set, err := open(t.Context(), Config{Servers: []Server{test.config}}, dial, settings{})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer closeSet(t, set)

			var names []string
			for _, tool := range set.Tools() {
				names = append(names, tool.Name)
				if tool.Trust != test.wantTrust {
					t.Errorf("tool %s trust = %q, want %q", tool.Name, tool.Trust, test.wantTrust)
				}
				if tool.ServerID != test.config.ID {
					t.Errorf("tool %s server = %q, want %q", tool.Name, tool.ServerID, test.config.ID)
				}
			}
			if !slices.Equal(names, test.wantTools) {
				t.Errorf("tools = %v, want %v", names, test.wantTools)
			}

			spots := set.BlindSpots()
			if test.wantReason == "" {
				if len(spots) != 0 {
					t.Fatalf("blind spots = %+v, want none", spots)
				}
				return
			}
			if len(spots) != 1 {
				t.Fatalf("blind spots = %+v, want exactly one", spots)
			}
			if spots[0].ServerID != test.config.ID {
				t.Errorf("blind spot server = %q, want %q", spots[0].ServerID, test.config.ID)
			}
			if !strings.Contains(spots[0].Reason, test.wantReason) {
				t.Errorf("blind spot reason = %q, want it to contain %q", spots[0].Reason, test.wantReason)
			}
			if spots[0].Err == nil {
				t.Error("blind spot lost the underlying error")
			}
		})
	}
}

func TestOpenRecordsToolProvenance(t *testing.T) {
	config := Server{ID: "operations", Command: "operations", Allow: []string{"operations_overview"}}
	dial := memoryDialer(t, map[string]*mcp.Server{"operations": newServer([]*mcp.Tool{readOnly("operations_overview")})})
	set, err := open(t.Context(), Config{Servers: []Server{config}}, dial, settings{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer closeSet(t, set)

	tool, ok := set.Lookup("sources__operations__operations_overview")
	if !ok {
		t.Fatal("Lookup missed the healthy tool")
	}
	if tool.ToolName != "operations_overview" {
		t.Errorf("ToolName = %q, want %q", tool.ToolName, "operations_overview")
	}
	if tool.Description != "operations_overview description" {
		t.Errorf("Description = %q", tool.Description)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatalf("Schema is not JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("Schema type = %v, want object", schema["type"])
	}

	result, err := set.Call(t.Context(), tool.Name, map[string]any{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.Text != "served operations_overview" {
		t.Errorf("Call text = %q", result.Text)
	}
	if result.Omitted() {
		t.Errorf("Call reported omissions on a plain text result: %+v", result)
	}
	if _, err := set.Call(t.Context(), "sources__operations__nope", nil); err == nil {
		t.Error("Call on an unknown tool should fail")
	}
}

func TestOneBadServerLeavesTheOthersHealthy(t *testing.T) {
	config := Config{Servers: []Server{
		{ID: "unreachable", Command: "missing", Allow: []string{"anything"}},
		{ID: "operations", Command: "operations", Allow: []string{"operations_overview"}},
		{ID: "broken", Command: "broken", Allow: []string{"broken_apply"}},
		{ID: "gateway", Transport: TransportHTTP, URL: "https://gateway.example/mcp", Allow: []string{"nope"}},
	}}
	dial := memoryDialer(t, map[string]*mcp.Server{
		"operations": newServer([]*mcp.Tool{readOnly("operations_overview")}),
		"broken":     newServer([]*mcp.Tool{writeCapable("broken_apply")}),
	})

	set, err := open(t.Context(), config, dial, settings{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer closeSet(t, set)

	var names []string
	for _, tool := range set.Tools() {
		names = append(names, tool.Name)
	}
	if want := []string{"sources__operations__operations_overview"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}

	spots := set.BlindSpots()
	if len(spots) != 3 {
		t.Fatalf("blind spots = %+v, want three", spots)
	}
	wantReasons := map[string]string{
		"unreachable": ReasonStdioUnreachable,
		"broken":      `"broken_apply" does not declare that it only reads`,
		"gateway":     ReasonHTTPUnreachable,
	}
	for _, spot := range spots {
		want, ok := wantReasons[spot.ServerID]
		if !ok {
			t.Fatalf("unexpected blind spot for %q", spot.ServerID)
		}
		if !strings.Contains(spot.Reason, want) {
			t.Errorf("%s reason = %q, want it to contain %q", spot.ServerID, spot.Reason, want)
		}
	}
}

func TestBlindSpotReasonKeepsTheUnderlyingErrorOut(t *testing.T) {
	config := Config{Servers: []Server{
		{ID: "stdio", Command: "missing", Allow: []string{"t"}},
		{ID: "remote", Transport: TransportHTTP, URL: "https://remote.example/mcp", Allow: []string{"t"}},
	}}
	set, err := open(t.Context(), config, memoryDialer(t, nil), settings{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer closeSet(t, set)

	spots := set.BlindSpots()
	if len(spots) != 2 {
		t.Fatalf("blind spots = %+v, want two", spots)
	}
	wantReasons := []string{ReasonStdioUnreachable, ReasonHTTPUnreachable}
	for i, spot := range spots {
		if spot.Reason != wantReasons[i] {
			t.Errorf("%s reason = %q, want %q", spot.ServerID, spot.Reason, wantReasons[i])
		}
		if strings.Contains(spot.Reason, "hunter2") || strings.Contains(spot.Reason, dialSecret) {
			t.Errorf("%s reason leaked the underlying error: %q", spot.ServerID, spot.Reason)
		}
		if spot.Err == nil || !strings.Contains(spot.Err.Error(), dialSecret) {
			t.Errorf("%s lost the underlying error, got %v", spot.ServerID, spot.Err)
		}
	}
}

// recordingSession stands in for a client session so close order is observable.
type recordingSession struct {
	id     string
	tools  []*mcp.Tool
	closed *[]string
}

func (r *recordingSession) ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: r.tools}, nil
}

func (r *recordingSession) CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{}, nil
}

func (r *recordingSession) Close() error {
	*r.closed = append(*r.closed, r.id)
	return errors.New("close failed for " + r.id)
}

func TestCloseRunsInReverseOrderAndJoinsErrors(t *testing.T) {
	var closed []string
	config := Config{Servers: []Server{
		{ID: "first", Command: "first", Allow: []string{"t"}},
		{ID: "second", Command: "second", Allow: []string{"t"}},
		{ID: "third", Command: "third", Allow: []string{"t"}},
	}}
	dial := func(_ context.Context, server Server, _ settings) (session, error) {
		return &recordingSession{id: server.ID, tools: []*mcp.Tool{readOnly("t")}, closed: &closed}, nil
	}

	set, err := open(t.Context(), config, dial, settings{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	err = set.Close()
	if want := []string{"third", "second", "first"}; !slices.Equal(closed, want) {
		t.Fatalf("close order = %v, want %v", closed, want)
	}
	for _, id := range []string{"first", "second", "third"} {
		if err == nil || !strings.Contains(err.Error(), "close failed for "+id) {
			t.Errorf("Close error = %v, want it to join the failure for %s", err, id)
		}
	}
	if second := set.Close(); second != nil {
		t.Errorf("second Close = %v, want nil", second)
	}
}

func TestBadConfigFailsOpenOutright(t *testing.T) {
	config := Config{Servers: []Server{{ID: "operations", Command: "ops", URL: "https://x.example", Allow: []string{"t"}}}}
	dialed := false
	dial := func(context.Context, Server, settings) (session, error) {
		dialed = true
		return nil, errors.New("should not happen")
	}
	if _, err := open(t.Context(), config, dial, settings{}); err == nil {
		t.Fatal("open accepted a server that mixes transports")
	}
	if dialed {
		t.Error("open dialed a server before validating the config")
	}
}

func TestStdioForwardsOnlyTheNamedEnvironment(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}
	t.Setenv(helperVariable, "1")
	t.Setenv("SOURCES_TEST_FORWARDED", "visible")
	t.Setenv("SOURCES_TEST_WITHHELD", "must-not-leak")

	set, err := OpenConfig(t.Context(), Config{Servers: []Server{{
		ID:      "helper",
		Command: executable,
		Env:     []string{helperVariable, "SOURCES_TEST_FORWARDED"},
		Allow:   []string{"report_environment"},
	}}})
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	defer closeSet(t, set)
	if spots := set.BlindSpots(); len(spots) != 0 {
		t.Fatalf("blind spots = %+v (err %v), want none", spots, spots[0].Err)
	}

	result, err := set.Call(t.Context(), "sources__helper__report_environment", map[string]any{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var report map[string]string
	if err := json.Unmarshal([]byte(result.Text), &report); err != nil {
		t.Fatalf("helper returned %q: %v", result.Text, err)
	}
	if report["SOURCES_TEST_FORWARDED"] != "visible" {
		t.Errorf("forwarded variable = %q, want %q", report["SOURCES_TEST_FORWARDED"], "visible")
	}
	if report["SOURCES_TEST_WITHHELD"] != "" {
		t.Errorf("withheld variable leaked as %q", report["SOURCES_TEST_WITHHELD"])
	}
	if report["PATH"] == "" {
		t.Error("PATH was not forwarded")
	}
}

func TestHTTPSendsHeadersResolvedFromTheEnvironment(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []string
	)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return newServer([]*mcp.Tool{readOnly("github_list_repos")}) },
		nil,
	)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	defer endpoint.Close()

	t.Setenv("SOURCES_TEST_TOKEN", "Bearer hunter2")
	set, err := OpenConfig(t.Context(), Config{Servers: []Server{{
		ID:        "gateway",
		Transport: TransportHTTP,
		URL:       endpoint.URL,
		Headers:   map[string]string{"Authorization": "SOURCES_TEST_TOKEN"},
		Allow:     []string{"github_list_repos"},
		Trust:     TrustGrants,
	}}})
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	defer closeSet(t, set)
	if spots := set.BlindSpots(); len(spots) != 0 {
		t.Fatalf("blind spots = %+v (err %v), want none", spots, spots[0].Err)
	}

	if _, err := set.Call(t.Context(), "sources__gateway__github_list_repos", map[string]any{}); err != nil {
		t.Fatalf("Call: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("the endpoint saw no requests")
	}
	for i, value := range seen {
		if value != "Bearer hunter2" {
			t.Fatalf("request %d carried Authorization %q, want the resolved value", i, value)
		}
	}
}

func TestHTTPWithAnUnsetHeaderVariableIsABlindSpot(t *testing.T) {
	t.Setenv("SOURCES_TEST_ABSENT_TOKEN", "")
	if err := os.Unsetenv("SOURCES_TEST_ABSENT_TOKEN"); err != nil {
		t.Fatal(err)
	}
	set, err := OpenConfig(t.Context(), Config{Servers: []Server{{
		ID:        "gateway",
		Transport: TransportHTTP,
		URL:       "https://gateway.example/mcp",
		Headers:   map[string]string{"Authorization": "SOURCES_TEST_ABSENT_TOKEN"},
		Allow:     []string{"github_list_repos"},
	}}})
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	defer closeSet(t, set)

	spots := set.BlindSpots()
	if len(spots) != 1 || spots[0].Reason != ReasonHTTPUnreachable {
		t.Fatalf("blind spots = %+v, want one HTTP unreachable", spots)
	}
	if len(set.Tools()) != 0 {
		t.Errorf("tools = %v, want none", set.Tools())
	}
}

func TestNilSetIsInert(t *testing.T) {
	var set *Set
	if set.Tools() != nil || set.BlindSpots() != nil || set.Close() != nil {
		t.Error("a nil Set should read as empty and close cleanly")
	}
	if _, ok := set.Lookup("anything"); ok {
		t.Error("a nil Set should find no tools")
	}
	if _, err := (Tool{Name: "unbound"}).Call(t.Context(), nil); err == nil {
		t.Error("an unbound Tool should refuse to be called")
	}
}

// bulkyEndpoint serves a real streamable HTTP MCP server whose one tool answers
// with size bytes, so the response limit can be exercised over the wire.
func bulkyEndpoint(t *testing.T, size int) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "bulky", Version: "0.0.1"}, nil)
	server.AddTool(readOnly("bulk_read"), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: strings.Repeat("x", size)},
		}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)
	return endpoint.URL
}

func bulkyConfig(url string) Config {
	return Config{Servers: []Server{{
		ID:        "bulky",
		Transport: TransportHTTP,
		URL:       url,
		Allow:     []string{"bulk_read"},
	}}}
}

func TestHTTPResponseLimitIsOffByDefault(t *testing.T) {
	set, err := OpenConfig(t.Context(), bulkyConfig(bulkyEndpoint(t, 1<<20)))
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	defer closeSet(t, set)
	if spots := set.BlindSpots(); len(spots) != 0 {
		t.Fatalf("blind spots = %+v, want none", spots)
	}
	result, err := set.Call(t.Context(), "sources__bulky__bulk_read", map[string]any{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.OriginalBytes != 1<<20 {
		t.Errorf("OriginalBytes = %d, want %d", result.OriginalBytes, 1<<20)
	}
}

func TestHTTPResponseLimitAllowsAModestRead(t *testing.T) {
	set, err := OpenConfig(t.Context(), bulkyConfig(bulkyEndpoint(t, 256)), WithMaxHTTPResponseBytes(1<<20))
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	defer closeSet(t, set)
	if spots := set.BlindSpots(); len(spots) != 0 {
		t.Fatalf("blind spots = %+v (err %v), want none", spots, spots[0].Err)
	}
	result, err := set.Call(t.Context(), "sources__bulky__bulk_read", map[string]any{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.OriginalBytes != 256 {
		t.Errorf("OriginalBytes = %d, want 256", result.OriginalBytes)
	}
}

func TestHTTPResponseLimitFailsTheCallWithoutKillingTheSource(t *testing.T) {
	set, err := OpenConfig(t.Context(), bulkyConfig(bulkyEndpoint(t, 1<<20)), WithMaxHTTPResponseBytes(4096))
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	defer closeSet(t, set)
	// Opening succeeded, so the limit is generous enough for initialize and
	// tools/list and only the oversized read trips it.
	if spots := set.BlindSpots(); len(spots) != 0 {
		t.Fatalf("blind spots = %+v (err %v), want none", spots, spots[0].Err)
	}
	if _, err := set.Call(t.Context(), "sources__bulky__bulk_read", map[string]any{}); err == nil {
		t.Fatal("an oversized read succeeded")
	}
	if len(set.Tools()) != 1 {
		t.Errorf("tools = %d, want the source still healthy", len(set.Tools()))
	}
}

func TestHTTPResponseLimitBelowHandshakeBecomesABlindSpot(t *testing.T) {
	set, err := OpenConfig(t.Context(), bulkyConfig(bulkyEndpoint(t, 16)), WithMaxHTTPResponseBytes(8))
	if err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	defer closeSet(t, set)

	spots := set.BlindSpots()
	if len(spots) != 1 {
		t.Fatalf("blind spots = %+v, want one", spots)
	}
	if spots[0].Reason != ReasonHTTPUnreachable {
		t.Errorf("reason = %q, want %q", spots[0].Reason, ReasonHTTPUnreachable)
	}
	if len(set.Tools()) != 0 {
		t.Errorf("tools = %v, want none", set.Tools())
	}
}

func TestBoundedBodyAllowsExactlyTheLimit(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		limit   int
		wantErr bool
	}{
		{name: "under the limit", body: "abcd", limit: 8},
		{name: "exactly the limit", body: "abcdefgh", limit: 8},
		{name: "one byte over", body: "abcdefghi", limit: 8, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &boundedBody{
				ReadCloser: io.NopCloser(strings.NewReader(test.body)),
				remaining:  int64(test.limit) + 1,
				limit:      test.limit,
			}
			read, err := io.ReadAll(body)
			if test.wantErr {
				if err == nil {
					t.Fatalf("read %q with no error, want the limit to trip", read)
				}
				if strings.Contains(err.Error(), test.body) {
					t.Error("the limit error carried the response body")
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(read) != test.body {
				t.Errorf("read %q, want %q", read, test.body)
			}
		})
	}
}
