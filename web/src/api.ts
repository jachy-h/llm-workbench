import type {
  ChatRequest,
  ChatResult,
  Experiment,
  ExperimentInput,
  ExperimentSummary,
  Filters,
  Health,
  ModelInfo,
  Prompt,
  ProviderInfo,
  RequestDetail,
  RequestList,
  Run,
} from "./types";

const base = "/__workbench/api";

async function getJSON<T>(path: string): Promise<T> {
  const response = await fetch(base + path, { headers: { Accept: "application/json" } });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      const body = await response.json();
      message = body?.error?.message ?? message;
    } catch {
      // keep the status text
    }
    throw new Error(message);
  }
  return (await response.json()) as T;
}

export function filterQuery(filters: Filters): string {
  const params = new URLSearchParams();
  if (filters.q.trim()) params.set("q", filters.q.trim());
  if (filters.source) params.set("source", filters.source);
  if (filters.status.trim()) params.set("status", filters.status.trim());
  params.set("limit", String(filters.limit));
  params.set("offset", String(filters.offset));
  return params.toString();
}

export function listRequests(filters: Filters): Promise<RequestList> {
  return getJSON<RequestList>(`/requests?${filterQuery(filters)}`);
}

export function getRequest(id: string): Promise<RequestDetail> {
  return getJSON<RequestDetail>(`/requests/${encodeURIComponent(id)}`);
}

export function getHealth(): Promise<Health> {
  return getJSON<Health>("/health");
}

export async function deleteRequest(id: string): Promise<void> {
  const response = await fetch(`${base}/requests/${encodeURIComponent(id)}`, { method: "DELETE" });
  if (!response.ok) throw new Error(`delete failed: ${response.status}`);
}

export function exportURL(filters: Filters, id?: string): string {
  if (id) return `${base}/requests/${encodeURIComponent(id)}/export`;
  return `${base}/requests/export?${filterQuery(filters)}`;
}

export async function listProviders(): Promise<{ items: ProviderInfo[]; default: string }> {
  return getJSON<{ items: ProviderInfo[]; default: string }>("/providers");
}

export async function listModels(provider: string): Promise<ModelInfo[]> {
  const response = await getJSON<{ items: ModelInfo[] }>(`/providers/${encodeURIComponent(provider)}/models`);
  return response.items;
}

export function chat(request: ChatRequest, signal?: AbortSignal): Promise<ChatResult> {
  return fetch(`${base}/chat`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({ ...request, stream: false }),
    signal,
  }).then(async (response) => {
    if (!response.ok) {
      let message = `${response.status} ${response.statusText}`;
      try {
        const body = await response.json();
        message = body?.error?.message ?? message;
      } catch {
        // keep status text
      }
      throw new Error(message);
    }
    return (await response.json()) as ChatResult;
  });
}

export type StreamEvent = {
  event: string;
  data: Record<string, unknown>;
};

export async function chatStream(
  request: ChatRequest,
  onEvent: (event: StreamEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  const response = await fetch(`${base}/chat`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
    body: JSON.stringify({ ...request, stream: true }),
    signal,
  });
  if (!response.ok || !response.body) {
    let message = `${response.status} ${response.statusText}`;
    try {
      const body = await response.json();
      message = body?.error?.message ?? message;
    } catch {
      // keep status text
    }
    throw new Error(message);
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let boundary = buffer.indexOf("\n\n");
    while (boundary >= 0) {
      const block = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const event = parseEvent(block);
      if (event) onEvent(event);
      boundary = buffer.indexOf("\n\n");
    }
  }
  if (buffer.trim()) {
    const event = parseEvent(buffer);
    if (event) onEvent(event);
  }
}

function parseEvent(block: string): StreamEvent | null {
  let event = "message";
  const data: string[] = [];
  for (const line of block.split("\n")) {
    if (line.startsWith("event:")) event = line.slice(6).trim();
    else if (line.startsWith("data:")) data.push(line.slice(5).trim());
  }
  if (!data.length) return null;
  try {
    return { event, data: JSON.parse(data.join("\n")) as Record<string, unknown> };
  } catch {
    return { event, data: { text: data.join("\n") } };
  }
}

