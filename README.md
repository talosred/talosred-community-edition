# TalosRed — Community Edition

**A single Go binary that sits between your app and the LLM providers, so you can finally _see_ what your code is doing.**

Local-first LLM proxy + observability dashboard. One OpenAI-compatible endpoint for OpenAI, Anthropic, and Gemini. Every request is logged with cost, latency, and the exact payloads — live, in a built-in web UI. No Python, no Node, no Postgres, no Redis. Just a binary and a `talosred.db` file.

```
your app ──▶ http://localhost:8080/v1 ──▶ TalosRed ──▶ OpenAI / Anthropic / Gemini / Ollama
                                              │
                                              └──▶ talosred.db  +  http://localhost:8080/ui
```

---

## Why

We were tired of running heavy Python proxies and standing up a database just to see our LLM logs locally. Sharing one OpenAI key across a team is a black box: you can't tell whose runaway loop spiked the bill, you can't see the raw JSON your SDK actually sent, and a provider 429 takes down everyone's local build at once.

TalosRed CE is the opposite of that:

- **Single dependency-free binary.** HTML templates, the HTMX library, CSS, and the SQLite engine are all compiled in via `go:embed`. `go build` → one executable.
- **SQLite, not Postgres.** A `talosred.db` file is created on startup. Delete it and restart for a clean slate in one second.
- **Localhost by default.** Binds to `127.0.0.1`. No API keys required to view the dashboard.
- **Respects your machine.** No build step, no container orchestration, no background daemons.

---

## 60-Second Quickstart

```bash
# 1. Build (or download a release binary)
go build -o talosred .

# 2. Put your real provider keys in the proxy's environment — once.
export OPENAI_API_KEY=sk-...
export ANTHROPIC_API_KEY=sk-ant-...
export GEMINI_API_KEY=...

# 3. Run it
./talosred
#  TalosRed CE listening on http://127.0.0.1:8080
#  Proxy:     http://127.0.0.1:8080/v1/chat/completions
#  Dashboard: http://127.0.0.1:8080/ui
```

Point your existing code at the proxy — change only the base URL:

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="sk-talos-local",        # dummy key; the real one lives in the proxy
)

resp = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}],
)
```

Open **http://localhost:8080/ui** and watch the request stream in live — cost, latency, tokens, raw payloads.

---

## Features

### Unified routing — one API for three providers
Expose a standard `/v1/chat/completions` endpoint. Write OpenAI-format requests; TalosRed translates them to each provider's native format upstream.

- **OpenAI** — passthrough.
- **Anthropic** — translated to the Messages API (system prompt, `max_tokens`, streaming).
- **Gemini** — translated to `generateContent` / `streamGenerateContent`, roles mapped.

The provider is inferred from the model name (`gpt-*`/`o1`/`o3` → OpenAI, `claude-*` → Anthropic, `gemini-*` → Gemini). Override per-request with an `X-Provider: anthropic` header.

Streaming (SSE) is supported on all three, with **Time-to-First-Token (TTFT)** measured from the first streamed chunk.

### Cost attribution — *"who burned $500 last night?"*
Tell your developers to tag their traffic:

```
X-Talos-App:  billing-service
X-Talos-User: dave
```

The **Usage** page (`/ui/usage`) groups total cost, tokens, and average latency by app and by user. No more guessing whether it was Dave's frontend loop or the CI pipeline. The logs table is filterable by app and user too.

### Key vaulting — developers never touch raw keys
Put the real provider keys in the proxy environment **once**. Developers hit the proxy with a dummy key. The proxy injects the real key upstream; the real key is never sent back to clients and is redacted everywhere in the UI.

```bash
./talosred --proxy-key sk-talos-local
# Clients must now send:  Authorization: Bearer sk-talos-local
# Missing / wrong key → 401. Real keys stay server-side.
```

Leave `--proxy-key` unset to accept any key (useful for trusted local-only setups).

### Raw payload inspection + "Copy as cURL"
Click any request — including failed ones — to see the exact JSON the proxy sent upstream, the response, status code, and retry count. A **Copy as cURL** button hands you a runnable command (with secrets redacted) so you can reproduce a 400 in your terminal instantly instead of adding print statements to LangChain.

### Automatic 429 retries — stable local builds
When the provider returns `429 Too Many Requests`, TalosRed doesn't immediately fail your app. It backs off and retries (up to 2 retries), honoring the upstream `Retry-After` header, otherwise using linear backoff (2s, 4s) capped at 30s. The retry count is recorded per request and shown in the UI.

### Transparent model aliasing — cut the bill with local models
Route a requested model to a different model or endpoint without changing any application code. Point `gpt-3.5-turbo` at a local Ollama instance and the bill for local dev drops to zero:

| Pattern | Target model | Target URL | Provider |
|---|---|---|---|
| `gpt-3.5-turbo` | `llama3.2` | `http://localhost:11434` | openai |
| `gpt-4*` | `llama3.1:70b` | `http://localhost:11434` | openai |

