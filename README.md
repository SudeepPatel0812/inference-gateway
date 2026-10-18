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
docker compose up -d --build
docker compose exec ollama ollama pull qwen3:4b-instruct   # once; shared by all replicas
curl -X POST localhost:8080/completion -d '{"model":"qwen3:4b-instruct","prompt":"Say hi"}'
```

| Env var      | Default                  | Purpose            |
|--------------|--------------------------|--------------------|
| `PORT`       | `8080`                   | gateway port       |
| `OLLAMA_URL` | `http://localhost:11434` | where Ollama lives |

## Roadmap

Tick a box when the step works end to end.

### 0. Foundation
- [x] HTTP server with JSON logging and graceful shutdown
- [x] Config from env / `.env`
- [x] `GET /health`
- [x] `POST /completion` — single prompt → Ollama `/api/generate`
- [x] Speed: thinking toggle, `keep_alive`, `num_ctx` 8192 so the model stays on the GPU
- [x] Docker: gateway image, Ollama replicas on the GPU, nginx load balancer

### 1. Conversations
- [ ] `POST /v1/chat/completions` taking a `messages` history (OpenAI-compatible) → Ollama `/api/chat`

### 2. Streaming
- [ ] Stream tokens to the client as they're generated (SSE) instead of waiting for the full answer

### 3. Tool use / agent loop
- [ ] Model requests a tool → gateway runs it → result goes back to the model → repeat until done

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

### 8. Ops
- [ ] API keys
- [ ] Rate limiting
- [ ] Token-usage logging

### Extras
- [ ] Chat UI: Open WebUI container pointed at `/v1/chat/completions`
