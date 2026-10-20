# Interview prep

Notes for backend / forward-deployed AI engineer interviews, built up one roadmap step at a time.
Each answer is grounded in something this project actually does — use those as your stories
("in my gateway I measured…").

**How to use it:** cover the answer, say yours out loud, then compare. Interviewers care about
*trade-offs* and *numbers you measured* more than definitions.

---

## Core vocabulary (learn these first)

| Term | One-line meaning |
|---|---|
| **Token** | A chunk of text the model reads/writes (~¾ of an English word). Cost, speed and limits are all counted in tokens. |
| **Context window** | Max tokens per request: prompt + history + answer must all fit (`num_ctx`). |
| **Prefill** | Phase 1 of a request: the model reads the whole prompt. Fast per token, parallel. |
| **Decode** | Phase 2: the model writes the answer one token at a time. This is the slow part. |
| **TTFT** | Time to first token — what users feel as "lag". Dominated by queueing + prefill. |
| **Tokens/sec** | Decode speed. Total time ≈ TTFT + output_tokens ÷ tokens/sec. |
| **KV cache** | Memory the model keeps per request for the context. Grows with `num_ctx` — that's why a big context pushed `qwen3:14b` off the GPU. |
| **VRAM** | GPU memory. The #1 constraint in self-hosted LLM serving. |
| **Quantization** | Storing weights in fewer bits (e.g. Q4_K_M ≈ 4-bit) so a model fits in less VRAM, at a small quality cost. |

---

## Step 0 — Foundation

