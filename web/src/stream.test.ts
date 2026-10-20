import { describe, expect, it } from "vitest";
import { SSEParser, toEvent } from "./stream";

describe("SSEParser", () => {
  it("joins events split across network chunks", () => {
    const p = new SSEParser();
    expect(p.push('data: {"a"')).toEqual([]); // half an event: nothing yet
    expect(p.push(':1}\n\ndata: [DO')).toEqual(['{"a":1}']);
    expect(p.push("NE]\n\n")).toEqual(["[DONE]"]);
  });

  it("returns several events from one chunk and handles CRLF", () => {
    const p = new SSEParser();
    expect(p.push("data: x\r\n\r\ndata: y\n\n")).toEqual(["x", "y"]);
  });
});

describe("toEvent", () => {
  it("reads the gateway's chunk shapes", () => {
    expect(toEvent('{"choices":[{"index":0,"delta":{"role":"assistant","content":"Go"},"finish_reason":null}]}'))
      .toEqual({ type: "delta", text: "Go" });
    expect(toEvent('{"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"stop"}]}'))
      .toEqual({ type: "finish", reason: "stop" });
    expect(toEvent('{"error":{"message":"model runner crashed"}}'))
      .toEqual({ type: "error", message: "model runner crashed" });
    expect(toEvent("[DONE]")).toEqual({ type: "done" });
  });

  it("ignores malformed or empty chunks", () => {
    expect(toEvent("not json")).toBeNull();
    expect(toEvent('{"choices":[{"delta":{"content":""}}]}')).toBeNull();
  });
});
