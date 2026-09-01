# ACP editor integration

<a href="../README.md">README</a>
&nbsp;·&nbsp;
<a href="./ACP.zh-CN.md">简体中文</a>
&nbsp;·&nbsp;
<a href="./GUIDE.md">Guide</a>
&nbsp;·&nbsp;
<a href="https://agentclientprotocol.com/">ACP specification</a>

Reasonix implements Agent Client Protocol (ACP) v1 as an NDJSON JSON-RPC 2.0
agent over standard input and output. Editors and other ACP hosts launch the
process, open one or more workspace-scoped sessions, and receive streamed
messages, tool activity, plans, permission requests, and configuration updates.

Session status usage objects may include structured `costQuote` (original
currency, `originalTotals`, identity/official-table valuations,
`costComplete`, `displayComplete`, `displayStatus`, and `billingMode`) alongside legacy
`estimatedCost` / `currency` aliases that mirror the selected display valuation.
See [Billing](./BILLING.md).

## Start the agent

An ACP host should launch one of these commands:

```sh
reasonix acp
reasonix acp --model deepseek-pro
reasonix acp
```

`--model` selects the startup model when the client does not override it.
Ordinary requests always enter the executor. There is no automatic simple /
light / full task mode. Verification obligations come from real tool actions.

Standard output is reserved for ACP messages. Reasonix sends diagnostics to
standard error, so hosts must not merge the two streams. Run `reasonix setup`
beforehand when no provider is configured; the initialize response also
advertises a terminal authentication method that launches `reasonix setup`.

## Initialize and negotiate capabilities

Clients should call `initialize` before opening a session. Reasonix advertises
the following capability shape (irrelevant fields omitted):

```json
{
  "protocolVersion": 1,
  "agentCapabilities": {
    "loadSession": true,
    "sessionCapabilities": {
      "list": {},
      "resume": {},
      "close": {},
      "delete": {}
    },
    "promptCapabilities": {
      "image": false,
      "audio": false,
      "embeddedContext": true
    },
    "mcpCapabilities": {
      "http": true,
      "sse": false
    },
    "_meta": {
      "reasonix.io": {
        "sessionSteer": {
          "method": "_reasonix.io/session/steer"
        }
      }
    }
  }
}
```

When the client advertises `fs.readTextFile`, `fs.writeTextFile`, or
`terminal`, Reasonix routes eligible file operations through the editor's
unsaved buffers and eligible foreground commands through a client-owned
terminal. Every file tool takes part — reads, edits and writes alike — so an
edit applies to what the editor currently shows instead of to the last saved
copy on disk. A non-UTF-8 file is not eligible: the ACP file methods are
text-only, so it stays on the local encoding-preserving path and keeps its
original charset. Without those client capabilities, the normal workspace
tools run locally inside the Reasonix process.

## Opt-in MCP interaction extension

Hosts supporting MCP elicitation advertise this in `initialize.clientCapabilities`:

```json
{"_meta":{"reasonix.io":{"mcpInteraction":{"supported":true,"schemaVersion":1}}}}
```

Reasonix advertises the matching capability under
`agentCapabilities._meta.reasonix.io.mcpInteraction`, including the method
`_reasonix.io/mcp/request_interaction`. Negotiated sessions use the interactive
MCP host profile. Clients without this exact opt-in keep the core profile and
receive no new reverse requests. This applies to new, loaded and rebuilt sessions.

The reverse request contains `sessionId`, `promptId`, `turnId`, `server`, `mode`
and `message`, plus `requestedSchema` for a form or `url` and `elicitationId` for
URL mode. Respond with `{"action":"accept","content":{}}`,
`{"action":"decline"}` or `{"action":"cancel"}`. Validate the requested form;
URL flows require user interaction and must not send credentials as form content.
Hosts that cannot render a requested interaction should cancel it.

Replies belong to the originating controller and turn. Cancellation, malformed
replies and rejected URLs cancel the interaction; content is ignored unless the
action is `accept`. Reasonix persists the decision through the controller before
releasing its MCP waiter. This does not replace standard
`session/request_permission` or change tool permission policy. The negotiated
host profile affects MCP capability/cache identity; transcript schema is unchanged.

## Session lifecycle

Each ACP session owns an independent Reasonix controller, workspace root, model,
collaboration mode, approval mode, MCP set, and persisted transcript. State does
not leak between sessions.

| Method | Behavior |
| --- | --- |
| `session/new` | Opens a session for an absolute `cwd` and returns its configuration state. |
| `session/load` | Opens a persisted ACP session and replays its transcript as `session/update` notifications. |
| `session/resume` | Opens a persisted session without replaying the transcript. |
| `session/prompt` | Runs one turn and streams updates until it returns a stop reason. |
| `session/cancel` | Cancels the active turn; this is a notification. |
| `session/list` | Lists live and persisted ACP sessions, optionally filtered by absolute `cwd`. |
| `session/close` | Stops a live session and releases resources without deleting history. |
| `session/delete` | Stops the session and removes its persisted ACP history. |

