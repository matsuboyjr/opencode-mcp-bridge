<!-- Modified in this fork to add OpenCode V1/V2 adapters and isolated verification. -->
# opencode MCP bridge

This is a fork of [leon123858/opencode-mcp-bridge](https://github.com/leon123858/opencode-mcp-bridge), with an OpenCode V2 adapter alongside the existing V1 client. The Apache-2.0 [LICENSE](LICENSE) and existing attribution are retained.

**Verification status:** the V2 implementation is based on the official **2.1.1** tagged OpenAPI and server sources. The isolated test matrix targets **1.2.27** and **2.1.1**. Real-server compatibility is **not yet verified**: this development session has no Go toolchain and cannot access the Docker daemon. Do not interpret the fixtures or source review as a successful integration test. See [the investigation and validation record](docs/opencode-v2.md).

An HTTP bridge implemented in Go that allows MCP clients to connect to an existing OpenCode server via Streamable HTTP or a REST API. The bridge **will not** start OpenCode automatically.

## Quick Start

Requires Go 1.25+ and the `opencode` CLI. Start OpenCode first, then start the bridge:

```powershell
opencode serve --port 4096
$env:OPENCODE_BASE_URL = "http://127.0.0.1:4096"
go run ./cmd
```

The bridge listens on `:8080` by default. Available environment variables:

- `BRIDGE_LISTEN_ADDRESS`: Listen address, default `:8080`
- `BRIDGE_REQUEST_TIMEOUT`: Upstream HTTP timeout, default `60m`
- `OPENCODE_MCP_BRIDGE_API_KEY`: Optional incoming Bridge API key. When nonempty, REST, Streamable HTTP (`/` and `/mcp`), and legacy SSE (`/mcp/sse`, including its POST requests) require `Authorization: Bearer <key>`. Unset or empty keeps authentication disabled for compatibility and emits a startup warning. `GET /healthz` stays public for liveness checks. Whitespace or malformed Bearer-token characters in a configured key cause a startup error.
- `OPENCODE_BASE_URL`: OpenCode server URL, default `http://127.0.0.1:4096`
- `OPENCODE_SERVER_USERNAME` / `OPENCODE_SERVER_PASSWORD`: HTTP Basic Auth credentials
- `OPENCODE_API_VERSION`: `auto` (default), `v1`, or `v2`. Detection checks JSON structure and version, not just HTTP status. Only missing/non-JSON V1 health routes permit trying V2 info. Authentication failures and network/server errors stop detection. Successful detection is cached for the bridge process; restart it after replacing the upstream API version.
- `OPENCODE_DIRECTORY`: absolute directory **on the OpenCode server**, for example `/work/my-project`. Session listing includes this directory and every descendant directory, including nested Git projects. Omit it to use the upstream default directory as the listing base. History and session operations remain restricted to the exact base directory. Use the upstream's canonical spelling, including case and symlinks; ownership checks fail closed on a mismatch. Run a separate bridge for each directory you need to operate in.

When a password is set without a username, the bridge uses `opencode`, the V2 server's fixed Basic Auth username. V1 also defaults to that username. Never put credentials in the server URL.

### Connecting to an existing V2 server

On the OpenCode host, start an explicitly authenticated foreground server in the desired directory (V2.1.1):

```sh
cd /path/to/project
OPENCODE_PASSWORD='your-server-password' opencode serve --hostname 0.0.0.0 --port 4096
```

Configure the bridge to connect to that existing server:

```sh
export OPENCODE_BASE_URL=http://opencode:4096
export OPENCODE_API_VERSION=auto
export OPENCODE_DIRECTORY=/path/to/project
export OPENCODE_SERVER_USERNAME=opencode
export OPENCODE_SERVER_PASSWORD='your-server-password'
go run ./cmd
```

For V1 use `OPENCODE_SERVER_PASSWORD` when starting OpenCode. The bridge-side credential variables stay the same for both versions. Keep the server on a private network or use an authenticated TLS endpoint when connecting across hosts.

## MCP Connection

The Streamable HTTP endpoint is `http://localhost:8080/mcp` (the root path `/` is also supported). Client configuration example:

```json
{
  "mcpServers": {
		"opencode": {
			"url": "http://localhost:8080/mcp",
			"timeout": 3600
		}
  }
}
```

Legacy clients requiring the MCP 2024-11-05 HTTP+SSE transport can use `http://localhost:8080/mcp/sse`. Modern clients should prefer Streamable HTTP via `/mcp`.

### Bridge API key authentication

Set the key in the Bridge process environment, then restart the Bridge:

```sh
export OPENCODE_MCP_BRIDGE_API_KEY='replace-with-a-long-random-key'
go run ./cmd
```

On PowerShell, use `$env:OPENCODE_MCP_BRIDGE_API_KEY = 'replace-with-a-long-random-key'`. Use a cryptographically random key, for example 32 random bytes encoded as hex, and keep it out of version control. The repository contains no production key.

Configure the MCP client's custom HTTP headers (the exact configuration UI depends on the client):

```json
{
  "mcpServers": {
    "opencode": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "Bearer <your-bridge-api-key>"
      }
    }
  }
}
```

For REST, supply the same header:

```sh
curl -H "Authorization: Bearer $OPENCODE_MCP_BRIDGE_API_KEY" \
  http://127.0.0.1:8080/opencode/setup
```

Missing, malformed or incorrect credentials receive HTTP 401 with a Bearer challenge. Query parameters are not accepted as credentials. Legacy SSE clients must send the header on both the event stream and message POST requests. This is a single shared key, without OAuth discovery, per-user permissions or key issuance endpoints. Rotation requires changing the environment and restarting the Bridge; existing long-lived streams should be reconnected.

The Bridge key is independent of `OPENCODE_SERVER_USERNAME` / `OPENCODE_SERVER_PASSWORD`, which authenticate the Bridge to OpenCode. It is not forwarded upstream. When accessing the Bridge across an untrusted network, use HTTPS via a reverse proxy. Without a key, restrict access using loopback binding (`BRIDGE_LISTEN_ADDRESS=127.0.0.1:8080`) or a private network. CORS preflight remains available, but the actual operation requires the key when configured.

The unused repository-local `logReader` OpenCode MCP debug configuration has been removed.

The bridge exposes nine tools:

- `opencode_setup`
- `opencode_ask`
- `opencode_reply`
- `opencode_run`
- `opencode_check`
- `opencode_conversation`
- `opencode_sessions_overview`
- `opencode_mcp_servers`
- `opencode_provider_test`

### V1 / V2 behavior

The MCP endpoint, nine tool names, and existing arguments are retained. API differences are handled in `client/`; no V2 mutation is retried against V1.

| Tool | V1 | V2.1.1 adapter |
|---|---|---|
| `opencode_setup` | Health and current project | Server info and resolved location/project |
| `opencode_ask` | Create, admit prompt asynchronously, wait for a new completed answer | Create at the scoped location, admit text input, wait for its matching transcript boundary and successful idle record |
| `opencode_reply` | New answer in the existing session | Same session, with an admitted prompt ID; older answers cannot satisfy the wait |
| `opencode_run` | Async prompt plus bounded polling | Async prompt plus bounded transcript polling; returns `completed` only with successful idle and nonempty assistant text |
| `opencode_check` | Status plus pending permissions/questions; optional todo/diff | Active execution, pending permissions/forms and session outcome. `detailed=true` returns an explicit unsupported error |
| `opencode_conversation` | Native V1 `{info, parts}` message array | Native V2 message array with `type` and assistant `content`; newest page returned in chronological order |
| `opencode_sessions_overview` | Global sessions filtered to the base directory and descendants | Global cursor-paginated sessions filtered to the base directory and descendants |
| `opencode_mcp_servers` | Native V1 object | Native V2 server array; includes upstream connection state |
| `opencode_provider_test` | Provider/model availability and real prompt | Normalize provider/model catalogs for availability, then send a real prompt |

`message` contains assistant text only. `raw` and conversation history may contain reasoning and tool data; treat them accordingly. An empty text answer, provider failure, interrupted execution, approval wait or question wait does not produce a completed answer.

`opencode_check` distinguishes `busy`, `idle`, `failed`, `interrupted`, `approval_required`, and `question_required` on V2. Idle alone does not establish that a requested prompt succeeded. Ask/reply/run return MCP tool errors for pending user interaction, including the session ID. Resolve requests in OpenCode's own UI, then use check/conversation. The bridge does not approve permissions or answer questions automatically.

For example, a base of `/projects` lists sessions in `/projects`, `/projects/test` and `/projects/test/sub`, including separate projects under those paths and archived sessions. `/projects-other` and `/other` are excluded. Listing does not imply an active execution or authorize follow-up prompts in a listed child directory. Set up a bridge scoped to that child directory to operate on its sessions. The listing uses upstream path strings; it does not traverse the host filesystem or expand symlinks.

**V2 model and agent selection persists for subsequent turns of the entire session.** It is not a per-message override and is not rolled back. When only `providerID` is supplied, the adapter selects the first enabled model for that provider in the upstream catalog order. A V2 `modelID` requires `providerID`. Avoid concurrent prompts or configuration changes to the same session from multiple clients; upstream selection and prompt admission are separate operations.

`BRIDGE_REQUEST_TIMEOUT` bounds upstream HTTP calls and ask/reply/run waits; `opencode_run.maxDurationSeconds` can shorten that wait. Timeout or cancellation attempts a bounded abort (V1) or interrupt with `resume=false` (V2), and reports the session ID and interrupt error if any. An interrupted HTTP request can have an ambiguous admission result: inspect the session instead of automatically resending. The adapter never retries a mutation.

The Go package's historical `client.New` constructor remains V1-only. The executable uses `client.NewWithOptions`, which enables the versioned adapter and stricter workspace/wait checks.

## REST API

| Method | Path | Functionality |
|---|---|---|
| GET | `/opencode/setup` | Check service health and current project |
| POST | `/opencode/ask` | Create a new session and send a prompt |
| POST | `/opencode/reply` | Send a follow-up prompt to an existing session |
| POST | `/opencode/run` | Execute an asynchronous task and wait for completion |
| GET | `/opencode/check?sessionId=...` | Poll session status |
| GET | `/opencode/conversation?sessionId=...&limit=...` | Retrieve conversation history |
| GET | `/opencode/sessions-overview` | List all sessions |
| GET | `/opencode/mcp-servers` | List configured OpenCode MCP servers |
| POST | `/opencode/provider-test` | Test a specific provider/model |

REST call example:

```powershell
Invoke-RestMethod http://localhost:8080/opencode/ask `
  -Method Post -ContentType application/json `
  -Body '{"prompt":"Explain this project"}'
```

If OpenCode is unreachable, the API returns HTTP 503:

```json
{
  "error": "cannot connect to OpenCode server",
  "hint": "Please run `opencode serve` to start the OpenCode server first"
}
```

## Validation & Testing

### Isolated Docker matrix (Linux / macOS / Docker Desktop)

```sh
sh integration/run.sh
```

This builds the bridge and runs unit tests in Go **1.25.1**, then starts real OpenCode **1.2.27** and **2.1.1**, two bridges and a deterministic OpenAI-compatible mock LLM. It requires a working Docker daemon and image/module/package download access. The mock supports streaming chat completions and actual harmless tool calls in `/work/a`; it does not replace the OpenCode server API.

Each run uses a unique Compose project, separate V1/V2 home and working volumes, and a second test project `/work/b`. No host home, real project, Docker socket, cloud credentials or SSH agent is mounted. No host ports are published, and no privileged containers are used. Only test fixtures are bind-mounted, read-only. Test authentication credentials are deliberately non-secret and must never be reused in production.

The MCP runner initializes the protocol, lists the nine tools, calls them, and asserts session IDs, directory ownership, model/agent selection, conversation continuity, async results, MCP listing, real tool output, authentication rejection, unavailable upstream, timeout, approval waits and question waits. Assertions fail rather than skip when an upstream contract or mock-tool format differs. Model wording is not compared. This tests protocol contracts with a mock LLM; it does **not** establish real-model quality or compatibility.

Logs, build output and resolved image IDs are retained under `.run/bridge-compat-*/`, including on failure. The cleanup trap removes only that run's Compose containers/networks/volumes. Check `cleanup.log` if daemon access fails during cleanup. Inspect `tests.log` and `containers.log` when an actual server rejects configuration or streaming.

To run Go checks directly on Linux with Go installed:

```sh
gofmt -w cmd client config handlers mcpbridge server types
go vet ./...
go test ./...
go test -race ./...
go build -o bin/opencode-mcp-bridge ./cmd
```

### Existing Windows workflow

```powershell
make check
make e2e
```

`make check` sequentially runs code formatting, vet, unit tests, and the build process. `make e2e` requires OpenCode to have an available provider/model configured; it starts OpenCode and the bridge, calls all nine MCP tools—including an ask/reply conversation chain and a cross-file audit task via `opencode_run`—and finally cleans up all test sessions and background processes.

`make test` generates `.run/coverage.out` and enforces a minimum overall statement coverage of 80% across core packages.

### Applying to an existing Docker Compose deployment

Rebuild the bridge image from this fork and retain the existing `/mcp` URL and tool arguments. Add these variables to its service; paths refer to the OpenCode container, not the bridge container:

```yaml
environment:
  OPENCODE_BASE_URL: http://opencode:4096
  OPENCODE_API_VERSION: auto # or v1 / v2 to require one API
  OPENCODE_DIRECTORY: /work/project
  OPENCODE_SERVER_USERNAME: opencode
  OPENCODE_SERVER_PASSWORD: ${OPENCODE_SERVER_PASSWORD}
  BRIDGE_REQUEST_TIMEOUT: 60m
  OPENCODE_MCP_BRIDGE_API_KEY: ${OPENCODE_MCP_BRIDGE_API_KEY}
```

For the V2.1.1 OpenCode service, configure `OPENCODE_PASSWORD` (the legacy `OPENCODE_SERVER_PASSWORD` alias is also accepted), use `serve --hostname 0.0.0.0 --port 4096`, and provide the matching server-side project directory. V2 provider configuration uses `providers`, `package` and `settings`; migrate OpenCode's configuration separately using its official migration guide. Keep the test Compose and its data volumes separate from the existing deployment. No production Compose changes are applied by this repository's test runner.

Supply a nonempty `OPENCODE_MCP_BRIDGE_API_KEY` in the deployment environment to enable Bridge authentication. Leaving it unset or empty disables authentication. The isolated test Compose uses a separate, deliberately non-secret Bridge key and checks both accepted and rejected credentials before running the MCP tool contracts.

### Limitations

- Runtime V1/V2 verification remains pending until the toolchain and Docker access are available; the test harness itself may need corrections after that first run.
- V2 `detailed` todo/diff is unsupported. The adapter does not pretend that V2 turn-diff semantics are the V1 diff contract.
- Conversation `raw` schemas and MCP server listing shapes differ between versions; consumers of raw data must branch on the version reported by setup.
- V1 pending-state checks require the permission/question endpoints, and cross-project listing requires `/experimental/session`, present in the pinned V1 target. Errors on older servers are reported explicitly. The V1 global list uses an increasing limit to avoid losing sessions sharing the timestamp-only pagination boundary; response size and timeout limits still apply.
- Session operations use exact-directory ownership even though the overview includes descendants. A moved session is refused; the bridge will not move it back or operate on a sibling project.
- V2 uses polling of durable projected messages instead of consuming the global event stream. Permission/form state is fetched separately. These reads are not an atomic snapshot of a session modified by another client.
