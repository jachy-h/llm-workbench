// The proxy settings page talks to the Go portal API. The React app is served
// by the same binary, so these requests stay on the same origin.

const base = "/_/llm-proxy/api";

export type PortalParams = Record<string, unknown>;

export type PortalInspector = {
  enabled: boolean;
  capture_bodies: boolean;
  max_body_bytes: number;
  retention_days: number;
  max_rows: number;
};

export type PortalStorage = {
  path: string;
};

export type PortalAutostart = {
  supported: boolean;
  label: string;
  plist_path: string;
  installed: boolean;
  loaded: boolean;
};

export type PortalDefaults = {
  host: string;
  port: number;
  upstream: string;
  timeout: number;
  params: PortalParams;
  thinking: string;
  system_prompt: { mode: string; text: string };
  inspector: PortalInspector;
  storage: PortalStorage;
  max_concurrent: number;
  max_body_size: number;
};

export type PortalState = {
  version: string;
  pid: number;
  listen: string;
  upstream: string;
  timeout: number;
  params: PortalParams;
  thinking: string;
  system_prompt: { mode: string; text: string };
  profiles: Record<string, unknown>;
  profile: string;
  inspector: PortalInspector;
  storage: PortalStorage;
  max_concurrent: number;
  max_body_size: number;
  log_path: string;
  log_capacity: number;
  config_path: string;
  launch_ready: boolean;
  stopping: boolean;
  lang: string;
  defaults: PortalDefaults;
  autostart: PortalAutostart;
};

export type PortalLogEntry = {
  seq: number;
  time: string;
  kind: "text" | "life" | "request" | string;
  level: string;
  text: string;
  method?: string;
  path?: string;
  status?: number;
  note?: string;
  client?: string;
};

export type PortalLogResponse = {
  entries: PortalLogEntry[];
  oldest: number;
  latest: number;
  capacity: number;
  poll_ms: number;
  path: string;
  text: string;
};

export type PortalAutostartResponse = PortalState & {
  ok: boolean;
  message: string;
};

export function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(base + path, init);
  let body: unknown = null;
  try {
    body = await response.json();
  } catch {
    // A shutdown may close the connection before it can reply.
  }
  if (!response.ok) {
    const raw = (body as { error?: unknown } | null)?.error;
    const message =
      typeof raw === "string"
        ? raw
        : (raw as { message?: string } | undefined)?.message || `HTTP ${response.status}`;
    throw new Error(message);
  }
  return body as T;
}

export function getPortalState(): Promise<PortalState> {
  return request<PortalState>("/state");
}

export function getPortalPID(): Promise<{ pid: number }> {
  return request<{ pid: number }>("/pid");
}

export function getPortalLog(since: number): Promise<PortalLogResponse> {
  return request<PortalLogResponse>(`/log?since=${since}`);
}

export function savePortalConfig(patch: Record<string, unknown>): Promise<PortalState> {
  return request<PortalState>("/config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
}

export function savePortalLang(lang: "zh" | "en"): Promise<PortalState> {
  return savePortalConfig({ lang });
}

export function togglePortalAutostart(install: boolean): Promise<PortalAutostartResponse> {
  return request<PortalAutostartResponse>("/autostart", {
    method: install ? "POST" : "DELETE",
  });
}

export function shutdownPortal(): Promise<{ ok: boolean; message?: string }> {
  return request<{ ok: boolean; message?: string }>("/shutdown", { method: "POST" });
}
