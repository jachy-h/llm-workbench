import { useEffect, useRef, useState } from "react";
import {
  chat,
  chatStream,
  createPrompt,
  deletePrompt,
  listModels,
  listPrompts,
  listProviders,
  updatePrompt,
} from "./api";
import type { ChatMessage, ChatRequest, ChatResult, ModelInfo, Prompt, ProviderInfo } from "./types";

type Props = {
  onOpenRequest: (id: string) => void;
};

type Meta = {
  requestId: string;
  duration: number;
  ttft: number;
  usage?: ChatResult["usage"];
};

const emptyMeta: Meta = { requestId: "", duration: 0, ttft: 0 };
const defaultParams: Record<string, string> = {
  temperature: "0.2",
  top_p: "0.9",
  top_k: "40",
  max_tokens: "512",
  seed: "",
};

function formatDuration(ms: number): string {
  if (!ms) return "—";
  if (ms < 1000) return `${ms} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

function numericParams(values: Record<string, string>): Record<string, number> {
  const out: Record<string, number> = {};
  for (const [key, value] of Object.entries(values)) {
    if (value.trim() === "") continue;
    const number = Number(value);
    if (Number.isFinite(number)) out[key] = number;
  }
  return out;
}

export default function PlaygroundPage({ onOpenRequest }: Props) {
  const [providers, setProviders] = useState<ProviderInfo[]>([]);
  const [providerId, setProviderId] = useState("");
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [modelId, setModelId] = useState("");
  const [system, setSystem] = useState("");
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [params, setParams] = useState(defaultParams);
  const [thinkMode, setThinkMode] = useState<"off" | "on" | "passthrough">("off");
  const [stream, setStream] = useState(true);
  const [output, setOutput] = useState("");
  const [thinking, setThinking] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [meta, setMeta] = useState<Meta>(emptyMeta);
  const [rawRequest, setRawRequest] = useState("");
  const [rawResponse, setRawResponse] = useState("");
  const [prompts, setPrompts] = useState<Prompt[]>([]);
  const [promptId, setPromptId] = useState("");
  const [promptName, setPromptName] = useState("");
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    listProviders()
      .then((response) => {
        setProviders(response.items);
        const initial = response.items.find((item) => item.default) ?? response.items[0];
        if (initial) setProviderId(initial.id);
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)));
    void refreshPrompts();
  }, []);

  useEffect(() => () => abortRef.current?.abort(), []);

  useEffect(() => {
    if (!providerId) return;
    setModelId("");
    listModels(providerId)
      .then((items) => {
        setModels(items);
        if (items[0]) setModelId(items[0].id);
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [providerId]);

  async function refreshPrompts() {
    try {
      setPrompts(await listPrompts());
    } catch {
      // prompts are optional; the chat still works without the database
    }
  }

  function buildRequest(nextMessages: ChatMessage[]): ChatRequest {
    const think = thinkMode === "passthrough" ? null : thinkMode === "on";
    return {
      provider: providerId,
      model: modelId,
      system,
      messages: nextMessages,
      params: numericParams(params),
      think,
      stream,
    };
  }

  async function send() {
    if (busy || !modelId || !input.trim()) return;
    const userMessage: ChatMessage = { role: "user", content: input.trim() };
    const nextMessages = [...messages, userMessage];
    const request = buildRequest(nextMessages);
    setMessages(nextMessages);
    setInput("");
    setOutput("");
    setThinking("");
    setMeta(emptyMeta);
    setRawRequest(JSON.stringify(request, null, 2));
    setRawResponse("");
    setError(null);
    setBusy(true);
    const controller = new AbortController();
    abortRef.current = controller;
    let text = "";
    let thinkingText = "";

    try {
      if (stream) {
        await chatStream(
          request,
          (event) => {
            switch (event.event) {
              case "meta":
                setMeta((current) => ({ ...current, requestId: String(event.data.request_id ?? "") }));
                break;
              case "delta":
                text += String(event.data.text ?? "");
                setOutput(text);
                break;
              case "thinking":
                thinkingText += String(event.data.text ?? "");
                setThinking(thinkingText);
                break;
              case "usage":
                setMeta((current) => ({ ...current, usage: event.data as ChatResult["usage"] }));
                break;
              case "done":
                setMeta((current) => ({
                  ...current,
                  requestId: String(event.data.request_id ?? current.requestId),
                  duration: Number(event.data.duration_ms ?? 0),
                  ttft: Number(event.data.ttft_ms ?? 0),
                  usage: (event.data.usage as ChatResult["usage"]) ?? current.usage,
                }));
                if (event.data.raw) setRawResponse(JSON.stringify(event.data.raw, null, 2));
                break;
              case "error":
                setError(String(event.data.message ?? "stream failed"));
                break;
            }
          },
          controller.signal,
        );
        if (text) setMessages((current) => [...current, { role: "assistant", content: text }]);
        setOutput("");
      } else {
        const result = await chat(request, controller.signal);
        text = result.text;
        setThinking(result.thinking);
        setMeta({
          requestId: result.request_id,
          duration: result.duration_ms,
          ttft: result.ttft_ms,
          usage: result.usage,
        });
        setRawResponse(JSON.stringify(result.raw ?? result, null, 2));
        if (text) setMessages((current) => [...current, { role: "assistant", content: text }]);
      }
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") {
        if (text) setMessages((current) => [...current, { role: "assistant", content: text }]);
        setOutput("");
        setError("stopped");
      } else {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setBusy(false);
      abortRef.current = null;
    }
  }

  function stop() {
    abortRef.current?.abort();
  }

  function clear() {
    setMessages([]);
    setInput("");
    setOutput("");
    setThinking("");
    setMeta(emptyMeta);
    setRawRequest("");
    setRawResponse("");
    setError(null);
  }

  async function savePrompt(asNew: boolean) {
    const name = promptName.trim();
    if (!name) {
      setError("prompt name is required");
      return;
    }
    const payload = {
      name,
      system,
      messages,
      params: numericParams(params),
    };
    try {
      const saved = asNew || !promptId
        ? await createPrompt(payload)
        : await updatePrompt(promptId, payload);
      setPromptId(saved.id);
      setPromptName(saved.name);
      await refreshPrompts();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  function loadPrompt(id: string) {
    const prompt = prompts.find((item) => item.id === id);
    if (!prompt) return;
    setPromptId(prompt.id);
    setPromptName(prompt.name);
    setSystem(prompt.system);
    setMessages(prompt.messages);
    const next: Record<string, string> = { ...defaultParams };
    for (const [key, value] of Object.entries(prompt.params ?? {})) {
      next[key] = String(value);
    }
    setParams(next);
    setOutput("");
    setThinking("");
    setMeta(emptyMeta);
  }

  async function removePrompt() {
    if (!promptId) return;
    if (!window.confirm(`Delete prompt ${promptName}?`)) return;
    try {
      await deletePrompt(promptId);
      setPromptId("");
      setPromptName("");
      await refreshPrompts();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <main className="playground">
      <aside className="pg-controls">
        <section>
          <h2>Model</h2>
          <label>
            Provider
            <select value={providerId} onChange={(event) => setProviderId(event.target.value)}>
              {providers.map((provider) => (
                <option key={provider.id} value={provider.id}>{provider.name}</option>
              ))}
            </select>
          </label>
          <label>
            Model
            <input
              list="model-options"
              value={modelId}
              placeholder="model name"
              onChange={(event) => setModelId(event.target.value)}
            />
            <datalist id="model-options">
              {models.map((model) => (
                <option key={model.id} value={model.id}>{model.name}</option>
              ))}
            </datalist>
          </label>
          <label className="row">
            <input type="checkbox" checked={stream} onChange={(event) => setStream(event.target.checked)} />
            stream
          </label>
          <label>
            Thinking
            <select value={thinkMode} onChange={(event) => setThinkMode(event.target.value as typeof thinkMode)}>
              <option value="off">off</option>
              <option value="on">on</option>
              <option value="passthrough">passthrough</option>
            </select>
          </label>
        </section>

        <section>
          <h2>System Prompt</h2>
          <textarea
            className="system"
            value={system}
            placeholder="Optional system prompt"
            onChange={(event) => setSystem(event.target.value)}
          />
        </section>

        <section>
          <h2>Parameters</h2>
          <div className="param-grid">
            {Object.entries(params).map(([key, value]) => (
              <label key={key}>
                {key}
                <input
                  value={value}
                  onChange={(event) => setParams((current) => ({ ...current, [key]: event.target.value }))}
                />
              </label>
            ))}
          </div>
        </section>

        <section>
          <h2>Saved Prompts</h2>
          <input
            placeholder="prompt name"
            value={promptName}
            onChange={(event) => setPromptName(event.target.value)}
          />
          <div className="button-row">
            <button onClick={() => void savePrompt(false)} disabled={!promptId}>
              update
            </button>
            <button onClick={() => void savePrompt(true)}>save new</button>
            <button onClick={() => void removePrompt()} disabled={!promptId}>
              delete
            </button>
          </div>
          <select value={promptId} onChange={(event) => loadPrompt(event.target.value)}>
            <option value="">load a prompt…</option>
            {prompts.map((prompt) => (
              <option key={prompt.id} value={prompt.id}>{prompt.name}</option>
            ))}
          </select>
        </section>
      </aside>

      <section className="pg-main">
        <div className="pg-head">
          <div>
            <h2>{modelId || "Playground"}</h2>
            <div className="subtle">
              {meta.requestId ? <span className="mono">{meta.requestId}</span> : "debug a prompt, not just chat"}
            </div>
          </div>
          <div className="pg-meta">
            <span>ttft <strong>{formatDuration(meta.ttft)}</strong></span>
            <span>total <strong>{formatDuration(meta.duration)}</strong></span>
            {meta.usage ? (
              <span>tokens <strong>{meta.usage.prompt_tokens} / {meta.usage.completion_tokens} / {meta.usage.total_tokens}</strong></span>
            ) : null}
            {meta.requestId ? (
              <button onClick={() => onOpenRequest(meta.requestId)}>inspect</button>
            ) : null}
          </div>
        </div>

        {error ? <div className="error">{error}</div> : null}

        <div className="conversation">
          {messages.map((message, index) => (
            <article key={index} className={`bubble ${message.role}`}>
              <div className="bubble-role">{message.role}</div>
              <div className="bubble-text">{message.content}</div>
            </article>
          ))}
          {thinking ? (
            <details className="thinking" open>
              <summary>thinking</summary>
              <pre>{thinking}</pre>
            </details>
          ) : null}
          {output ? (
            <article className="bubble assistant live">
              <div className="bubble-role">assistant</div>
              <div className="bubble-text">{output}</div>
            </article>
          ) : null}
          {!messages.length && !output && !busy ? (
            <div className="pg-empty">
              <h2>Developer playground</h2>
              <p>Pick a model, edit the system prompt and params, then send. Every call is recorded in Inspector.</p>
            </div>
          ) : null}
        </div>

        <div className="composer">
          <textarea
            value={input}
            placeholder="User message…"
            onChange={(event) => setInput(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
                event.preventDefault();
                void send();
              }
            }}
          />
          <div className="composer-actions">
            <button onClick={clear} disabled={busy}>clear</button>
            {busy ? <button onClick={stop}>stop</button> : <button className="primary" onClick={() => void send()} disabled={!input.trim() || !modelId}>send</button>}
          </div>
        </div>

        <details className="raw">
          <summary>raw request / response</summary>
          <div className="raw-grid">
            <div>
              <h3>request</h3>
              <pre>{rawRequest || "—"}</pre>
            </div>
            <div>
              <h3>response</h3>
              <pre>{rawResponse || "—"}</pre>
            </div>
          </div>
        </details>
      </section>
    </main>
  );
}