`session/new`, `session/load`, and `session/resume` may include `mcpServers`.
Reasonix accepts stdio, Streamable HTTP, and legacy SSE servers. ACP's official `[{"name":"...","value":"..."}]`
shape is supported for stdio `env` and HTTP `headers`; the older object-map
shape remains accepted for compatibility.

## Session controls

Reasonix exposes independent controls instead of combining unrelated choices in
one mode selector:

| Control | Values | Wire surface |
| --- | --- | --- |
| Collaboration mode | `normal`, `plan`, `goal` | `modes` and `session/set_mode` |
| Model | Configured `provider/model` entries | `configOptions` with id `model` |
| Reasoning effort | Provider-supported levels or `auto` | `configOptions` with id `effort` |
| Tool approval | `ask`, `auto`, `yolo` | `configOptions` with id `tool_approval` |

Use `session/set_config_option` for model, effort, and tool approval.
Its parameters are `sessionId`, `configId` and `value`, where `configId` is the
`id` of the option as advertised in `configOptions`:

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "session/set_config_option",
  "params": {
    "sessionId": "session-id",
    "configId": "tool_approval",
    "value": "yolo"
  }
}
```

Note that the field is `configId`, not `optionId`. The result is the full
refreshed `configOptions` array. An unknown id returns `-32602 InvalidParams`.

Model and effort changes rebuild the session controller while preserving its
history and the other axes. Tool-approval changes update the gate in place
without rebuilding the controller.

Execution modes are gone. For one compatibility version, clients that still
send `session/set_config_option` with `configId` `agent_preset` or `work_mode`
(including legacy aliases `profile`, `runtime_profile`, `token_mode`) receive a
successful no-op: nothing switches, nothing rebuilds, and the result carries a
`deprecatedNotice` explaining the adaptive standard execution.

For older clients, `session/set_model` remains available. The legacy
`session/set_mode` values `default` and `auto` are also accepted as Normal + Ask
and Normal + Yolo respectively; new clients should use the independent
selectors above.

## Prompts, updates, and approvals

`session/prompt` accepts text blocks and embedded text resources. Images and
audio are not advertised. During a turn, Reasonix may send:

- agent message and thought chunks;
- pending and completed tool-call updates;
- complete plan updates derived from `todo_write`;
- available slash commands;
- current-mode and configuration-option updates; and
- `session/request_permission` requests for permission-gated tools and user
  questions.

Hosts should keep the `session/prompt` request open until Reasonix returns its
stop reason, while continuing to process requests and notifications in both
directions.

A prompt of `/clear` clears the current session in place (a fresh transcript,
same session id), matching the CLI's `/clear`. When the session's tool-approval
mode is Yolo the clear happens immediately; in Ask, Auto, and Plan modes
Reasonix first asks the client with a `session/request_permission` round-trip
whose `Clear the session` option confirms the clear and `Cancel` aborts it.
The command is advertised in `available_commands_update` as `clear`.

Reasonix emits only ACP v1 stop reasons. A completed turn that still needs a
final-readiness check sends a `[warning]` message chunk and returns `end_turn`;
its vendor status remains `readiness_paused` so the host can offer recovery.
An explicit model-round limit (`max_steps`) sends a `[warning]`, returns
`max_turn_requests`, and records a paused vendor outcome. A host task-time,
token, or cost budget also sends a `[warning]` and records a paused outcome,
but returns `end_turn` because ACP v1 has no task-budget-specific stop reason.
The completion validator has been removed. A clean model stop without tool
calls returns `end_turn`; a response with tool calls continues through the
agent loop, and a truly empty response is retried at the frozen-request
boundary. Legacy `completion_validation`, `completion_evaluator_model`, and
`REASONIX_COMPLETION_VALIDATION_MODE` settings remain readable but are ignored
and are no longer emitted. Host-owned readiness, budget, tool-safety, and
recovery boundaries remain active.
Client cancellation returns `cancelled`, even when the interrupted runner exits
without an error. Other provider, tool, or runtime failures return a JSON-RPC
`-32603 InternalError` whose message contains a bounded, credential-redacted
cause; they do not return a successful prompt result with a non-standard
`stopReason`.

When the status phase is `readiness_paused`, resume that exact check with a
`session/prompt` request whose optional `action` is
`"final_readiness_recovery"`. Sending `/continue-checks` as the sole text block
is the compatibility form. Both forms consume a one-shot, persisted host
checkpoint; ordinary prompt text never inherits it, and a stale action after a
newer user turn is rejected as JSON-RPC `-32600 InvalidRequest` without
publishing or persisting a synthetic status turn.

## Mid-turn steering extension

Reasonix exposes mid-turn guidance as an ACP v1 vendor extension. It is not a
core ACP method, and it is not the still-unreleased ACP v2 `session/inject`
proposal.

### Discover support

Read the method name from:

```text
agentCapabilities._meta["reasonix.io"].sessionSteer.method
```

Do not assume the extension exists, and do not call the unnamespaced
`session/steer` name. ACP reserves non-underscore method names for the core
protocol.

### Send guidance

Call the advertised method while `session/prompt` is active:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "_reasonix.io/session/steer",
  "params": {
    "sessionId": "session-id",
    "prompt": [
      {"type": "text", "text": "use email instead of username"}
    ]
  }
}
```

