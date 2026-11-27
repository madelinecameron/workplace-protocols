package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

const placementSample = `
mcp:
  - name: everywhere
    server:
      name: com.example/everywhere
      description: Runs on every machine
      version: 1.0.0
      packages:
        - {registryType: fleet, identifier: everywhere, transport: {type: stdio}}
  - name: desk
    roles: [laptop, workstation]
    env: {dsn: DESK_DSN}
    server:
      name: com.example/desk
      description: Only at a desk
      version: 1.0.0
      packages:
        - registryType: fleet
          identifier: desk
          transport: {type: stdio}
          environmentVariables:
            - {name: DATABASE_URI, value: "{dsn}", isSecret: true}
  - name: parked
    enabled: false
    server:
      name: com.example/parked
      description: Kept but not given out
      version: 1.0.0
      remotes: [{type: streamable-http, url: "https://parked.example.com/mcp"}]
  - name: design
    projects: [work/acme/app, work/acme/site]
    env: {token: DESIGN_TOKEN}
    server:
      name: com.example/design
      description: Design assets for one product
      version: 1.0.0
      remotes:
        - type: streamable-http
          url: https://design.example.com/mcp
          headers: [{name: Authorization, value: "Bearer {token}", isSecret: true}]
`

func placementEndpoints(t *testing.T, role string) map[string]Endpoint {
	t.Helper()
	c := parse(t, placementSample)
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	eps, err := ResolveFor(c, nil, Machine{OS: "linux", Role: role})
	if err != nil {
		t.Fatalf("ResolveFor: %v", err)
	}
	byName := map[string]Endpoint{}
	for _, e := range eps {
		byName[e.Name] = e
	}
	return byName
}

func TestRolesLimitWhichMachinesGetAServer(t *testing.T) {
	if _, ok := placementEndpoints(t, "server")["desk"]; ok {
		t.Fatal("a desk-only server resolved for a server")
	}
	if _, ok := placementEndpoints(t, "")["desk"]; ok {
		t.Fatal("a server limited to roles resolved with no role given")
	}
	if _, ok := placementEndpoints(t, "laptop")["desk"]; !ok {
		t.Fatal("a desk-only server did not resolve on a laptop")
	}
}

func TestDisabledEntriesResolveNowhere(t *testing.T) {
	if _, ok := placementEndpoints(t, "laptop")["parked"]; ok {
		t.Fatal("a disabled entry resolved")
	}
}

func TestProjectServersRenderPerProject(t *testing.T) {
	eps := placementEndpoints(t, "laptop")
	all := []Endpoint{eps["everywhere"], eps["design"]}
	if _, ok := Config(all)["design"]; ok {
		t.Fatal("a project server is in the global config")
	}
	projects := ProjectConfigs(all)
	if len(projects) != 2 || projects["work/acme/site"]["design"].URL != "https://design.example.com/mcp" {
		t.Fatalf("projects = %+v", projects)
	}
	out, err := Codex(all)
	if err != nil || strings.Contains(string(out), "design") {
		t.Fatalf("codex = %q, %v; want project servers skipped", out, err)
	}
}

func TestFillReplacesKnownReferences(t *testing.T) {
	eps := placementEndpoints(t, "laptop")
	if got := strings.Join(eps["design"].Refs(), ","); got != "DESIGN_TOKEN" {
		t.Fatalf("refs = %s", got)
	}
	filled := Fill([]Endpoint{eps["design"], eps["desk"]}, map[string]string{"DESIGN_TOKEN": "t0k"})
	if refs := filled[0].Refs(); len(refs) != 0 {
		t.Fatalf("design still reads %v", refs)
	}
	if got := strings.Join(filled[1].Refs(), ","); got != "DESK_DSN" {
		t.Fatalf("desk refs = %s, want the unfilled variable kept", got)
	}
	data, err := json.Marshal(ProjectConfigs(filled)["work/acme/app"]["design"].Headers)
	if err != nil || string(data) != `{"Authorization":"Bearer t0k"}` {
		t.Fatalf("headers = %s, %v", data, err)
	}
}

func TestScopeValidation(t *testing.T) {
	for _, bad := range []string{"roles: [\"two words\"]", "projects: [/abs]", "projects: [../out]", "projects: [a/../b]"} {
		doc := strings.Replace(placementSample, "  - name: everywhere\n", "  - name: everywhere\n    "+bad+"\n", 1)
		if err := parse(t, doc).Validate(); err == nil {
			t.Errorf("%s: expected a validation error", bad)
		}
	}
	doc := "toolchain:\n  tools:\n    - {name: cli, version: v1, roles: [laptop]}\n"
	if err := parse(t, doc).Validate(); err == nil || !strings.Contains(err.Error(), "need a server") {
		t.Fatalf("error = %v, want roles without a server rejected", err)
	}
}