export async function listPrompts(): Promise<Prompt[]> {
  const response = await getJSON<{ items: Prompt[] }>("/prompts");
  return response.items;
}

export async function createPrompt(prompt: {
  name: string;
  system: string;
  messages: unknown[];
  params: Record<string, unknown>;
}): Promise<Prompt> {
  const response = await fetch(`${base}/prompts`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(prompt),
  });
  if (!response.ok) throw new Error(await errorMessage(response));
  return (await response.json()) as Prompt;
}

export async function updatePrompt(
  id: string,
  prompt: { name: string; system: string; messages: unknown[]; params: Record<string, unknown> },
): Promise<Prompt> {
  const response = await fetch(`${base}/prompts/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(prompt),
  });
  if (!response.ok) throw new Error(await errorMessage(response));
  return (await response.json()) as Prompt;
}

export async function deletePrompt(id: string): Promise<void> {
  const response = await fetch(`${base}/prompts/${encodeURIComponent(id)}`, { method: "DELETE" });
  if (!response.ok) throw new Error(await errorMessage(response));
}

async function errorMessage(response: Response): Promise<string> {
  try {
    const body = await response.json();
    return body?.error?.message ?? `${response.status} ${response.statusText}`;
  } catch {
    return `${response.status} ${response.statusText}`;
  }
}

export async function listExperiments(): Promise<ExperimentSummary[]> {
  const response = await getJSON<{ items: ExperimentSummary[] }>("/experiments");
  return response.items;
}

export function getExperiment(id: string): Promise<Experiment> {
  return getJSON<Experiment>(`/experiments/${encodeURIComponent(id)}`);
}

export async function createExperiment(input: ExperimentInput): Promise<Experiment> {
  const response = await fetch(`${base}/experiments`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  if (!response.ok) throw new Error(await errorMessage(response));
  return (await response.json()) as Experiment;
}

export async function updateExperiment(id: string, name: string, notes: string): Promise<Experiment> {
  const response = await fetch(`${base}/experiments/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, notes }),
  });
  if (!response.ok) throw new Error(await errorMessage(response));
  return (await response.json()) as Experiment;
}

export async function deleteExperiment(id: string): Promise<void> {
  const response = await fetch(`${base}/experiments/${encodeURIComponent(id)}`, { method: "DELETE" });
  if (!response.ok) throw new Error(await errorMessage(response));
}

export async function runExperiment(id: string): Promise<{ batch_id: string; run_ids: string[] }> {
  const response = await fetch(`${base}/experiments/${encodeURIComponent(id)}/run`, { method: "POST" });
  if (!response.ok) throw new Error(await errorMessage(response));
  return (await response.json()) as { batch_id: string; run_ids: string[] };
}

export async function cancelExperiment(id: string): Promise<void> {
  const response = await fetch(`${base}/experiments/${encodeURIComponent(id)}/cancel`, { method: "POST" });
  if (!response.ok) throw new Error(await errorMessage(response));
}

export async function listRuns(id: string, batch?: string): Promise<{ items: Run[]; active: boolean }> {
  const params = batch ? `?batch=${encodeURIComponent(batch)}` : "";
  return getJSON<{ items: Run[]; active: boolean }>(`/experiments/${encodeURIComponent(id)}/runs${params}`);
}

export async function rateRun(id: string, rating: number | null, note: string): Promise<Run> {
  const response = await fetch(`${base}/runs/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ rating, note }),
  });
  if (!response.ok) throw new Error(await errorMessage(response));
  return (await response.json()) as Run;
}

export function experimentExportURL(id: string, format: "json" | "csv"): string {
  return `${base}/experiments/${encodeURIComponent(id)}/export?format=${format}`;
}
