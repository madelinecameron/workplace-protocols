# Workplace Protocols

Go packages for tools and agents that run across a [Tailscale](https://tailscale.com)
network. They give every tool the same answer to a few questions: which
machines exist, what a repository or agent is called, which tools are
available and how to reach them, and what shape shared records take.

Each answer lives in one package so that separate tools compute the same ids
and read the same config, instead of each keeping its own copy that drifts.

```sh
go get github.com/madelinecameron/workplace-protocols
```

Requires Go 1.26. Dependencies: `gopkg.in/yaml.v3`, the
[MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) (used by `sources`)
and the [MCP Registry](https://github.com/modelcontextprotocol/registry)
`server.json` types (used by `tools`).

## Packages

| Package | What it does |
|---|---|
| `tailnet` | Parses `tailscale status --json` into nodes with hostnames, MagicDNS names, tags and online state. `Lookup` finds a node by name; `ByTag` finds nodes by ACL tag. |
| `discovery` | Reads `workplace-protocols.yaml`, a manifest of tailnet-wide facts. `Resolve` finds the node carrying the closet tag (default `tag:closet`) and reads the manifest from it over SSH. |
| `repoid` | Stable ids for git repositories. `RepoID` hashes the canonical remote URL, so ssh, https and scp forms of one repository, cloned anywhere, get the same id. |
| `agentid` | Stable addresses for agents, humans and roles, written `kind:name` (`agent:scout`, `human:alice`, `role:owner/acme`). Parsing normalizes case and stray slashes. `SessionID` names one run of an agent. |
| `tools` | Reads the `toolchain` block of the manifest: which tools exist, at which version, and their MCP servers as standard `server.json`. `Resolve` matches it to the live tailnet; `Config` renders an MCP client configuration. |
| `sources` | A client for reading from allowlisted, read-only MCP servers. A config file names every server, every tool it may expose, and the environment variables that hold credentials. A server that fails the contract is reported as a blind spot and the rest keep working. |
| `protocol` | Correlation between systems, as [CloudEvents correlation](https://github.com/cloudevents/spec/blob/main/cloudevents/extensions/correlation.md) attributes (`correlationid`, `causationid`) plus `sessionid`, with a JSON Schema and validation. See [docs/protocol.md](docs/protocol.md). |

## The manifest

[`examples/workplace-protocols.yaml`](examples/workplace-protocols.yaml) is a
complete, commented manifest. Its tests parse the shipped file, so the example
stays in sync with the parser.

`discovery.Resolve` reads the manifest from
`~/.workplace-protocols/workplace-protocols.yaml` on the node carrying the
closet tag. Pass a different path to `discovery.SSHReader` to change that.

### Declaring an MCP endpoint

Give a tool a `server:` block. It is an
[MCP Registry `server.json`](https://github.com/modelcontextprotocol/registry/tree/main/docs/reference/server-json)
(schema `2025-12-11`) written in YAML, so the same description works with
registry tooling.

```yaml
toolchain:
  tools:
    - name: tracker                # local: a stdio subprocess on the calling machine
      version: v0.2.6
      server:
        name: io.github.example/tracker
        description: Workstation and repository state
        version: 0.2.6             # must match the tool version
        packages:
          - registryType: fleet    # a binary the fleet installs onto PATH
            identifier: tracker    # the command to run
            transport: {type: stdio}
            packageArguments:
              - {type: positional, value: mcp}

    - name: mailbox                # remote: a daemon on a node carrying the tag
      version: v0.1.4
      where: [tag:server]
      server:
        name: io.github.example/mailbox
        description: Threaded mail between agents
        version: 0.1.4
        remotes:
          - type: streamable-http
            url: http://{host}:8788/mcp   # {host} is the serving node
```

A tool without a `server:` block stays in the catalog with its pinned version;
agents just don't call it. Unknown fields on a tool entry are rejected, so a typo
or a leftover key fails loudly instead of being ignored.

`Resolve` supports the part of `server.json` it can turn into an endpoint, and
`Validate` rejects the rest instead of silently ignoring it:

- **Packages** must use `registryType: fleet` and the `stdio` transport. No
  standard registry type covers a binary your own fleet installs, so public
  registries will not accept these entries. Environment variables and runtime
  arguments are not supported.
- **Remotes** must use `streamable-http`, include `{host}` in the URL, and have
  a `where` tag. Other URL variables need a `value` or `default`.
- A `where` tag on a **package** means it only resolves on nodes carrying that
  tag. Leave `where` off a package every machine should run.
- If no online node carries a remote's tag, the tool is reported as unreachable
  instead of being dropped, so a dead daemon looks different from an
  undeclared one.

Declaring an endpoint grants no authority. It says where a server is, not what
a caller may do with it. A consumer that needs a safety property must enforce
it itself, the way `sources` refuses any tool that does not declare
`readOnlyHint=true`.

## Example

```go
st, err := tailnet.Fetch()
if err != nil {
	log.Fatal(err)
}
manifest, err := discovery.Resolve(st, "", discovery.SSHReader(""))
if err != nil {
	log.Fatal(err)
}
fmt.Println(manifest.Tailnet)
```

## Development

```sh
go test ./...
go vet ./...
golangci-lint run
```

## License

[MIT](LICENSE)
