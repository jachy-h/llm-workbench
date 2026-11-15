export type RequestSummary = {
  id: string;
  created_at: string;
  source: string;
  provider: string;
  model: string;
  endpoint: string;
  method: string;
  stream: boolean;
  status_code?: number;
  error?: string;
  duration_ms: number;
  ttft_ms: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
  client?: string;
  note?: string;
  system_prompt?: string;
  response_truncated: boolean;
};

export type RequestDetail = RequestSummary & {
  request_original?: string;
  request_forwarded?: string;
  response_body?: string;
};

export type RequestList = {
  items: RequestSummary[];
  total: number;
  limit: number;
  offset: number;
};

export type Stats = {
  count: number;
  oldest?: string;
  newest?: string;
  size_bytes: number;
  path: string;
};

export type Health = {
  version: string;
  inspector: {
    enabled: boolean;
    capture_bodies: boolean;
    dropped: number;
    written: number;
  };
  storage: Stats;
};

export type ProviderInfo = {
  id: string;
  name: string;
  default: boolean;
  capabilities: {
    streaming: boolean;
    thinking: "none" | "bool" | "levels";
    usage: boolean;
    system_role: boolean;
    tool_calls: boolean;
    sampling_params?: string[];
  };
};

export type ModelInfo = {
  id: string;
  name: string;
  digest?: string;
  family?: string;
  parameter_size?: string;
  quantization?: string;
  size_bytes?: number;
};

export type ChatMessage = {
  role: "system" | "user" | "assistant";
  content: string;
};

export type ChatRequest = {
  provider: string;
  model: string;
  system: string;
  messages: ChatMessage[];
  params: Record<string, unknown>;
  think: boolean | null;
  stream: boolean;
};

export type ChatResult = {
  request_id: string;
  provider: string;
  model: string;
  text: string;
  thinking: string;
  finish_reason?: string;
  usage?: { prompt_tokens: number; completion_tokens: number; total_tokens: number };
  duration_ms: number;
  ttft_ms: number;
  raw?: unknown;
};

export type Prompt = {
  id: string;
  name: string;
  system: string;
  messages: ChatMessage[];
  params: Record<string, unknown>;
  created_at: string;
  updated_at: string;
};

export type Filters = {
  q: string;
  source: string;
  status: string;
  limit: number;
  offset: number;
};

export type Variant = {
  id: string;
  experiment_id: string;
  provider: string;
  model: string;
  system: string;
  params: Record<string, unknown>;
  sort_order: number;
};

export type ExperimentCase = {
  id: string;
  experiment_id: string;
  name: string;
  messages: ChatMessage[];
  sort_order: number;
};

export type Experiment = {
  id: string;
  name: string;
  notes: string;
  created_at: string;
  updated_at: string;
  variants: Variant[];
  cases: ExperimentCase[];
};

export type ExperimentSummary = {
  id: string;
  name: string;
  notes: string;
  created_at: string;
  updated_at: string;
  variant_count: number;
  case_count: number;
  run_count: number;
};

export type Run = {
  id: string;
  batch_id: string;
  experiment_id: string;
  variant_id: string;
  case_id: string;
  request_id?: string;
  status: "pending" | "running" | "succeeded" | "failed" | "canceled";
  error?: string;
  output?: string;
  thinking?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  duration_ms?: number;
  ttft_ms?: number;
  rating?: number;
  rating_note?: string;
  created_at: string;
  finished_at?: string;
};

export type ExperimentInput = {
  name: string;
  notes: string;
  variants: Array<{ provider: string; model: string; system: string; params: Record<string, unknown> }>;
  cases: Array<{ name: string; messages: ChatMessage[] }>;
};


