# paseo-api

An HTTP API in front of a [paseo](https://getpaseo.com) daemon, written in Go.

It speaks the paseo daemon's **native WebSocket protocol** directly (the same one the
`paseo` CLI uses) — **no CLI invocation, no subprocess spawning**. You point it at a
paseo instance with one env var (`PASEO_HOST`) and drive AI coding agents over plain
HTTP: start a run, stream the transcript back, list/inspect/stop/archive agents, and
query providers and daemon status.

It was built to replace CLI-based integrations (which spawned `paseo run` /
`paseo logs` / `paseo delete` as subprocesses) with a single, dependency-free service.

## How it works

The official paseo CLI is just a client to a daemon that listens on `ws://HOST/ws`.
This service reimplements the relevant slice of that protocol in Go:

```
client ──HTTP──▶ paseo-api ──WebSocket (ws://HOST/ws)──▶ paseo daemon ──▶ AI agent
```

A `POST /run` performs the same sequence the CLI does for a foreground run:

1. `workspace.create.request` — create a directory-backed workspace
2. `create_agent_request` — start the agent with the prompt + images
3. `wait_for_finish_request` — block until the agent settles
4. `fetch_agent_timeline_request` — collect the transcript (assistant messages)
5. `delete_agent_request` — clean up

No state is stored — the service is stateless. There is no database.

## Quick start

```bash
docker run --rm -p 3000:3000 \
  -e PASEO_HOST=192.168.0.3:6666 \
  jaledeveloper/paseo-api:latest
```

Then:

```bash
curl -s -X POST http://localhost:3000/run \
  -H 'content-type: application/json' \
  -d '{"prompt":"Reply with only this JSON: {\"ok\":true}","extractJson":true}'
```

API docs (Swagger UI): open <http://localhost:3000/docs>. Raw spec: `GET /openapi.yaml`.

## Configuration

All configuration is via environment variables. `PASEO_HOST` is the only required one.

| Variable | Default | Description |
| --- | --- | --- |
| `PASEO_HOST` | *(required)* | paseo instance `IP:port`, e.g. `192.168.0.3:6666` |
| `PASEO_PASSWORD` | *(empty)* | Daemon password (sent as a Bearer token during the WS handshake) |
| `PASEO_PROVIDER` | `claude` | Default agent provider |
| `PASEO_MODEL` | `claude-opus-4-8` | Default model |
| `PASEO_CWD` | `/app` | Default working directory of the agent on the host |
| `PASEO_MODE` | `bypassPermissions` | Default provider mode |
| `PASEO_THINKING` | `low` | Default thinking option id |
| `PASEO_WAIT_TIMEOUT` | `5m` | Max time to wait for an agent (Go duration or seconds) |
| `PASEO_CONNECT_TIMEOUT` | `15s` | WS connect timeout |
| `API_TOKEN` | *(empty)* | If set, every endpoint except `/health` and the docs requires header `x-api-token: <token>` |
| `PORT` | `3000` | Listen port |

## Endpoints

| Method & path | CLI equivalent | Description |
| --- | --- | --- |
| `GET /health` | — | Liveness probe (no token) |
| `GET /docs`, `GET /openapi.yaml` | — | Swagger UI + OpenAPI spec (no token) |
| `POST /run` | `paseo run` (+ `logs` + `delete`) | Run an agent and wait for the result |
| `POST /agents` | `paseo run` | Alias of `POST /run` |
| `GET /agents` | `paseo agent ls` | List agents (`?includeArchived=true`) |
| `GET /agents/{id}` | `paseo agent inspect` | Inspect one agent (id, prefix or title) |
| `GET /agents/{id}/logs` | `paseo agent logs` | Transcript (`?extractJson=true` for JSON) |
| `POST /agents/{id}/messages` | `paseo agent send` | Send a follow-up message |
| `POST /agents/{id}/stop` | `paseo agent stop` | Interrupt the current run |
| `POST /agents/{id}/mode` | `paseo agent mode` | Change the agent mode |
| `POST /agents/{id}/archive` | `paseo agent archive` | Soft-delete |
| `DELETE /agents/{id}` | `paseo agent delete` | Hard-delete |
| `GET /providers` | `paseo provider ls` | List providers |
| `GET /providers/{provider}/models` | `paseo provider models` | List models + thinking options |
| `GET /daemon/status` | `paseo daemon status` | Daemon health & provider availability |

