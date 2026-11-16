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
import SearchSelect from "./components/SearchSelect";
import TokenUsage from "./components/TokenUsage";
import { useI18n } from "./i18n";
import { toModelOptions } from "./modelOptions";
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

type PlaygroundMessage = ChatMessage & { metrics?: Meta };

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
  const { t } = useI18n();
  const [providers, setProviders] = useState<ProviderInfo[]>([]);
  const [providerId, setProviderId] = useState("");
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [modelId, setModelId] = useState("");
  const [system, setSystem] = useState("");
  const [messages, setMessages] = useState<PlaygroundMessage[]>([]);
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

  function buildRequest(nextMessages: PlaygroundMessage[]): ChatRequest {
    const think = thinkMode === "passthrough" ? null : thinkMode === "on";
    return {
      provider: providerId,
      model: modelId,
      system,
      messages: nextMessages.map(({ role, content }) => ({ role, content })),
      params: numericParams(params),
      think,
      stream,
    };
  }

  function appendAssistant(content: string, metrics?: Meta) {
    setMessages((current) => [...current, { role: "assistant", content, metrics }]);
  }

  async function send() {
    if (busy || !modelId || !input.trim()) return;
    const userMessage: PlaygroundMessage = { role: "user", content: input.trim() };
    const nextMessages: PlaygroundMessage[] = [...messages, userMessage];
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
    let requestMeta: Meta = emptyMeta;

    try {
      if (stream) {
        await chatStream(
          request,
          (event) => {
            switch (event.event) {
              case "meta":
                requestMeta = {
                  ...requestMeta,
                  requestId: String(event.data.request_id ?? requestMeta.requestId),
                };
                setMeta(requestMeta);
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
                requestMeta = { ...requestMeta, usage: event.data as ChatResult["usage"] };
                setMeta(requestMeta);
                break;
              case "done":
                requestMeta = {
                  ...requestMeta,
                  requestId: String(event.data.request_id ?? requestMeta.requestId),
                  duration: Number(event.data.duration_ms ?? 0),
                  ttft: Number(event.data.ttft_ms ?? 0),
                  usage: (event.data.usage as ChatResult["usage"]) ?? requestMeta.usage,
                };
                setMeta(requestMeta);
                if (event.data.raw) setRawResponse(JSON.stringify(event.data.raw, null, 2));
                break;
              case "error":
                setError(String(event.data.message ?? t("playground.streamFailed")));
                break;
            }
          },
          controller.signal,
        );
        if (text) appendAssistant(text, requestMeta);
        setOutput("");
      } else {
        const result = await chat(request, controller.signal);
        text = result.text;
        requestMeta = {
          requestId: result.request_id,
          duration: result.duration_ms,
          ttft: result.ttft_ms,
          usage: result.usage,
        };
        setThinking(result.thinking);
        setMeta(requestMeta);
        setRawResponse(JSON.stringify(result.raw ?? result, null, 2));
        if (text) appendAssistant(text, requestMeta);
      }
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") {
        if (text) appendAssistant(text, requestMeta);
        setOutput("");
        setError(t("playground.stopped"));
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
      setError(t("playground.promptNameRequired"));
      return;
    }
    const payload = {
      name,
      system,
      messages: messages.map(({ role, content }) => ({ role, content })),
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
    setMessages(prompt.messages.map(({ role, content }) => ({ role, content })));
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
    if (!window.confirm(t("playground.confirmDeletePrompt", { name: promptName }))) return;
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
      <aside className="panel pg-controls">
        <div className="panel-head">
          <h2>{t("nav.playground")}</h2>
          <span className="aside-note">{t("playground.model")}</span>
        </div>
        <div className="panel-body pg-scroll">
          <section className="control-section">
            <label>
              {t("playground.provider")}
              <select value={providerId} onChange={(event) => setProviderId(event.target.value)}>
                {providers.map((provider) => (
                  <option key={provider.id} value={provider.id}>{provider.name}</option>
                ))}
              </select>
            </label>
            <label>
              {t("playground.modelName")}
              <SearchSelect
                value={modelId}
                options={toModelOptions(models)}
                placeholder={t("playground.modelPlaceholder")}
                onChange={setModelId}
              />
            </label>
            <label className="row">
              <input type="checkbox" checked={stream} onChange={(event) => setStream(event.target.checked)} />
              {t("playground.stream")}
            </label>
            <label>
              {t("playground.thinking")}
              <select value={thinkMode} onChange={(event) => setThinkMode(event.target.value as typeof thinkMode)}>
                <option value="off">{t("playground.thinkingOff")}</option>
                <option value="on">{t("playground.thinkingOn")}</option>
                <option value="passthrough">{t("playground.thinkingPassthrough")}</option>
              </select>
            </label>
          </section>

          <section className="control-section">
            <h3>{t("playground.system")}</h3>
            <textarea
              className="system"
              value={system}
              placeholder={t("playground.systemPlaceholder")}
              onChange={(event) => setSystem(event.target.value)}
            />
          </section>

          <section className="control-section">
            <h3>{t("playground.params")}</h3>
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

          <section className="control-section">
            <h3>{t("playground.prompts")}</h3>
            <input
              placeholder={t("playground.promptName")}
              value={promptName}
              onChange={(event) => setPromptName(event.target.value)}
            />
            <div className="button-row">
              <button onClick={() => void savePrompt(false)} disabled={!promptId}>
                {t("action.update")}
              </button>
              <button onClick={() => void savePrompt(true)}>{t("action.saveNew")}</button>
              <button onClick={() => void removePrompt()} disabled={!promptId}>
                {t("action.delete")}
              </button>
            </div>
            <select value={promptId} onChange={(event) => loadPrompt(event.target.value)}>
              <option value="">{t("playground.loadPrompt")}</option>
              {prompts.map((prompt) => (
                <option key={prompt.id} value={prompt.id}>{prompt.name}</option>
              ))}
            </select>
          </section>
        </div>
      </aside>

      <section className="panel pg-main">
        <div className="pg-head">
          <div>
            <h2>{modelId || t("nav.playground")}</h2>
            <div className="subtle mono">
              {meta.requestId || t("playground.subtitle")}
            </div>
          </div>
          <div className="pg-meta">
            <span>{t("common.ttft")} <strong>{formatDuration(meta.ttft)}</strong></span>
            <span>{t("common.duration")} <strong>{formatDuration(meta.duration)}</strong></span>
            {meta.usage ? (
              <span>
                {t("common.tokens")} <TokenUsage
                  prompt={meta.usage.prompt_tokens}
                  completion={meta.usage.completion_tokens}
                  total={meta.usage.total_tokens}
                />
              </span>
            ) : null}
            {meta.requestId ? (
              <button onClick={() => onOpenRequest(meta.requestId)}>{t("action.inspect")}</button>
            ) : null}
          </div>
        </div>

        {error ? <div className="error">{error}</div> : null}

        <div className="conversation">
          {messages.map((message, index) => (
            <article key={index} className={`bubble ${message.role}`}>
              <div className="bubble-role">{message.role}</div>
              <div className="bubble-text">{message.content}</div>
              {message.metrics ? (
                <div className="bubble-meta">
                  <span>{t("common.ttft")} <strong>{formatDuration(message.metrics.ttft)}</strong></span>
                  <span>{t("common.duration")} <strong>{formatDuration(message.metrics.duration)}</strong></span>
                  {message.metrics.usage ? (
                    <span>
                      {t("common.tokens")} <TokenUsage
                        prompt={message.metrics.usage.prompt_tokens}
                        completion={message.metrics.usage.completion_tokens}
                        total={message.metrics.usage.total_tokens}
                      />
                    </span>
                  ) : null}
                </div>
              ) : null}
            </article>
          ))}
          {thinking ? (
            <details className="thinking" open>
              <summary>{t("playground.thinking")}</summary>
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
              <h2>{t("playground.emptyTitle")}</h2>
              <p>{t("playground.emptyBody")}</p>
            </div>
          ) : null}
        </div>

        <div className="composer">
          <textarea
            value={input}
            placeholder={t("playground.promptPlaceholder")}
            onChange={(event) => setInput(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
                event.preventDefault();
                void send();
              }
            }}
          />
          <div className="composer-actions">
            <button onClick={clear} disabled={busy}>{t("action.clear")}</button>
            {busy
              ? <button onClick={stop}>{t("action.stop")}</button>
              : <button onClick={() => void send()} disabled={!input.trim() || !modelId}>{t("action.send")}</button>}
          </div>
        </div>

        <details className="raw">
          <summary>{t("playground.raw")}</summary>
          <div className="raw-grid">
            <div>
              <h3>{t("playground.request")}</h3>
              <pre>{rawRequest || "—"}</pre>
            </div>
            <div>
              <h3>{t("playground.response")}</h3>
              <pre>{rawResponse || "—"}</pre>
            </div>
          </div>
        </details>
      </section>
    </main>
  );
}