A persistent session returns an item id and disposition:

```json
{"itemId":"inbox-item-id","disposition":"steer_accepted"}
```

Reasonix durably commits the guidance before returning. `steer_accepted` means
the active turn accepted it; `queued_followup` means that admission lost a race
or no turn was active, so the same item remains queued for a later turn. A
pathless compatibility session may omit `itemId` and still returns
`steer_accepted`. Applied guidance is persisted in normal history; transcript
replay shows the original user text, not Reasonix's internal steer marker.

| Condition | JSON-RPC result |
| --- | --- |
| Active prompt accepted durable guidance | `{"itemId":"...","disposition":"steer_accepted"}` |
| Guidance persisted but active admission was rejected | `{"itemId":"...","disposition":"queued_followup"}` |
| Unknown session or empty prompt | `-32602 InvalidParams` |
| Pathless compatibility session has no active prompt | `-32600 InvalidRequest` |
| Client calls `session/steer` | `-32601 MethodNotFound` |

On `InvalidRequest`, the compatibility session did not queue the guidance.

## Durable session inbox extension

Discover the versioned queue at
`agentCapabilities._meta["reasonix.io"].sessionInbox`. Schema version 1
advertises method names in its `methods` map; clients must use those advertised
names rather than constructing vendor method strings.

| Key | Purpose | Main parameters |
| --- | --- | --- |
| `enqueue` | Persist a follow-up or steer | `sessionId`, `text`, optional `intent`, `idempotencyKey` |
| `list` | Read metadata, capacity, pause and recovery state | `sessionId` |
| `get` | Read one full envelope on demand | `sessionId`, `itemId` |
| `update` / `delete` | Edit or delete pending work | `sessionId`, `itemId` |
| `move` | Reorder pending work | `sessionId`, `itemId`, zero-based `toIndex` |
| `setPaused` | Pause or resume dispatch | `sessionId`, `paused` |
| `retry` / `refresh` | Retry uncertain work or re-freeze references | `sessionId`, `itemId` |

`enqueue` returns `itemId`, `disposition`, `position`, `paused`, and
`idempotent`. List responses contain previews and byte counts, never prompt
bodies. A recovered inbox is paused; clients should let users inspect it before
calling `setPaused` with `false`.

## Runtime reload and extension surface

Reasonix advertises two more extension points in
`agentCapabilities._meta["reasonix.io"]`:

- `sessionReloadExtensions` — the vendor method
  `_reasonix.io/session/reloadExtensions`. Calling it reloads the session's
  agent runtime (extensions, tools, skills, commands, hooks, providers) with
  the same fail-atomic semantics as the CLI `/reload` command: while a turn
  or rebuild is active exactly one reload is queued (`{"queued": true}`) and
  runs when the session goes idle; otherwise the runtime is rebuilt and
  swapped atomically, and a failed rebuild keeps the previous runtime. After
  a successful reload Reasonix pushes a fresh `available_commands_update`.
- `extensionSurface` — structured extension UI support. Clients that also
  advertise `reasonix.io.extensionSurface` in their initialize `_meta`
  receive structured extension surface payloads; clients without it receive
  equivalent text fallbacks (`agent_message_chunk` for cards and statuses,
  permission requests for extension forms), so no client-side handling is
  required to stay compatible.

Extension actions declared by installed plugins are exposed as
`/<plugin>:<action>` in `available_commands_update` and can be invoked like
any other slash command.

## Compatibility and cache behavior

| Surface | Older or non-Reasonix clients | Conclusion |
| --- | --- | --- |
| Existing ACP v1 methods | Their names and response shapes are unchanged. | Compatible |
| Capability `_meta` | Unknown metadata may be ignored. | Compatible |
| Persisted transcripts | Transcript schema is unchanged; the inbox is a versioned sidecar. | Compatible |
| CLI, Desktop, and Bot steering | Rejected steers remain durable follow-ups. | Compatible |

Steering appends a user-requested message to normal conversation history. It
does not change the system prompt, tool schemas, tool order, or other stable
provider-prefix bytes. The next provider request necessarily misses the suffix
that did not previously exist, just like any normal new user message, while the
earlier prefix remains reusable.

## Client integration checklist

1. Launch `reasonix acp` with separate stdin, stdout, and stderr streams.
2. Call `initialize` and honor both standard and `_meta` capabilities.
3. Open sessions with absolute workspace paths and keep their ids isolated.
4. Process agent-to-client filesystem, terminal, and permission requests while
   a prompt is running.
5. Show steer UI only when the Reasonix capability is advertised and a prompt
   is active.
6. Branch on the steer `disposition`; both accepted steer and queued follow-up
   are durable, but only the former can affect the active turn.
7. Use `session/close` for resource cleanup and `session/delete` only when the
   user intends to remove persisted history.