### `POST /run`

Request:

```jsonc
{
  "prompt": "…",                 // required
  "images": [                    // optional
    { "data": "<base64>", "mimeType": "image/jpeg" }
  ],
  "provider": "claude",          // optional, overrides PASEO_PROVIDER
  "model": "claude-opus-4-8",    // optional, overrides PASEO_MODEL
  "cwd": "/app",                 // optional, overrides PASEO_CWD
  "mode": "bypassPermissions",   // optional
  "thinking": "low",             // optional
  "waitTimeoutMs": 300000,       // optional, 0 = config default
  "extractJson": true            // optional, also return JSON parsed from the transcript
}
```

Response:

```jsonc
{
  "agentId": "agt_…",
  "status": "completed",          // or "error" / "timeout" / "permission"
  "transcript": "…full assistant text…",
  "json": [ { "ok": true } ]      // only when extractJson=true
}
```

When the agent fails/times out, the API responds `502` with `{ "message": "…" }`.

## Using from a Node/TypeScript client

If you previously spawned the `paseo` CLI from a client, you can switch to this
service by replacing that with a single HTTP call:

```ts
// paseo-client.ts (sketch)
export class PaseoApiClient {
  constructor(private apiUrl: string, private token?: string) {}

  async run(prompt: string, imagePaths: string[] = []): Promise<string> {
    const images = await Promise.all(
      imagePaths.map(async (p) => ({
        data: (await readFile(p)).toString("base64"),
        mimeType: lookup(p) || "application/octet-stream",
      })),
    );
    const res = await fetch(`${this.apiUrl}/run`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        ...(this.token ? { "x-api-token": this.token } : {}),
      },
      body: JSON.stringify({ prompt, images }),
    });
    if (!res.ok) throw new Error(`paseo-api run failed: ${res.status}`);
    const { transcript } = await res.json();
    return transcript; // raw transcript
  }
}
```

Any client-side JSON extraction keeps working as-is on `transcript`, or you
can pass `extractJson: true` and read the `json` array directly.

## Development

Go is not required locally — everything builds in Docker:

```bash
docker build -t jaledeveloper/paseo-api:dev .
docker run --rm -p 3000:3000 -e PASEO_HOST=192.168.0.3:6666 jaledeveloper/paseo-api:dev
```

With Go installed:

```bash
go build ./...
go vet ./...
PASEO_HOST=192.168.0.3:6666 go run ./cmd/server
```

Layout:

```
cmd/server         entrypoint (config, HTTP server, graceful shutdown)
internal/config    env configuration
internal/httpapi   HTTP router, middleware, handlers, OpenAPI/Swagger
internal/paseo     native paseo daemon client (WebSocket protocol + RPCs)
```

## Releasing & Docker Hub

Pushing a git tag `v*` (or publishing a GitHub Release) triggers
`.github/workflows/docker.yml`, which builds a multi-arch image
(`linux/amd64`, `linux/arm64`) and pushes it to `jaledeveloper/paseo-api`.

Required repository secret: **`DOCKERHUB_TOKEN`** — a Docker Hub access token
(Docker Hub → Account Settings → Security). The username is `jaledeveloper`
(hardcoded in the workflow).

```bash
git tag v0.1.0
git push origin v0.1.0
```

## Scope

This service covers the **agent lifecycle** (run, list, inspect, logs, send, stop,
mode, archive, delete) plus provider and daemon introspection — the core of what
paseo does. The CLI's auxiliary command groups (terminals, schedules, chat, loops,
worktrees, permits, daemon administration) are intentionally out of scope; they can be
added the same way (each is one more daemon RPC wrapped in a handler).