Manage aliases at `/ui/aliases`. Exact patterns win; otherwise the longest `prefix*` match applies. Ollama is OpenAI-compatible, so use provider `openai` and its base URL. Leave the URL blank to only rename the model against the provider's default endpoint.

### Pre / post-request hooks — your logic, any language
A git-hooks–style plugin system. Drop executable scripts into a directory; TalosRed pipes JSON over stdin and reads JSON from stdout.

```
hooks/
  pre-request/
    01-add-system-prompt.py   # modify the request before it goes upstream
    02-block-pii.sh           # exit non-zero (or {"action":"block"}) to reject
  post-request/
    01-slack-notify.py        # fire-and-forget side effects after the response
```

Pre-request hooks run serially (each sees the previous hook's modifications); post-request hooks run concurrently and can't block the response. Manage and toggle them at `/ui/hooks`, which shows each hook's last exit code, duration, and stderr. See [Hooks](#hooks) below for the wire protocol.

### Live HTMX dashboard
A two-pane dashboard rendered entirely server-side with Go's `html/template` and HTMX — no SPA, no build step. Server-Sent Events push new request rows into the table the instant a prompt fires. Click a row for the drill-down panel.

### OpenTelemetry export
Push traces to your wider observability stack (Jaeger, Datadog, etc.):

```bash
./talosred --otel-endpoint http://localhost:4318
```

Each request becomes a span with `llm.provider`, `llm.model`, `llm.input_tokens`, `llm.output_tokens`, `llm.cost_usd`, `llm.ttft_ms`, `llm.latency_ms`, and `llm.retries` attributes, plus `talos.app` / `talos.user`.

---

## Installation

### Build from source
Requires Go 1.26+.

```bash
git clone https://github.com/talosred/ce.git talosred-ce
cd talosred-ce
go build -o talosred .
./talosred
```

The build is pure Go (SQLite via `modernc.org/sqlite` — no cgo), so it cross-compiles cleanly:

```bash
GOOS=linux  GOARCH=amd64 go build -o talosred-linux-amd64 .
GOOS=darwin GOARCH=arm64 go build -o talosred-darwin-arm64 .
```

### Docker

`Dockerfile`:

```dockerfile
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /talosred .

FROM gcr.io/distroless/static-debian12
COPY --from=build /talosred /talosred
EXPOSE 8080
ENTRYPOINT ["/talosred"]
# Bind to all interfaces inside the container
CMD ["--port", "8080"]
```

`docker-compose.yml`:

```yaml
services:
  talosred:
    build: .
    ports:
      - "8080:8080"
    environment:
      OPENAI_API_KEY: ${OPENAI_API_KEY}
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
      GEMINI_API_KEY: ${GEMINI_API_KEY}
      TALOSRED_PROXY_KEY: sk-talos-local
    volumes:
      - ./talosred.db:/talosred.db   # omit for fully ephemeral runs
```

> Note: TalosRed binds to `127.0.0.1` by default. Inside a container you typically want it reachable on the published port — run behind the container network as shown, or adjust the bind address for your setup.

---

## Configuration

### CLI flags

| Flag | Default | Description |
|---|---|---|
| `--port` | `8080` | Port to listen on (binds `127.0.0.1`). |
| `--db-path` | `talosred.db` | Path to the SQLite database file. |
| `--proxy-key` | `$TALOSRED_PROXY_KEY` | Require this dummy key from clients (key vaulting). Empty disables the check. |
| `--hooks-dir` | `hooks` | Directory holding `pre-request/` and `post-request/` hook scripts. |
| `--otel-endpoint` | _(off)_ | OTLP HTTP endpoint for trace export, e.g. `http://localhost:4318`. |
| `--version` | | Print the build version and exit. |

Release binaries stamp `--version` / `/health` with the short commit SHA; local `go build` reports `dev`.

### Environment variables

| Variable | Purpose |
|---|---|
| `OPENAI_API_KEY` | Upstream key for OpenAI requests. |
| `ANTHROPIC_API_KEY` | Upstream key for Anthropic requests. |
| `GEMINI_API_KEY` / `GOOGLE_API_KEY` | Upstream key for Gemini requests. |
| `TALOSRED_PROXY_KEY` | Default for `--proxy-key`. |

Aliased local endpoints (e.g. Ollama) don't need a provider key — TalosRed sends a dummy token.

---

## Usage Examples

### Switch providers by model name

```python
client.chat.completions.create(model="gpt-4o", messages=[...])                       # → OpenAI
client.chat.completions.create(model="claude-3-5-sonnet-20241022", messages=[...])   # → Anthropic
client.chat.completions.create(model="gemini-1.5-pro", messages=[...])               # → Gemini
```

### Attribute spend

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-talos-local" \
  -H "X-Talos-App: billing-service" \
  -H "X-Talos-User: dave" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

### Force a provider

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "X-Provider: anthropic" \
  -d '{"model":"some-custom-model","messages":[...]}'
```

---

## Hooks

Hooks are any executable file (`chmod +x`) in `hooks/pre-request/` or `hooks/post-request/`. They run in alphabetical order. Disable one without deleting it by creating a sibling `<name>.disabled` file (or toggle it in the UI). A 5-second timeout applies per hook; a hook that times out or errors at the system level is skipped, never blocking your request.

### Pre-request protocol

**stdin:**

```json
{
  "id": "talos-...",
  "provider": "anthropic",
  "model": "claude-3-5-sonnet-20241022",
  "request": { "...": "OpenAI-format ChatRequest" }
}
```

**stdout:**

```json
{"action": "proceed"}                                  // continue unchanged
{"action": "proceed", "request": { "...": "modified" }} // continue with edits
{"action": "block", "message": "PII detected"}          // reject → client gets 403
```

A non-zero exit code also blocks the request (stderr is used as the message).

### Post-request protocol

Post-request hooks observe only — their output doesn't alter the response.

**stdin:**

```json
{
  "id": "talos-...",
  "provider": "anthropic",
  "model": "claude-3-5-sonnet-20241022",
  "request":  { "...": "ChatRequest" },
  "response": { "...": "ChatResponse" },
  "metrics": {
    "input_tok": 150, "output_tok": 80,
    "ttft_ms": 320, "latency_ms": 1100, "cost_usd": 0.0012
  }
}
```

### Example: block prompts containing a secret

```python
#!/usr/bin/env python3
# hooks/pre-request/10-block-secrets.py   (chmod +x)
import sys, json

payload = json.load(sys.stdin)
text = json.dumps(payload["request"]).lower()

if "password" in text or "api_key" in text:
    print(json.dumps({"action": "block", "message": "possible credential in prompt"}))
else:
    print(json.dumps({"action": "proceed"}))
```

---

## Model Pricing

Costs are computed from a `model_pricing` table seeded with current per-1K-token rates for the major OpenAI, Anthropic, and Gemini models. Edit them, add your own, or override variants in the UI at `/ui/pricing`. Unknown models fall back to a prefix match (`gpt-4o-2024-08-06` → `gpt-4o`); anything unmatched is costed at `$0`. Pricing is cached in memory and refreshed on edit.

---

## Dashboard Reference

| Route | Page |
|---|---|
| `/ui` | Live request logs with filters (provider, model, app, user) and drill-down. |
| `/ui/usage` | Spend grouped by app and by user. |
| `/ui/aliases` | Model-alias CRUD (e.g. route to Ollama). |
| `/ui/hooks` | Hook list with enable/disable and last-run status. |
| `/ui/pricing` | Per-model pricing CRUD. |
| `/ui/settings` | API-key status and usage instructions. |
| `/health` | Liveness check — JSON `{status, version, pid, uptime_seconds}`. |

---

## Data & Storage

Everything lives in a single SQLite file (`talosred.db`, WAL mode). It's plain SQLite — query it with any tool:

```bash
sqlite3 talosred.db "SELECT app_name, COUNT(*), ROUND(SUM(cost_usd),4) AS usd
                     FROM requests GROUP BY app_name ORDER BY usd DESC;"
```

The `requests` table stores the request/response JSON, the exact (redacted) upstream URL/headers/body, tokens, TTFT, latency, cost, status code, retries, and attribution. Want a fresh slate? Stop the binary, `rm talosred.db`, restart.

---

## Architecture

```
main.go              entry point, CLI flags, graceful shutdown
proxy/               /v1/chat/completions handler, provider translators,
                     429 retry, key vaulting, attribution, upstream capture
store/               SQLite: requests, model_pricing, model_aliases,
                     attribution aggregates, SSE broadcaster
metrics/             cost calculator (SQLite-backed cache), OTel export
hooks/               external-process hook runner (pre/post-request)
dashboard/           embedded HTMX UI (templates + static assets via go:embed)
```

No cgo. No external services. The HTMX library, CSS, and HTML templates are embedded into the binary.

---

## Scope

This Community Edition is focused on **visibility and routing** for local development. It deliberately does **not** include enterprise concerns like response caching, hard blocking/guardrails, or RBAC.

> **Need enterprise blocking & caching for production?** Once you're used to this level of local visibility, [TalosRed Cloud](https://talosred.io) adds caching, policy enforcement, and team controls for your production environment.

---

## License

See [LICENSE](LICENSE).
