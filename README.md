# LLM Workbench

English · [中文](README.zh.md)

> A local-first toolkit for controlling, inspecting and testing LLM inference.

LLM Workbench is a local-first LLM proxy and experiment workbench. Ollama is supported today, and the provider layer is built for more.

The proxy's default behavior:

- `/v1/chat/completions` forces `reasoning_effort: "none"` and removes `think`;
- `/api/chat` forces `think: false`;
- missing generation params are filled in; values the client sends explicitly always win;
- other paths, status codes and streaming responses pass through as untouched as possible.

v0.3.0 adds configurable thinking / system prompts / profiles, an Ollama provider and a semantic call engine — the foundation for Playground, Inspector and Experiments.

> Current stage: Phase 3. Proxy, provider, Inspector, Playground and Experiments are in place; Phase 4 is extension and stabilization.
> Loopback only: no TLS, no auth — do not expose it to a LAN or the public internet.

## Install

**Recommended: build from source** (needs Go 1.24+; rebuilding the frontend needs Node 22+)

```bash
git clone https://github.com/jachy-h/llm-workbench
cd llm-workbench
make build && ./llm-workbench
```

`web/dist` is committed, so `go build ./cmd/llm-workbench` works without Node; `make build` rebuilds the frontend.

**Download a prebuilt archive**: [Releases](https://github.com/jachy-h/llm-workbench/releases); SHA256 is in `checksums.txt`:

```bash
tar -xzf llm-workbench_0.6.0_darwin_arm64.tar.gz
./llm-workbench
```

On macOS the first run may be blocked by Gatekeeper — the archive is not signed or notarized. Either right-click → Open, run `xattr -d com.apple.quarantine ./llm-workbench`, or build from source.

Windows behaves the same way: on the SmartScreen prompt choose "More info → Run anyway".

## Quick start

```bash
./llm-workbench                    # background, open the proxy page
./llm-workbench stop               # stop the running instance
./llm-workbench restart            # restart the running instance
```

Point an OpenAI-compatible client at:

```text
http://127.0.0.1:11500/v1/chat/completions
```

Ollama's native API works too:

```text
http://127.0.0.1:11500/api/chat
```

Defaults: listen on `127.0.0.1:11500`, upstream `http://127.0.0.1:11434`. The listen port is never 11434 — that one belongs to Ollama.

## Proxy

The proxy page is at <http://127.0.0.1:11500/_/llm-proxy> (loopback only): upstream, timeout, injected params, concurrency and body limits, thinking, system prompts, profiles, language, macOS autostart and the last 100 request log lines. The top bar links to Requests, Playground, Experiments and Settings; the language toggle is shared by all pages and remembered in the browser.

Everything except the bootstrap paths (`--host`, `--port`, `--config`, `--log`, `--db`) is configured in the UI: proxy behavior on the proxy page, request history, privacy and storage on the Settings page.

## Inspector (request history)

Open <http://127.0.0.1:11500/_/workbench/>:

- list: time, model, endpoint, status, latency, TTFT, tokens, stream;
- detail: original request, forwarded request, raw response, error, system prompt;
- filters: source, provider/model, status, full-text search;
- delete one row or clear by filter;
- export JSON / JSONL / CSV;
- local SQLite, default retention 14 days or 5000 rows.

Privacy defaults:

- the transparent proxy records **metadata only**; request/response bodies are not persisted;
- turn on **Remember bodies** on the Settings page to inspect bodies;
- Playground / Experiments calls (Phase 2b+) record bodies by default;
- sensitive JSON keys (`api_key`, `authorization`, `token`, `password`, `secret`, ...) are redacted before storage;
- Authorization / Cookie headers are never stored.

History settings (enabled, body capture, body size, retention days, max rows and the database path) are on the Settings page; they take effect after a restart. `--db` is still available as a bootstrap override.

```text
all platforms: ~/.llm-workbench/workbench.db
```

## Playground

Switch to **Playground** in the top bar, or open <http://127.0.0.1:11500/_/workbench/>:

- provider / model selection;
- system prompt editor;
- temperature, top_p, top_k, max_tokens, seed;
- thinking: `off` / `on` / `passthrough`;
- streaming / non-streaming;
- multi-turn messages, send, stop, clear;
- TTFT, total duration, usage;
- view the actual request and raw response;
- jump to Inspector for the same request;
- save / update / delete / load prompts (SQLite).

Every playground call is recorded in Inspector (`source=playground`) and records its bodies by default, even when the transparent proxy has body capture off. Turning history off on the Settings page disables it.

## Experiments

Switch to **Experiments** in the top bar:

- create an experiment with name / notes / variants / cases;
- variant: provider, model, system prompt, params (JSON);
- case: name, messages (JSON);
- run the full variant × case matrix;
- progress, stop, per-run failure isolation;
- results: output, thinking, latency, TTFT, usage;
- manual 0–5 rating + note;
- export JSON / CSV;
- open a proxy request in Inspector and press `experiment` to seed a case.

Experiment calls are recorded in Inspector (`source=experiment`) and record bodies by default.

## Configuration

Precedence:

```text
client body field
  > request profile (full snapshot, X-Workbench-Profile)
  > startup profile / global settings
      (CLI --set > config file > built-in defaults)
```

`--set` overlays the startup profile and the config file; a request-header profile is an independent snapshot and does not inherit global `--set`. An explicit client field always wins.

Built-in params: `temperature=0.2`, `top_p=0.9`, `max_tokens=96`, `keep_alive="30m"`.

### Thinking

| Mode | `/v1/chat/completions` | `/api/chat` |
| --- | --- | --- |
| `off` (default) | force `reasoning_effort:"none"`, remove `think` | force `think:false`, remove `reasoning_effort` |
| `on` | stop forcing `none`; keep the client's level | force `think:true` |
| `passthrough` | leave both fields alone | leave both fields alone |

Set the mode on the proxy page (**Thinking mode**); it applies immediately.

### System Prompt

| Mode | Behavior |
| --- | --- |
| `off` (default) | leave messages alone |
| `inject-if-missing` | prepend a system message only when none exists |
| `override` | remove existing system/developer messages and replace them |

Configure the mode and text on the proxy page; both apply immediately.

### Profiles

A profile is a reusable bundle of thinking / system prompt / params. Built-ins:

- `nothink`: the historical default;
- `raw`: no params, thinking passthrough;
- `thinking`: thinking on, no params.

Edit the profiles JSON and pick a default profile on the proxy page.

Select a profile per request on chat paths (the header is stripped before forwarding):

```bash
curl http://127.0.0.1:11500/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Workbench-Profile: raw' \
  -d '{"model":"qwen3:8b","messages":[{"role":"user","content":"hi"}]}'
```

Temporarily override thinking:

```text
X-Workbench-Thinking: off | on | passthrough
```

Config example:

```json
{
  "host": "127.0.0.1",
  "port": 11500,
  "upstream": "http://127.0.0.1:11434",
  "timeout": 600,
  "params": {
    "temperature": 0.2,
    "top_p": 0.9,
    "max_tokens": 96,
    "keep_alive": "30m"
  },
  "thinking": "off",
  "system_prompt": {"mode": "off", "text": ""},
  "profile": "",
  "profiles": {
    "fast": {
      "thinking": "off",
      "system_prompt": {"mode": "inject-if-missing", "text": "Be terse."},
      "params": {"temperature": 0.1, "top_k": 20}
    }
  },
  "inspector": {
    "enabled": true,
    "capture_bodies": false,
    "max_body_bytes": 262144,
    "retention_days": 14,
    "max_rows": 5000
  },
  "storage": {"path": "/Users/you/.llm-workbench/workbench.db"},
  "max_concurrent": 8,
  "max_body_size": 1048576,
  "lang": "en"
}
```

Paths:

```text
all platforms: ~/.llm-workbench/config.jsonc
log:           ~/.llm-workbench/logs/llm-workbench.log
```

The file is JSONC: `//` and `/* */` comments and trailing commas are accepted, and it is written back as plain JSON. Older releases used `~/.config/llm-workbench/config.jsonc`, `~/Library/Application Support/llm-workbench/config.json` (macOS) or `~/.config/llm-workbench/config.json`, and before the rename `ollama-nothink-proxy`; the first start after upgrading imports the first one it finds, corrects an unusable listen port, moves the old database into `~/.llm-workbench`, and keeps the config original next to it as `config.json.bak`. Old log files are removed.

The listen port needs to be free for the proxy, so it is never 11434: a stored `"port": 11434` (or a port equal to the upstream's) is replaced with 11500 and written back once. An explicit `--port 11434` is corrected the same way and reported at startup.

Upstream, timeout, params, thinking, system prompt, profiles, language and request-history capture apply live. `host`, `port`, the database path, concurrency and body-size limits take effect after `./llm-workbench restart`.

## CLI

```text
llm-workbench [<command>] [<options>]
```

| Command | Behavior |
| --- | --- |
| (none) / `start` | start the proxy; background by default and opens the proxy page |
| `stop` | stop the running instance (idempotent) |
| `restart` | stop the running instance and start again with these flags |

| Flag | Config key | Default |
| --- | --- | --- |
| `--host` | `host` | `127.0.0.1` |
| `--port` | `port` | `11500` |
| `--set KEY=VALUE` (repeatable) | `params` | see above |
| `--config` | — | `~/.llm-workbench/config.jsonc` |
| `--log` | — | `~/.llm-workbench/logs/llm-workbench.log` |
| `--db` | `storage.path` | `~/.llm-workbench/workbench.db` |
| `-v`, `--version` | — | — |

Everything else is configured in the UI. There are no environment-variable settings.

Upgrading from 0.6.0: an already installed LaunchAgent still passes the removed flags, so re-open the autostart switch on the proxy page after upgrading (otherwise launchd restarts a process that exits immediately).

## Architecture

```text
third-party agent/SDK ──► /v1/* /api/* ──► transparent proxy ──► Ollama
                                                │
                                          policy (single source)

Playground/Experiments (Phase 2+) ──► engine ──► provider/ollama ──► Ollama
```

- `internal/policy`: parameter table, thinking mapping, system-prompt rules shared by the proxy and provider;
- `internal/provider/ollama`: model list, non-stream and NDJSON stream, usage, cancellation;
- `internal/engine`: semantic call orchestration, request IDs, TTFT;
- `internal/inspector`: capture, redaction, TTFT, async writes;
- `internal/storage`: SQLite migrations, queries, retention;
- `internal/workbench`: `/_/workbench` API, Playground / Experiments and the shared React UI (also serves the proxy page);
- `internal/portal`: `/_/llm-proxy` settings/log/autostart/shutdown API;
- `internal/experiments`: variant × case batches, cancellation, ratings;
- `internal/proxy`: compatibility plane; only explicit policy rewrites.

## Development

```bash
make test      # go test ./...
make race      # go test -race ./...
make vet       # go vet ./...
make build     # frontend build + single binary
make web       # rebuild the frontend only
make dist      # cross-platform archives
```

Planning and progress:

- `docs/v1.0.0-plan.md`
- `docs/v1.0.0-progress.md`

## Roadmap

- **Phase 1**: proxy foundation, policy, Ollama provider, engine.
- **Phase 2a**: SQLite + Inspector (history, details, export, redaction).
- **Phase 2b**: Playground (streaming, system prompt, params, raw request/response).
- **Phase 3 (current)**: Experiments (prompt A/B, multi-model, parameter comparison, batches, ratings).
- **Phase 4**: more providers, record/replay, datasets, LLM-as-Judge.

Everything stays local by default: no cloud service, telemetry or remote fonts.
