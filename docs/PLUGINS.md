# Community Plugins

Reasonix loads external tools as **MCP servers**. The normal discovery path is
the official MCP Registry: use **Settings → MCP servers → Browse registry** or
`reasonix mcp browse [query]`. See the [Guide](./GUIDE.md#plugins-mcp) for the
installation and runtime model.

This page complements the registry with community servers that include
Reasonix-specific setup guidance. Listings are links, not bundled dependencies,
endorsements, or quality guarantees.

> Installing or declaring an MCP server is a trust decision. A local stdio
> server runs as a subprocess, and its `readOnlyHint` / `destructiveHint` values
> are workflow metadata rather than containment against malicious code. Review
> the public source and published package before adding one.

## How to add a plugin

Each `[[plugins]]` entry names a server and how to launch it. `type` defaults to
`stdio`; `${VAR}` / `${VAR:-default}` are expanded in `command`, `args`, and
`env`. Pin package versions so a reviewed configuration does not silently start
running a different release. `-y` prevents `npx`'s first-run installation prompt
from blocking the MCP stdio handshake.

```toml
[[plugins]]
name    = "example"
command = "npx"
args    = ["-y", "some-reasonix-plugin@1.2.3"]
# env   = { SOME_TOKEN = "${SOME_TOKEN}" }
```

After launch, tools appear in-session as `mcp__<name>__<tool>`. Run `/mcp` in
the chat TUI to inspect connected servers and their tool counts.

## Community plugins

No entries are listed yet. A listing must have a reachable public source
repository and a published, versioned release so users can inspect what the
documented command executes.

## Publishing your own

1. Build an MCP server (any language) that speaks stdio JSON-RPC; see
   `cmd/reasonix-plugin-example` for a runnable reference.
2. Publish its source, license, tests, and versioned installation artifact.
3. Declare its tools with honest annotations, including `readOnlyHint` only for
   tools whose behavior is actually read-only.
4. Document a complete, copyable `[[plugins]]` entry with a pinned version.
5. Open a PR adding the entry to both this page and `PLUGINS.zh-CN.md`, keeping
   entries alphabetical by plugin name.
