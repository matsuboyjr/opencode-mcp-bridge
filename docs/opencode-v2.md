<!-- Added for this fork's OpenCode V2 compatibility investigation. -->
# OpenCode V2 investigation and validation record

## Status

Implementation and a reproducible Docker test matrix have been added. **Go compilation, Go unit tests and real OpenCode integration tests have not run successfully in this session.** This is an implementation awaiting runtime validation, not a completed compatibility certification.

The working branch is `opencode-v2`. The initial working tree was clean. No commits, pushes, PRs or production changes were made. Existing Apache-2.0 LICENSE text is unchanged. Changed source/config/documentation files carry modification notes; new files identify their purpose in this fork.

## Primary evidence

The implementation was checked against the official `anomalyco/opencode` **v2.1.1** tag, tree SHA `ba546ec1598a70db2570e9c6258028d955ba8f58`, fetched through the connected GitHub reader:

- [Tagged OpenAPI](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/protocol/openapi.json): paths, deep-object query encoding, model references, response envelopes, session/message schemas and pending-request APIs.
- [Session handlers](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/server/src/handlers/session.ts): create location defaults, prompt admission, model/agent selection and interrupt behavior.
- [Message handler](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/server/src/handlers/message.ts): default descending message order, opaque cursors and projected transcript pagination.
- [Authentication](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/server/src/auth.ts): fixed `opencode` username and password authentication.
- [CLI server process](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/cli/src/server-process.ts), [CLI environment](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/cli/src/env.ts): `OPENCODE_PASSWORD`, the legacy password alias, foreground server mode and isolated configuration/data options.
- [Provider configuration schema](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/schema/src/config/provider.ts), [configuration tests](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/core/test/config/provider.test.ts) and [OpenAI-compatible provider](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/ai/src/providers/openai-compatible.ts): V2 test provider configuration.
- [Shell tool](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/core/src/tool/plugin/shell.ts), [question tool](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/core/src/tool/plugin/question.ts), [Code Mode](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/core/src/codemode/tool.ts): harmless fixture tool calls, pending forms, and the `execute` wrapper.
- V1 **v1.2.27** [permission routes](https://github.com/anomalyco/opencode/blob/v1.2.27/packages/opencode/src/server/routes/permission.ts) and [question routes](https://github.com/anomalyco/opencode/blob/v1.2.27/packages/opencode/src/server/routes/question.ts): pending user interaction checks.

The [live official API reference](https://dev.opencode.ai/v2/docs/api), [migration guide](https://dev.opencode.ai/v2/docs/migrate-v1/) and [installation/Docker documentation](https://dev.opencode.ai/v2/docs/) were also inspected. The live documentation can change; the tagged source above is the version-specific basis. No real-server responses have yet been collected.

## Adapter decisions

| Concern | V1 | V2.1.1 |
|---|---|---|
| Identity probe | `/global/health`: `healthy=true`, `version` beginning `1.` | `/api/info`: V2 version, nonnegative `pid`, `urls` array and `paths.tmp` |
| Project resolution | Explicit directory, otherwise `/path` | `/api/location?location[directory]=...`; directory and project structure validated |
| Create session | `/session` with directory query | `/api/session` with JSON `location.directory`, unwrap `data` |
| Overview | `/experimental/session` across projects, filtered locally to the base directory and descendants | `/api/session` without directory/project equality filters; follow all cursors and filter locally to the base directory and descendants |
| Ownership | Compare session `directory` with scoped directory | Compare session `location.directory`; do this before prompt/selection and history reads |
| Prompt | `/prompt_async` with `parts`, per-prompt model/agent | `/prompt` with `text`, asynchronous admitted input ID |
| Model reference | `providerID`, `modelID` | `providerID`, `id`; `/model` changes the session's later turns |
| Agent | Prompt argument | `/agent` changes the session's later turns |
| Busy | `/session/status` | `/api/session/active`, whose entries have `type=running` |
| Approval | `/permission` filtered by session | `/api/session/{id}/permission` |
| Question | `/question` filtered by session | `/api/session/{id}/form` |
| Completion | New completed assistant message, no provider error, nonempty text, idle status | Find the admitted prompt in the durable transcript, then assistant text and an `idle` message with `outcome=succeeded` |
| Failure | Assistant `info.error` | Assistant `error`, idle `failed`/`interrupted`, or pending interaction |
| Answer text | Only `parts[type=text]` | Only assistant `content[type=text]` |
| Cancellation | Bounded `/abort` | Bounded `/interrupt?resume=false` |
| Detailed todo/diff | Existing routes | Explicit unsupported error; different turn-based diff API is not silently substituted |

The adapter does not subscribe to SSE. It uses the V2 projected transcript, whose message variants include user, assistant, model/agent changes and idle outcome records. Merely observing no active execution or receiving 204 from the experimental wait endpoint is insufficient evidence of successful completion or nonempty text. Pending requests are read separately and surfaced as errors with session IDs.

The overview now includes descendants of the resolved base directory. The tagged V2 [session store](https://github.com/anomalyco/opencode/blob/v2.1.1/packages/core/src/session/store.ts) uses equality for its `directory` filter. The V1 [ordinary listing](https://github.com/anomalyco/opencode/blob/v1.2.27/packages/opencode/src/session/index.ts) is also project-scoped, so the adapter uses its [experimental global endpoint](https://github.com/anomalyco/opencode/blob/v1.2.27/packages/opencode/src/server/routes/experimental.ts). V1's global cursor is timestamp-only: an increasing limit avoids omitting ties. Neither version applies the root equality filter upstream. Local filtering checks path-component boundaries, retains nested Git projects, and excludes similarly prefixed sibling directories. This listing change does not broaden mutation or history access.

Read-only API detection happens before any mutation and is cached only on success. An HTTP 401/403, network error, 5xx or wrong JSON health structure is never reinterpreted as permission to try another API. Missing routes and non-JSON V1 responses can lead to a V2 info probe. No failed mutation is replayed against another version.

## Reproducible runtime validation

Run `sh integration/run.sh` with Docker access. Exact target images are:

- `ghcr.io/anomalyco/opencode:1.2.27`
- `ghcr.io/anomalyco/opencode:2.1.1`
- Bridge builder `golang:1.25.1-alpine3.22`
- Mock LLM and MCP runner `python:3.13.7-alpine3.22`

Image IDs are recorded per successful build/start. Test project names and all persistent data are unique per invocation. The runner does not mount host projects, home directories, Docker sockets, AWS credentials or SSH agents. Cleanup is restricted by its unique Compose project name. Diagnostic files remain under ignored `.run/` directories even on failure.

The mock exercises OpenAI chat-completion JSON and SSE responses, direct V1 `bash`, V2 `shell`/Code Mode `execute`, and question requests. Unknown tools cause explicit fixture failure. It verifies protocol behavior only; validation with real models remains a separate task.

## Checks attempted in this session

| Check | Result |
|---|---|
| `git status --short`, branch inspection | Clean initial tree; host-created `opencode-v2` branch confirmed |
| `docker compose -f integration/compose.yaml config --quiet` | Passed |
| Python AST parse of integration scripts | Passed |
| JSON parsing of V1/V2 test configuration | Passed |
| `sh -n integration/run.sh integration/start-opencode.sh` | Passed |
| `git diff --check` | Passed at time of inspection; repeat after subsequent edits |
| `make check` | Blocked at `gofmt`: executable missing |
| `go test ./...` | Blocked: `go` executable missing |
| `sh integration/run.sh` | Blocked before starting containers: host Docker client/buildx state read-only, then Docker socket access denied |
| Retry with isolated writable Docker client/buildx state | Still blocked by Docker socket access denied |
| Local mock LLM HTTP/SSE smoke test | Blocked: creating even a loopback socket returns `Operation not permitted` |
| V1/V2 actual server startup, authentication and MCP contracts | **Not executed** |
| Go vet/build/race tests and formatting | **Not executed** |

The test runner now keeps Docker client/buildx state in its own writable diagnostic directory; daemon access is still required. The execution policy prohibits escalation requests, so Docker access cannot be repaired from inside this session. Once Go/Docker are available, run the commands in README, investigate failures, and update this record with actual image IDs and output. Do not mark the real-server requirements complete before doing so.
