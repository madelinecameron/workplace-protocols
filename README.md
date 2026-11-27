# Workplace Protocols

Go packages and a small CLI for running your own tools and AI agents across
machines. They give every tool the same answer to a few questions, so each tool
doesn't keep its own copy that drifts:

- Which MCP servers do agents get, and how does each machine reach them?
- Where are the shared services my tools need: git remotes, databases, APIs?
- What is this repository called, across every clone on every machine?
- What is this agent called, so messages and logs about it line up?
- How do records in separate systems point at each other?

```sh
go get github.com/madelinecameron/workplace-protocols
go install github.com/madelinecameron/workplace-protocols/cmd/workplace-protocols@latest
```

Requires Go 1.26.

## Packages

| Package | What it does |
|---|---|
| [`discovery`](#set-up-the-manifest) | Loads the manifest, one YAML file every machine reads, and looks up services in it by name. |
| [`tools`](#declare-mcp-servers) | Reads the `toolchain` and `mcp` sections and renders MCP client config for Claude Code and Codex. |
| [`sources`](#read-from-allowlisted-mcp-servers) | An MCP client that only calls tools you allowlisted, and only if the server marks them read-only. |
| [`agentid`](#name-agents) | Canonical addresses for agents, people and roles: `agent:scout`, `human:alice`, `role:owner/acme`. |
| [`repoid`](#name-repositories) | One id per repository, whether it was cloned over SSH or HTTPS, and wherever it lives. |
| [`protocol`](docs/protocol.md) | Correlation ids for linking records across systems, as CloudEvents attributes. |
| `tailnet` | Parses `tailscale status --json` into nodes with names, tags and online state. The other packages use it. |

You can use `agentid`, `repoid`, `protocol` and `sources` on their own.
Tailscale is only needed for MCP servers found by tag, and for reading the
manifest over SSH.

## Set up the manifest

The manifest is one YAML file with three lists, each with its own job:

| Section | Answers |
|---|---|
| `toolchain` | What do I build and install, at which version, on which machines? |
| `mcp` | Which other MCP servers do agents get, and how do they connect? |
| `services` | Where are the other things my tools look up by name? |

1. Copy [`examples/workplace-protocols.yaml`](examples/workplace-protocols.yaml)
   to `~/.workplace-protocols/workplace-protocols.yaml` and edit it. Only
   `tailnet` is required.
2. Put the same file on every machine, with whatever you already use for
   dotfiles or configuration. To keep it somewhere else, set
   `WORKPLACE_PROTOCOLS_FILE` to its path.
3. Run `workplace-protocols validate`. It reports every problem at once.

The file holds no secrets, so it can be committed and reviewed. See
[Secrets](#secrets).

Programs read it with `discovery.Load`:

```go
manifest, err := discovery.Load("") // the path, else $WORKPLACE_PROTOCOLS_FILE, else ~/.workplace-protocols/
if err != nil {
	log.Fatal(err)
}
hub, ok := manifest.Service("hub")
```

To keep a single copy instead, put it on one tailnet node carrying the tag
`tag:workplace` and read it over SSH with
`discovery.Resolve(st, "", discovery.SSHReader(""))`.

Your own tools can add top-level sections to the manifest. Parse them from
`manifest.Raw`; `discovery` and `tools` ignore sections they don't know.

### Services

A service is something on your network that your programs find by name.
Agents don't connect to services.

```yaml
services:
  - name: hub
    url: ssh://git@server.example.ts.net/srv/hub.git
  - name: postgres
    url: postgres://server.example.ts.net:5432/app
    tags: [tag:server]
    parameters:
      sslmode: disable
```

`url` is required and may use any scheme. `parameters` holds anything else a
consumer needs that isn't part of the URL.

## Declare MCP servers

Every MCP server is a standard
[MCP Registry `server.json`](https://github.com/modelcontextprotocol/registry/tree/main/docs/reference/server-json)
(schema `2025-12-11`) written in YAML, under `server:`.

- A tool you build that has an MCP server declares it inline in `toolchain`,
  so its version is written once.
- Everything else agents should get goes in `mcp`: servers someone else
  deploys, public remote servers, and packages run with npx or uvx.

```yaml
toolchain:
  tools:
    - name: tracker                # a command you install, run over stdio
      version: v0.2.6
      server:
        name: io.github.example/tracker
        description: Workstation and repository state
        version: 0.2.6             # must match the tool version
        packages:
          - registryType: fleet    # already installed here, found on PATH
            identifier: tracker
            transport: {type: stdio}
            packageArguments:
              - {type: positional, value: mcp}

mcp:
  - name: mailbox                  # a remote at a fixed URL
    use_when: "Messages between agents."
    env: {token: MAILBOX_TOKEN}
    server:
      name: io.github.example/mailbox
      description: Threaded mail between agents
      version: 0.9.0
      remotes:
        - type: streamable-http
          url: https://mailbox.example.ts.net/mcp
          headers:
            - {name: Authorization, value: "Bearer {token}", isSecret: true}

  - name: playwright               # an npm package, run with npx
    server:
      name: com.microsoft/playwright-mcp
      description: Browser automation
      version: 0.0.41
      packages:
        - {registryType: npm, identifier: "@playwright/mcp", version: 0.0.41, transport: {type: stdio}}
```

Fields on the entry, next to `server:`:

| Field | Meaning |
|---|---|
| `use_when` | When agents should reach for the server. server.json's `description` is capped at 100 characters; this isn't. |
| `where` | On a remote: the tags of the nodes that serve it. `{host}` in the URL becomes the first online one. On a package: only machines carrying one of these tags run it. |
| `os` | Only these operating systems get the server: `darwin`, `linux`, `windows`. |
| `env` | Maps each `{variable}` to the environment variable holding its value. |
| `headers_helper` | A command that prints extra request headers for a remote. Only Claude Code gets it. |

A name may appear in `toolchain` or `mcp`, not both.

### Secrets

A `{name}` in a URL, header, argument or environment value is filled from, in
order:

1. the server.json variable of that name, if it has a `value` or `default`;
2. `{host}`, on a remote with a `where` tag;
3. the environment variable that the entry's `env:` maps it to.

Validation fails if a variable is left unbound, if an `env:` binding is
unused, or if a value marked `isSecret` is written into the file. The rendered
config refers to the environment variable by name, and the MCP client reads it
when it starts the server. Where those variables come from (a secret manager,
a `.env` file, your shell) is up to you.

### Render client config

```sh
workplace-protocols render claude > .mcp.json
workplace-protocols render codex     # paste into ~/.codex/config.toml
workplace-protocols servers          # what this machine gets, and how it reaches each
```

For the example above, `render claude` prints:

```json
{
  "mcpServers": {
    "mailbox": {
      "type": "http",
      "url": "https://mailbox.example.ts.net/mcp",
      "headers": {"Authorization": "Bearer ${MAILBOX_TOKEN}"}
    },
    "playwright": {"command": "npx", "args": ["-y", "@playwright/mcp@0.0.41"]},
    "tracker": {"command": "tracker", "args": ["mcp"]}
  }
}
```

and `render codex` prints:

```toml
[mcp_servers.mailbox]
url = "https://mailbox.example.ts.net/mcp"
bearer_token_env_var = "MAILBOX_TOKEN"

[mcp_servers.playwright]
command = "npx"
args = ["-y", "@playwright/mcp@0.0.41"]

[mcp_servers.tracker]
command = "tracker"
args = ["mcp"]
```

Codex can only read an environment variable as a whole value: a header of
`Bearer {token}` or exactly `{token}`, or a stdio variable passed through
under its own name. `render codex` leaves out any server that needs more,
names it, and exits non-zero.

The same from Go:

```go
catalog, err := tools.ParseCatalog(manifest.Raw)
if err != nil {
	log.Fatal(err)
}
if err := catalog.Validate(); err != nil {
	log.Fatal(err)
}
st, _ := tailnet.Fetch() // nil is fine: servers found by tag are then reported unreachable
endpoints, err := tools.Resolve(catalog, st)
if err != nil {
	log.Print(err) // some servers are unreachable; the rest are still returned
}
claude := map[string]any{"mcpServers": tools.Config(endpoints)}
codex, err := tools.Codex(endpoints)
```

### Rules for `server:` blocks

- Declare exactly one package or one remote.
- **Packages** use the `stdio` transport.
  - `fleet` runs `identifier` from PATH. It means "already installed here",
    so public registries don't accept it.
  - `npm` runs `npx -y <identifier>@<version>`, and `pypi` runs
    `uvx <identifier>@<version>`. `runtimeArguments` replace the `-y`.
  - `environmentVariables` and arguments may use `{variables}`.
- **Remotes** use `streamable-http` and an `http` or `https` URL. With a
  `where` tag the URL must contain `{host}`; without one it is fixed.
- If no online node carries a remote's tag, `Resolve` returns the other
  endpoints and an error naming the unreachable server.
- A tool without a `server:` block is a CLI or background job. It stays in the
  list so its version is recorded, and agents don't call it.
- Unknown fields on a tool, an `mcp` entry or a server block are rejected, so
  a typo fails instead of being ignored.

`role`, `build`, `install_cmd`, `repo`, `sha256` and `channel` are optional
hints for whatever installs your tools. This library stores them and acts on
none of them.

Declaring a server grants no authority. It says where a server is and how to
connect, not what a caller may do with it.

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
