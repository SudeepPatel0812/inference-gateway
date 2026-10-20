// Reads the gateway's Server-Sent Events stream (POST /v1/chat/completions with "stream": true).
// EventSource can't send a POST body, so we use fetch and read response.body chunk by chunk.

export type Message = { role: "user" | "assistant"; content: string };

export type StreamEvent =
  | { type: "delta"; text: string } // a new piece of the answer
  | { type: "finish"; reason: string } // last chunk: why the model stopped
  | { type: "error"; message: string } // in-band error (the 200 was already sent)
  | { type: "done" }; // data: [DONE]

// SSEParser turns raw text chunks into complete "data:" payloads. Network chunks don't line up
// with events: one read can hold half an event, or three, so we buffer until the blank line.
export class SSEParser {
  private buffer = "";

  push(chunk: string): string[] {
    this.buffer += chunk.replace(/\r\n/g, "\n");
    const events = this.buffer.split("\n\n");
    this.buffer = events.pop() ?? ""; // the last piece may be incomplete: keep it for next time
    return events.flatMap((event) =>
      event
        .split("\n")
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).trimStart()),
    );
  }
}

export function toEvent(data: string): StreamEvent | null {
  if (data === "[DONE]") return { type: "done" };

  let obj: any;
  try {
    obj = JSON.parse(data);
  } catch {
    return null; // ignore a malformed line rather than killing the stream
  }
  if (obj.error) return { type: "error", message: obj.error.message ?? String(obj.error) };

  const choice = obj.choices?.[0];
  if (choice?.finish_reason) return { type: "finish", reason: choice.finish_reason };
  const text = choice?.delta?.content;
  return text ? { type: "delta", text } : null;
}

export async function* streamChat(
  model: string,
  messages: Message[],
  signal: AbortSignal,
): AsyncGenerator<StreamEvent> {
  const res = await fetch("/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ model, messages, stream: true }),
    signal, // aborting closes the connection → the gateway cancels the Ollama call
  });

  if (!res.ok || !res.body) {
    yield { type: "error", message: `HTTP ${res.status}: ${(await res.text()).trim()}` };
    return;
  }

  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  const parser = new SSEParser();
  for (;;) {
    const { value, done } = await reader.read(); // resolves as soon as the next bytes arrive
    if (done) return;
    for (const data of parser.push(value)) {
      const event = toEvent(data);
      if (event) yield event;
    }
  }
}
