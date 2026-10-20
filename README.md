# inference-gateway

A local, self-hosted AI assistant in the spirit of Claude: a Go gateway in front of
[Ollama](https://ollama.com) models, growing step by step into conversations,
streaming, tool use, memory and vision — all running on your own machine.

> **Limit to keep in mind:** answer quality is bounded by the model that fits on the GPU
> (12 GB → ~14B models, e.g. `qwen3:14b` at 8k context). The gateway adds capabilities;
> it can't make a small model reason like a frontier one.

## Run it

**Locally** (needs Ollama running on `localhost:11434`):

```sh
go run ./cmd/server
curl -X POST localhost:8080/completion -d '{"model":"qwen3:14b","prompt":"Say hi"}'
```

**In Docker** (2 Ollama replicas on the GPU behind nginx, gateway on `:8080`):

```sh
docker compose up -d --build   # first build bakes qwen3:4b-instruct into the Ollama image (deploy/ollama.Dockerfile)
curl -X POST localhost:8080/completion -d '{"model":"qwen3:4b-instruct","prompt":"Say hi"}'
```

**Chat UI** (`web/`, React, streams answers token by token):

- **With Docker:** the gateway image builds the UI and serves it — open <http://localhost:8080>
  after `docker compose up -d --build`.
- **While editing the UI** (hot reload):

```sh
cd web
npm install      # once
npm run dev      # → http://localhost:5173 (proxies /v1 to the gateway on :8080)
```

Point it at another gateway with `GATEWAY_URL=http://host:port npm run dev`. `npm test` runs the
SSE parser tests.

| Env var      | Default                  | Purpose            |
|--------------|--------------------------|--------------------|
| `PORT`       | `8080`                   | gateway port       |
| `OLLAMA_URL` | `http://localhost:11434` | where Ollama lives |
| `STATIC_DIR` | *(empty)*                | folder with the built chat UI to serve at `/`; set to `/web` in the image |

Dashboards: Grafana at <http://localhost:3000> (admin / admin to edit), Prometheus at <http://localhost:9090>.

## API

| Method & path | What it does |
|---|---|
| `GET /health` | liveness check → `{"status": "ok"}` |
| `GET /metrics` | Prometheus metrics |
| `POST /completion` | single prompt → `{"model", "response"}` |
| `POST /v1/chat/completions` | OpenAI-style chat; send the whole `messages` history; `"stream": true` for SSE |

**PowerShell tip:** put the JSON body in a file — inline JSON gets mangled by PowerShell quoting.

```powershell
'{"model":"qwen3:4b-instruct","stream":true,"messages":[{"role":"user","content":"Explain Go channels"}]}' | Set-Content -Encoding ascii chat.json
curl.exe -N -X POST localhost:8080/v1/chat/completions -H "Content-Type: application/json" --data-binary "@chat.json"
```

- Continue a conversation by appending the model's reply as `{"role":"assistant",...}` and your next
  `{"role":"user",...}`; the last message must be from the user.
- `-Encoding ascii` (not `utf8`): Windows PowerShell's UTF-8 adds a BOM that the JSON decoder rejects.

## Roadmap

Tick a box when the step works end to end. Interview questions for each step: [docs/interview-prep.md](docs/interview-prep.md).

### 0. Foundation
- [x] HTTP server with JSON logging and graceful shutdown
- [x] Config from env / `.env`
- [x] `GET /health`
- [x] `POST /completion` — single prompt → Ollama `/api/generate`
- [x] Speed: thinking toggle, `keep_alive`, `num_ctx` 8192 so the model stays on the GPU
- [x] Docker: gateway image, Ollama replicas on the GPU, nginx load balancer

### 1. Conversations
- [x] `POST /v1/chat/completions` taking a `messages` history (OpenAI-compatible) → Ollama `/api/chat`

### 2. Streaming
- [x] Stream tokens to the client as they're generated (SSE) instead of waiting for the full answer — send `"stream": true`

### 3. Tool use / agent loop
Model requests a tool → gateway runs it → result goes back to the model → repeat until done.
- [x] Define a first tool (`get_current_time`) and send its JSON schema to Ollama in `tools`
- [x] Detect `tool_calls` in the model's reply, run the matching Go function
- [x] Append the result as a `{"role":"tool"}` message and call the model again
- [x] Loop until the model answers in plain text, with a max-iterations guard (`maxToolRounds`)
- [x] Second tool with arguments (`calculator`) — validate arguments before running
  - Tools so far (`internal/tools`): `get_current_time` (optional IANA `timezone`), `calculator`
    (`a`, `b`, `op`), `fetch_url` (HTTP GET; blocks private/internal addresses against SSRF,
    10 s timeout, 1 MB read limit, returns ≤ 4000 chars of text)
- [x] Metrics: tool calls by tool name and outcome, loop iterations per request (Grafana row "Tools")
- [x] Tools in streaming mode: forward text, run tools when a stream reports `tool_calls`, open the next stream (one SSE response to the client)

### 4. Tools
- [ ] Read / write files
- [ ] Run code in a sandbox container
- [ ] Web search (SearxNG container)
- [ ] Fetch a URL

### 5. Memory / documents (RAG)
- [ ] Embed files and past chats (`nomic-embed-text`) into a vector store (Qdrant or pgvector)
- [ ] Retrieve the relevant chunks and add them to the prompt

### 6. Vision
- [ ] Accept images (`gemma3`, `qwen2.5-vl`)

### 7. Model routing
- [ ] Send quick requests to a small model and hard ones to `qwen3:14b`

### Observability
- [x] Prometheus + Grafana (`localhost:3000`, dashboard "Inference Gateway"), GPU exporter, node exporter
- [x] Gateway `/metrics`: request count, latency histogram (P50/P95/P99), TTFB, in-flight
- [x] LLM metrics from Ollama stats: tokens in/out, load / prefill / decode time, tokens per second

### 8. Ops
- [ ] API keys
- [ ] Rate limiting
- [ ] Token-usage logging

### 9. Distributed architecture
Split the system into independent services, one container each, that talk to each other only
over the network: each can be built, deployed, scaled and restarted on its own.

```
browser ──▶ frontend (nginx: static UI) ──/v1──▶ API gateway (Go) ──▶ model server (Ollama, GPU)
                                                     │
                                                     └──▶ database (Postgres: users, chats, messages)
```

- [ ] **Frontend service:** move the UI out of the gateway image into its own container (nginx serving
      `web/dist`, proxying `/v1` to the gateway); the gateway goes back to API-only
- [ ] **Database service:** Postgres container with a volume; schema for users, conversations,
      messages; migrations run on deploy
- [ ] **Server-side conversations:** gateway stores and loads history by conversation ID instead of
      the client resending everything (see interview Q16)
- [ ] **Service discovery & config:** services find each other by Docker DNS name via env vars
      (`OLLAMA_URL`, `DATABASE_URL`), nothing hard-coded
- [ ] **Health & startup order:** `/health` (liveness) and `/ready` (can reach DB + model server);
      compose `healthcheck` + `depends_on: condition: service_healthy`
- [ ] **Resilience for network calls:** timeouts on every call, retries with backoff for idempotent
      ones, clear errors when a dependency is down (no hanging requests)
- [ ] **Distributed tracing:** OpenTelemetry trace ID passed frontend → gateway → DB / model server,
      viewed in Jaeger or Grafana Tempo, so one slow request can be followed across services
- [ ] **Scale independently:** run several gateway replicas behind the load balancer (stateless, since
      state lives in the DB); zero-downtime rolling restarts (see interview Q23)
- [ ] **Network isolation:** separate Docker networks so only the gateway can reach the DB and the
      model server; only the frontend is published to the host
- [ ] **Later:** the same services on Kubernetes (Deployments, Services, probes, Secrets)

### Extras
- [x] Chat UI: own React app in `web/` with real-time streaming, stop button, markdown, time-to-first-token
- [x] Serve the chat UI from Docker: built in the gateway image, served at `/` (`STATIC_DIR`)
