# Workplace Protocols

Go packages for running your own tools and AI agents across machines on a
[Tailscale](https://tailscale.com) network. They give every tool the same
answer to a few questions, so each tool doesn't keep its own copy that drifts:

- Which MCP servers exist, and how does an agent on this machine reach them?
- What is this repository called, across every clone on every machine?
- What is this agent called, so messages and logs about it line up?
- How do records in separate systems point at each other?

```sh
go get github.com/madelinecameron/workplace-protocols
```

Requires Go 1.26.

## Packages

| Package | What it does |
|---|---|
| [`discovery`](#set-up-the-manifest) | Finds the manifest, one YAML file on one tagged node, and reads it over SSH. |
| [`tools`](#generate-mcp-client-config) | Reads the `toolchain` section of the manifest and turns it into `mcpServers` JSON for an MCP client. |
| [`sources`](#read-from-allowlisted-mcp-servers) | An MCP client that only calls tools you allowlisted, and only if the server marks them read-only. |
| [`agentid`](#name-agents) | Canonical addresses for agents, people and roles: `agent:scout`, `human:alice`, `role:owner/acme`. |
| [`repoid`](#name-repositories) | One id per repository, whether it was cloned over SSH or HTTPS, and wherever it lives. |
| [`protocol`](docs/protocol.md) | Correlation ids for linking records across systems, as CloudEvents attributes. |
| `tailnet` | Parses `tailscale status --json` into nodes with names, tags and online state. The other packages use it. |

You can use `agentid`, `repoid`, `protocol` and `sources` on their own. Only
`discovery`, `tools` and `tailnet` need Tailscale.

## Set up the manifest

The manifest is one YAML file that every machine reads. You keep it on one
node and mark that node with a Tailscale ACL tag, so there is no service to
run.

1. Pick an always-on node and give it the tag `tag:workplace` in your
   [tailnet policy](https://tailscale.com/kb/1068/tags). Any tag works if you
   pass it to `discovery.Resolve`.
2. Copy [`examples/workplace-protocols.yaml`](examples/workplace-protocols.yaml)
   to `~/.workplace-protocols/workplace-protocols.yaml` on that node and edit it.
   Only `tailnet` is required.
3. Make sure every machine that reads it can run `ssh <node> cat <file>`
   without a prompt. [Tailscale SSH](https://tailscale.com/kb/1193/tailscale-ssh)
   is the simplest way.

Then any program can read it:

```go
st, err := tailnet.Fetch()
if err != nil {
	log.Fatal(err)
}
manifest, err := discovery.Resolve(st, "", discovery.SSHReader(""))
if err != nil {
	log.Fatal(err)
}
fmt.Println(manifest.Tailnet, "read from", manifest.Host)
```

To keep the file somewhere else, pass your own `discovery.Reader`. It takes a
host name and returns the file's bytes.

Your own tools can add top-level sections to the manifest. Parse them from
`manifest.Raw`; `discovery` and `tools` ignore sections they don't know.

## Generate MCP client config

The `toolchain` section lists your tools. Give a tool a `server:` block and
agents can find its MCP server. The block is a standard
[MCP Registry `server.json`](https://github.com/modelcontextprotocol/registry/tree/main/docs/reference/server-json)
(schema `2025-12-11`) written in YAML.

```yaml
toolchain:
  tools:
    - name: tracker                # runs on the calling machine over stdio
      version: v0.2.6
      server:
        name: io.github.example/tracker
        description: Workstation and repository state
        version: 0.2.6             # must match the tool version
        packages:
          - registryType: fleet    # a command you install yourself
            identifier: tracker    # the command, found on PATH
            transport: {type: stdio}
            packageArguments:
              - {type: positional, value: mcp}

    - name: mailbox                # runs on a node carrying tag:server
      version: v0.1.4
      where: [tag:server]
      server:
        name: io.github.example/mailbox
        description: Threaded mail between agents
        version: 0.1.4
        remotes:
          - type: streamable-http
            url: http://{host}:8788/mcp   # {host} becomes that node's name
```

`tools.Resolve` works out which servers this machine can reach, and
`tools.Config` renders them as the `mcpServers` map that Claude Code, Codex,
Cursor and other MCP clients accept:

```go
catalog, err := tools.ParseCatalog(manifest.Raw)
if err != nil {
	log.Fatal(err)
}
if err := catalog.Validate(); err != nil {
	log.Fatal(err)
}
endpoints, err := tools.Resolve(catalog, st)
if err != nil {
	log.Print(err) // some servers are unreachable; the rest are still returned
}
out, _ := json.MarshalIndent(map[string]any{"mcpServers": tools.Config(endpoints)}, "", "  ")
fmt.Println(string(out))
```

```json
{
  "mcpServers": {
    "mailbox": {"type": "http", "url": "http://server.example.ts.net:8788/mcp"},
    "tracker": {"command": "tracker", "args": ["mcp"]}
  }
}
```

Rules for `server:` blocks:

- **Packages** run a command on the calling machine. They must use
  `registryType: fleet` and the `stdio` transport. Environment variables and
  runtime arguments aren't supported. Public MCP registries don't accept the
  `fleet` type, because it only means "already installed here".
- **Remotes** must use `streamable-http`, have a `where` tag, and include
  `{host}` in the URL. Other URL variables need a `value` or `default`.
- A `where` tag on a package means it only resolves on nodes carrying that tag.
  Leave `where` off a package every machine should run.
- If no online node carries a remote's tag, `Resolve` returns the other
  endpoints and an error naming the unreachable tool.
- A tool without a `server:` block is a CLI or background job. It stays in the
  list so its version is recorded, and agents don't call it.
- Unknown fields on a tool are rejected, so a typo fails instead of being
  ignored.

`role`, `build`, `install_cmd` and `channel` are optional hints for whatever
installs your tools. This library stores them and acts on none of them.

Declaring a server grants no authority. It says where a server is, not what a
caller may do with it.

## Read from allowlisted MCP servers

`sources` lets an agent read from MCP servers that you approve tool by tool.
The config file names every server, every tool it may call, and the
environment variables that hold credentials, so the file itself has no secrets
and can be committed and reviewed.
[`examples/sources.json`](examples/sources.json) shows both transports:

```json
{
  "servers": [
    {
      "id": "deployer",
      "command": "deployer",
      "args": ["mcp"],
      "env": ["DEPLOYER_CONFIG"],
      "allow": ["deployer_overview", "deployer_service_status"]
    },
    {
      "id": "metrics",
      "transport": "http",
      "url": "https://metrics.example.ts.net/mcp",
      "headers": {"Authorization": "METRICS_MCP_AUTHORIZATION"},
      "allow": ["metrics_query"]
    }
  ]
}
```

```go
set, err := sources.Open(ctx, "sources.json")
if err != nil {
	log.Fatal(err)
}
defer set.Close()
for _, spot := range set.BlindSpots() {
	log.Printf("%s unavailable: %s", spot.ServerID, spot.Reason)
}
result, err := set.Call(ctx, "sources__deployer__deployer_overview", nil)
```

- Every allowed tool must exist on the server and declare `readOnlyHint: true`.
  Set `"trust": "grants"` on a server whose tool list is already limited by
  other means to skip the `readOnlyHint` check.
- A server that breaks these rules, or can't be reached, becomes a blind spot.
  The other servers keep working.
- A stdio server only gets `HOME`, `PATH`, `TMPDIR` and the variables in `env`.
  An HTTP header's value is read from the named environment variable.
- Tools are offered as `sources__<server>__<tool>`.

## Name agents

```go
addr, err := agentid.Parse(" Agent:Scout ")   // agent:scout
role := agentid.MustParse("role:owner/acme")
me, err := agentid.FromEnv()                  // reads WORKPLACE_PROTOCOLS_AGENT
```

Set `WORKPLACE_PROTOCOLS_AGENT` (for example `agent:scout`) in each agent's
environment, so every process learns its own name the same way. `addr.ID()` is
a fixed-width hash for database columns and metric labels, and
`agentid.SessionID(addr, host, nonce)` names one run of an agent.

## Name repositories

```go
id := repoid.RepoID("git@github.com:acme/api.git", "/home/alice/api")
// same id as repoid.RepoID("https://github.com/acme/api", "/srv/api")
```

`repoid.PathID(machine, path)` names one checkout on one machine instead.

## Development

```sh
go test ./...
go vet ./...
golangci-lint run
```

The tests parse the files in `examples/`, so the examples can't drift from the
code.

## License

[MIT](LICENSE)