### Q1. Why put a gateway in front of a model server instead of calling Ollama directly?
**Answer:** One place to own the cross-cutting concerns: auth/API keys, rate limits, request
validation, logging and token-usage metering, routing between models, retries/fallbacks, and a
stable API so clients don't break when you swap the backend (Ollama → vLLM → a hosted API). It
also hides internal endpoints (Ollama's admin API can pull/delete models — never expose it).

### Q2. A request takes 85 s. How do you find out why?
**Answer:** Break latency into phases instead of guessing. Ollama returns `load_duration`,
`prompt_eval_duration` (prefill) and `eval_duration` + `eval_count` (decode). In this project:
905 output tokens at 12.3 tok/s = ~74 s of pure decode. Then ask *why* decode was slow →
`ollama ps` showed the model at 16.3 GB with only 10.2 GB in VRAM: the default 40k context's KV
cache spilled to CPU. Setting `num_ctx` to 8192 put it fully on GPU → 49.6 tok/s (4×), and the
same prompt dropped to ~19 s.
**Lesson to say out loud:** *measure per phase, then check resource placement (GPU vs CPU).*

### Q3. How do you make an LLM endpoint *feel* faster without a faster model?
**Answer:** (1) Streaming — the user sees tokens after TTFT (~1 s) instead of waiting for the
whole answer. (2) Fewer output tokens — disable "thinking" for simple tasks (cut a reply here
from 14 s to ~1 s), cap `num_predict`. (3) Keep the model loaded (`keep_alive`) to avoid cold
loads (~8 s here). (4) Route easy requests to a smaller model. (5) Cache identical prompts.

### Q4. Why pass `r.Context()` into the outgoing request to Ollama?
**Answer:** So cancellation propagates. If the client disconnects or the server shuts down,
the upstream call is cancelled and the GPU stops generating tokens nobody will read. On
expensive backends (GPU time) this is real money and capacity, not just tidiness.

### Q5. Explain graceful shutdown. What's wrong with a 10 s shutdown timeout here?
**Answer:** On SIGTERM (Kubernetes sends it before killing a pod) the server stops accepting new
connections and lets in-flight requests finish, up to a deadline, then exits. Our `run()` does
this with `signal.NotifyContext` + `srv.Shutdown(ctx)`.
**The catch:** LLM requests can take 20–80 s, so a 10 s drain cuts long generations off. The
drain timeout must be ≥ your longest expected request, *and* Kubernetes'
`terminationGracePeriodSeconds` (default 30 s) must be longer than that drain, or the pod is
SIGKILLed mid-drain. Streaming + client retries soften this.
*(Exercise: this is a real gap in our `main.go` — decide what timeout you'd pick and why.)*

### Q6. 500 vs 502 vs 504 — which does the gateway return when?
**Answer:** **500** — a bug in *our* code. **502 Bad Gateway** — the upstream (Ollama) is
unreachable or returned garbage; we return this when the dial fails. **504 Gateway Timeout** —
the upstream didn't answer within our deadline. **400** — the client sent bad input (we check
`model` and `prompt`). Getting these right tells on-call *whose* fault an alert is.

### Q7. You need to serve more users. More replicas of the model, or something else?
**Answer:** The GPU is the bottleneck, not the gateway. Options, in order:
- **Batching within one server** (`OLLAMA_NUM_PARALLEL`; vLLM/TGI do *continuous batching*) —
  one model copy serves many requests at once, usually the most efficient use of a GPU.
- **Replicas** only when VRAM allows: here `qwen3:4b` = 3.9 GB, so 2 fit in 12 GB; a 3rd would
  spill to CPU and be slower than queueing.
- **More / bigger GPUs**, or a smaller / more quantized model.

Gateway replicas are cheap — scale them freely; they're stateless.

### Q8. Why `least_conn` instead of round-robin in nginx for LLM backends?
**Answer:** LLM requests vary wildly in duration (1 s vs 80 s). Round-robin can stack several
long requests on one replica while another sits idle. `least_conn` sends each new request to the
replica with the fewest in-flight requests. Also: nginx's default 60 s `proxy_read_timeout`
would kill long generations — every hop's timeout must fit the slowest request.

### Q9. Where does conversation state live? Is the model stateful?
**Answer:** The model is stateless — it remembers nothing between calls. The client (or the
gateway) re-sends the history every request. That's why the context window matters: long chats
eventually need truncation, summarization, or retrieval (RAG). Keeping the gateway stateless is
what lets you run many replicas behind a load balancer.

### Q10 (forward-deployed). A customer wants an LLM on-prem with no internet. What do you ask first?
**Answer:** Hardware (GPU model, VRAM, count) — that decides the model size. Workload (users,
concurrency, typical prompt/answer length, latency target). Data (what documents, how sensitive,
who can see what → RAG with access control). Integration (their auth/SSO, existing APIs,
logging/monitoring). Ops (who upgrades models, air-gapped image delivery, rollback).
Then propose: an open-weight model sized to their VRAM, a serving engine (vLLM/Ollama), a
gateway for auth + metering, and a measured latency/throughput number before go-live.

---

## Step 1 — Conversations

### Q11. The model is stateless. How does a chatbot "remember" the conversation?
**Answer:** The client re-sends the full `messages` history (system / user / assistant turns) on
every request; the model only ever sees what's in the current call. In this project, sending
"My name is Sudeep." → "Nice to meet you" → "What is my name?" in one request returns "Your name
is Sudeep." Send only the last question and it can't know. Same idea as a stateless REST API
with no server-side session.

### Q12. What do `system`, `user` and `assistant` roles actually do?
**Answer:** The model sees one long string, not objects. A **chat template** wraps each turn in
special tokens (for Qwen: `<|im_start|>user … <|im_end|>`) — you can see those in our Ollama
model's stop parameters. `system` sets standing instructions and persona, `user` is the human,
`assistant` is the model's earlier replies (which *you* send back). That's why `/api/chat`
exists: the server applies the right template for each model, so you don't build the string by hand.

### Q13. Why make your API OpenAI-compatible?
**Answer:** It's the de facto standard. Every SDK, chat UI (Open WebUI, LibreChat), framework
(LangChain, LlamaIndex) and eval tool already speaks it, so they work with your server by just
changing the base URL. It also lets you swap backends (local Ollama ↔ a hosted API) without
touching clients. Ours returns `choices[0].message`, the same shape as OpenAI.

### Q14. Resending the whole history every turn sounds wasteful. Is it?
**Answer:** In tokens, yes: the prompt grows each turn, so total tokens processed over a chat
grow roughly with the *square* of the number of turns — that's cost and prefill latency.
Mitigations: **prefix (KV) caching** — the server reuses the computed state for the unchanged
start of the prompt; Ollama reported `prompt_eval_cached_count: 11` of 12 prompt tokens on a
repeat call here, and vLLM/hosted APIs do the same at scale. Plus trimming or summarizing old turns.

### Q15. The conversation outgrows the context window (8k tokens here). What do you do?
**Answer:** Options, simplest first: (1) **Truncate** the oldest turns but always keep the system
prompt and the latest messages. (2) **Summarize** older turns into a short running summary
(costs an extra model call). (3) **Retrieve** — store old turns in a vector DB and pull back only
the relevant ones (RAG, step 5). (4) A model with a bigger window — costs VRAM (see Q2). Mention
you'd count tokens *before* sending, not wait for an error.

### Q16. Where should conversation history be stored: client, gateway or database?
**Answer:** **Client-held** (what we do now, and what OpenAI's chat API does): the gateway stays
stateless and trivially scalable, but every client re-sends everything and you can't do
server-side features. **Server-side** (a DB keyed by conversation ID): enables history across
devices, audit logs, memory and summarization, but adds a database, privacy/retention rules and
per-user access control. Common production answer: stateless inference API + a separate
conversation service that owns storage.

---

## Observability

Run used for the numbers below: one client chatting for 5 minutes with `qwen3:4b-instruct`
on 2 GPU replicas — 372 turns, 0 errors.

### Q17. What would you monitor on an LLM gateway?
**Answer:** Three layers.
- **RED for the API** (Rate, Errors, Duration): requests/s by route, 5xx rate, latency P50/P95/P99,
  plus in-flight requests and TTFB.
- **LLM-specific:** tokens in/out per second (the unit of cost), decode speed (tokens/s),
  time per phase (load / prefill / decode), output tokens per request.
- **USE for resources** (Utilization, Saturation, Errors): GPU utilization, GPU memory (the
  spill-to-CPU early warning), power/temperature, CPU and memory.

Here: 0.7 req/s, P50 0.78 s, decode 124 tokens/s, GPU peaked at 91% utilization and 175 W.

### Q18. Why are P95/P99 more useful than the average?
**Answer:** The average hides the slow tail that real users hit. One request in 100 taking 30 s
barely moves the average but means P99 = 30 s; a user making 20 calls a session will very likely
hit it. Latency distributions are skewed (long answers, cold model loads, queueing), so you set
SLOs on percentiles ("P95 < 2 s"), not means.

### Q19. Histogram vs summary in Prometheus — and why did our P95 come out wrong?
**Answer:** A **histogram** counts observations into fixed buckets; percentiles are *estimated*
at query time with `histogram_quantile`, and buckets from many replicas can be summed — so you
can get a fleet-wide P95. A **summary** computes exact quantiles inside each process, but they
**can't be aggregated** across replicas (averaging P95s is meaningless). Histograms are the usual
choice.
**The catch, measured here:** the client saw a true P95 of **1.37 s**, but Prometheus reported
**2.07 s**. Most requests fell between the 1 s and 2.5 s bucket boundaries, and
`histogram_quantile` assumes values are spread evenly *within* a bucket, so it interpolated
upward. Fix: put bucket boundaries where your SLO and typical latencies are (e.g. add 1.25,
1.5, 2), or use Prometheus native histograms. Say this in an interview — it shows you've
actually read your own graphs.

### Q20. Why label by route pattern and not by URL path?
**Answer:** Every unique label combination is a separate time series held in Prometheus memory.
Labelling `/users/123`, `/users/124`… creates a series per ID — **cardinality explosion** — which
slows queries and can take Prometheus down. We use Go's `r.Pattern`
(`POST /v1/chat/completions`), and `unmatched` for 404s so random scanner URLs can't create
series. Same rule for user IDs, prompts or request IDs: those go in logs or traces, never in metric labels.

### Q21. What's TTFB, and why does it equal total latency here?
**Answer:** Time to first byte: from request start until the first byte of the response. Without
streaming, the gateway writes nothing until Ollama has finished the whole answer, so TTFB ≈ total
latency (our P95 TTFB ≈ P95 duration). With streaming (step 2) TTFB becomes **time to first
token** — what users feel — and drops to ~prefill time while total latency stays the same. That's
why LLM dashboards track both.

### Q22. The prompt tokens were 8× the completion tokens. Why, and why does it matter?
**Answer:** Measured: 216,807 prompt vs 26,656 completion tokens. Each turn resends the whole
history (Q14), so input grows every turn while answers stay short. It matters because input
tokens cost money on hosted APIs and prefill time locally. Prefill was cheap here (avg 0.095 s vs
0.586 s decode per request) thanks to the GPU and prefix caching, but on long chats it dominates —
the case for trimming, summarizing or caching history.

### Q23. Deploying the gateway caused 35 failed requests in one second. What went wrong?
**Answer:** With a single gateway container, a redeploy stops the old one before the new one
accepts connections. Graceful shutdown only protects requests *already in flight*; new
connections during the gap are refused. Fix: ≥ 2 replicas behind a load balancer, a **rolling
update** (start new → health check passes → shift traffic → stop old), readiness probes so traffic
only reaches ready instances, and client retries with backoff for idempotent calls.

---

## Step 2 — Streaming

Measured here (`qwen3:4b-instruct`, ~250-word answer): streaming first byte **0.06 s** vs
non-streaming **2.8 s**; total time the same (~2.7 s).

### Q24. Why stream LLM responses? Does it make generation faster?
**Answer:** It doesn't make generation faster — total time is unchanged (2.6–2.8 s both ways
here). It makes the *perceived* latency tiny: users see text after time-to-first-token (0.06 s
here, ~45× sooner) and read while the rest is generated. Streaming also lets clients cancel early
and keeps long requests from looking hung behind timeouts.

### Q25. SSE vs WebSockets vs long polling for token streaming?
**Answer:** **SSE** (Server-Sent Events) is plain HTTP: one long response with `data: …\n\n`
events, server → client only. That's exactly the shape of token streaming, it works through
proxies and load balancers, and browsers reconnect automatically. That's why OpenAI and Anthropic
use it. **WebSockets** are bidirectional — worth it for voice or interactive agents sending input
mid-stream, but they need upgrade handling and sticky connections. **Long polling** adds a round
trip per chunk; no reason to use it here.

### Q26. How does your gateway turn Ollama's stream into SSE?
**Answer:** Ollama streams **NDJSON** (one JSON object per line). The gateway reads lines with
`bufio.Scanner`, converts each to an OpenAI chunk (`choices[0].delta.content` — `delta` because
it's only the new piece), writes `data: <json>\n\n` and calls `Flush()` so it leaves the buffer
immediately. The last Ollama line has `done: true` and the token stats: we record metrics, send a
chunk with `finish_reason`, then `data: [DONE]`.

### Q27. What breaks streaming in production?
**Answer:** **Buffering** anywhere on the path: Go's response writer (needs `Flush()`), a
middleware wrapper that hides `http.Flusher` (ours passes `Flush` through on purpose), nginx
(`proxy_buffering off`), CDNs, and compression (gzip buffers). **Timeouts**: idle/read timeouts on
load balancers can cut long streams — send keep-alive comments if needed. **HTTP/1.1 connection
limits** in browsers (6 per host) for many parallel streams.

### Q28. The model fails halfway through a stream. What does the client see?
**Answer:** The `200 OK` and headers are already sent, so you can't change the status code.
The error has to go **in-band**: we send `data: {"error":{"message":…}}` and close *without*
`[DONE]`, so clients can tell a complete answer from a cut-off one. Same if Ollama's connection
drops (scanner error). Client should treat "no `[DONE]`" as a failure and may retry.

### Q29. The user closes the tab mid-answer. What happens to the GPU?
**Answer:** The write to the client fails, `streamChat` returns, and because the Ollama request
was made with `r.Context()`, Go cancels it — Ollama stops generating. Without context
propagation the GPU would keep producing tokens nobody reads, wasting capacity on every abandoned chat.

### Q30. How does streaming change your metrics?
**Answer:** TTFB becomes **time to first token** (our recorder notes the first write; we
deliberately don't flush headers early, so the first write *is* the first token). Total duration
still measures the whole generation. Track both: TTFT for user experience, total duration and
tokens/s for capacity. Also watch in-flight requests — streams hold connections open longer.

---

## Step 3 — Tool use / agent loop

Measured here: "1234 × 5678" → `calculator` → 7,006,652 exact; "time in India" →
`get_current_time` with `Asia/Kolkata`; "UTC time and 17 × 23" → both tools in one round;
streaming without tools invented a date ("April 5, 2025").

### Q31. How does tool calling work? Who executes the tool?
**Answer:** The application sends tool definitions (name, description, JSON schema) with every
request. The model's chat template renders them into the prompt as text; the model, trained on
tool-use examples, may answer with a structured call (`<tool_call>{"name":…,"arguments":…}`),
which the server parses into `tool_calls`. **The application executes the tool**, appends the
result as a `role: "tool"` message, and calls the model again. The model only ever *requests*.

### Q32. Why send tool definitions on every request instead of "preloading" them?
**Answer:** Models are stateless and their weights are frozen — they only know what's in the
current context. Fine-tuning teaches general tool-use *skill*, but specific schemas belong in
context so they can change per request (per-user permissions, only relevant tools). The repeated
cost is handled by **prefix/prompt caching**: identical system prompt + tool block is computed
once and reused.

### Q33. Describe the agent loop and its failure modes.
**Answer:** `ask → if tool_calls: append the assistant message once, run each tool, append one
tool message per call → ask again → repeat until a plain answer`. Failure modes: **infinite
loops** (cap rounds — ours is `maxToolRounds = 5`, observed in the `llm_agent_rounds` histogram),
**duplicated or misordered messages** (append the assistant order once, then results in order),
**tool errors** (return them to the model as text so it can recover or explain, don't 500),
**cost** (every round is another full model call over a growing context).

### Q34. A tool takes a URL from the model. What can go wrong?
**Answer:** **SSRF** — the model (steered by a user prompt) makes your server request internal
addresses: other containers (`lb:11434` = Ollama here), admin panels, cloud metadata
(`169.254.169.254`, which can leak credentials). Defenses: http(s) only; resolve the host
**yourself** and reject loopback/private/link-local/CGNAT IPs **before connecting** (checking
the hostname string isn't enough — DNS can point anywhere, and redirects dial again); no proxy;
timeouts; response size caps; content-type allowlist. Tested here: the model happily called
`fetch_url` on `http://lb:11434/api/tags`; the gateway blocked it. **Never rely on the model to refuse.**

### Q35. How do you design safe tool arguments?
**Answer:** Treat them as untrusted user input. Typed parameters with a JSON schema, an
allowlist for enums (`op ∈ add/subtract/multiply/divide`), validation with clear errors, no
`eval` of model-written expressions, no shell commands built from arguments (our first draft
used `os/exec` on a model-supplied string — remote code execution). Go detail: JSON numbers decode
to `float64` in `map[string]any`, so type-assert accordingly.

### Q36. Why did the model invent a date, and how do tools fix it?
**Answer:** Asked for something not in its training data or context, a model may **hallucinate**
a plausible answer (here "April 5, 2025" in the streaming path, which had no tools). Tools and
retrieval **ground** answers in real data; the same prompt via the tool-enabled path returned the
real time. Also: return tool failures as clear text — the model then reports "blocked" or
"HTTP 403" honestly instead of guessing.

### Q37. An agent needs data from a website that blocks bots. What do you do?
**Answer:** Prefer the site's **official API** over scraping — stable, structured, permitted.
Stack Overflow pages return a Cloudflare challenge (403) to plain HTTP clients; the Stack
Exchange API returns the same answers as JSON and worked through `fetch_url`. Don't bypass bot
protection. Better still: a dedicated tool per source (cleaner output, fewer tokens) or a search tool.

### Q38. What would you monitor for tools?
**Answer:** Calls per tool and outcome (`llm_tool_calls_total{tool,outcome}`), tool latency
(`llm_tool_duration_seconds`), rounds per request (`llm_agent_rounds`). Label tools by
**registered name only** — map anything the model invents to `"unknown"`, or a misbehaving
model creates unbounded time series (cardinality explosion driven by model output).

---

## System design prompts to practise
You'll be able to answer these by the end of the roadmap:
- Design an LLM gateway for 1,000 internal users (→ steps 0, 2, 7, 8)
- Design a "chat with our company docs" system (→ step 5, RAG)
- Design an agent that can search the web and run code safely (→ steps 3–4)
- Deploy an LLM for a customer on-prem with strict data rules (→ Q10, step 8)
