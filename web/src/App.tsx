import { useEffect, useRef, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { streamChat, type Message } from "./stream";

// The Docker stack only has qwen3:4b-instruct; qwen3:14b exists only on the Windows Ollama
// (run the gateway with `go run ./cmd/server` to use it).
const MODELS = ["qwen3:8b", "qwen3:14b"];

type Turn = Message & {
  ttft?: number; // seconds to the first token
  total?: number; // seconds for the whole answer
  status?: "streaming" | "stopped" | "error" | "incomplete";
  error?: string;
};

export default function App() {
  const [turns, setTurns] = useState<Turn[]>([]);
  const [input, setInput] = useState("");
  const [model, setModel] = useState(MODELS[0]);
  const [busy, setBusy] = useState(false);
  const abortRef = useRef<AbortController | null>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);

  // keep the newest text in view while it streams in
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [turns]);

  // grow the textarea with its content
  useEffect(() => {
    const el = inputRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 240) + "px";
  }, [input]);

  // updateLast patches the assistant turn that is currently streaming
  const updateLast = (patch: (t: Turn) => Partial<Turn>) =>
    setTurns((prev) => {
      const last = prev[prev.length - 1];
      return [...prev.slice(0, -1), { ...last, ...patch(last) }];
    });

  async function send() {
    const text = input.trim();
    if (!text || busy) return;

    // the model is stateless: send the whole conversation every time (failed answers excluded)
    const history: Message[] = [
      ...turns.filter((t) => t.content && t.status !== "error").map(({ role, content }) => ({ role, content })),
      { role: "user", content: text },
    ];

    setInput("");
    setBusy(true);
    setTurns((prev) => [...prev, { role: "user", content: text }, { role: "assistant", content: "", status: "streaming" }]);

    const abort = new AbortController();
    abortRef.current = abort;
    const start = performance.now();
    const elapsed = () => (performance.now() - start) / 1000;
    let sawDone = false;

    try {
      for await (const ev of streamChat(model, history, abort.signal)) {
        if (ev.type === "delta") {
          updateLast((t) => ({ content: t.content + ev.text, ttft: t.ttft ?? elapsed() }));
        } else if (ev.type === "error") {
          updateLast(() => ({ status: "error", error: ev.message }));
          return;
        } else if (ev.type === "done") {
          sawDone = true;
        }
      }
      // no [DONE]: the stream was cut off (gateway restarted, network dropped)
      updateLast(() => ({ status: sawDone ? undefined : "incomplete", total: elapsed() }));
    } catch (err) {
      if (abort.signal.aborted) updateLast(() => ({ status: "stopped", total: elapsed() }));
      else updateLast(() => ({ status: "error", error: String(err) }));
    } finally {
      setBusy(false);
      abortRef.current = null;
      inputRef.current?.focus();
    }
  }

  function newChat() {
    abortRef.current?.abort();
    setTurns([]);
    inputRef.current?.focus();
  }

  return (
    <div className="app">
      <header className="topbar">
        <button className="ghost" onClick={newChat} title="Start a new conversation">
          + New chat
        </button>
        <span className="brand">Gateway Chat</span>
        <select value={model} onChange={(e) => setModel(e.target.value)} disabled={busy} aria-label="Model">
          {MODELS.map((m) => (
            <option key={m}>{m}</option>
          ))}
        </select>
      </header>

      <main className="thread">
        {turns.length === 0 ? (
          <div className="empty">
            <div className="spark" aria-hidden>✻</div>
            <h1>How can I help you today?</h1>
            <p>Answers stream from your local gateway at localhost:8080.</p>
          </div>
        ) : (
          turns.map((t, i) => <Bubble key={i} turn={t} />)
        )}
        <div ref={bottomRef} />
      </main>

      <footer className="composer-wrap">
        <form
          className="composer"
          onSubmit={(e) => {
            e.preventDefault();
            send();
          }}
        >
          <textarea
            ref={inputRef}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                send();
              }
            }}
            placeholder="Message Gateway Chat…  (Enter to send, Shift+Enter for a new line)"
            rows={1}
            autoFocus
          />
          {busy ? (
            <button type="button" className="send stop" onClick={() => abortRef.current?.abort()} title="Stop generating">
              ■
            </button>
          ) : (
            <button type="submit" className="send" disabled={!input.trim()} title="Send">
              ↑
            </button>
          )}
        </form>
        <p className="hint">Local models can be wrong. Streaming requests don't use tools yet.</p>
      </footer>
    </div>
  );
}

function Bubble({ turn }: { turn: Turn }) {
  const [copied, setCopied] = useState(false);

  if (turn.role === "user") {
    return <div className="user-msg">{turn.content}</div>;
  }

  const streaming = turn.status === "streaming";
  return (
    <div className="assistant-msg">
      <div className="markdown">
        {turn.content ? <ReactMarkdown remarkPlugins={[remarkGfm]}>{turn.content}</ReactMarkdown> : null}
        {streaming && <span className={turn.content ? "cursor" : "thinking"} aria-label="generating" />}
      </div>

      {turn.status === "error" && <div className="notice error">⚠ {turn.error}</div>}
      {turn.status === "stopped" && <div className="notice">Stopped.</div>}
      {turn.status === "incomplete" && <div className="notice error">Stream ended early: the answer may be cut off.</div>}

      {!streaming && turn.content && (
        <div className="meta">
          <button
            className="ghost small"
            onClick={() => {
              navigator.clipboard.writeText(turn.content);
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            }}
          >
            {copied ? "Copied" : "Copy"}
          </button>
          {turn.ttft !== undefined && (
            <span title="time to first token · total time">
              first token {turn.ttft.toFixed(2)}s · {turn.total?.toFixed(1)}s
            </span>
          )}
        </div>
      )}
    </div>
  );
}
