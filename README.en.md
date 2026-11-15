# LLM Workbench

English · [中文](README.md)

> A local-first toolkit for controlling, inspecting and testing LLM inference.

LLM Workbench is a local-first LLM proxy and experiment workbench. Ollama is the first-class provider.

The proxy started life as `ollama-nothink-proxy`, and its default behavior is unchanged:

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
./llm-workbench                    # background, open the console
./llm-workbench -f --no-browser    # foreground
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

Defaults: listen on `127.0.0.1:11500`, upstream `http://127.0.0.1:11434`.

## Migrating from ollama-nothink-proxy

v0.3.0 renames the project. API paths, CLI flags, config keys and default proxy behavior are unchanged; the binary name, module path, config/log directories and LaunchAgent label changed.

Run this once:

```bash
llm-workbench migrate
```

It copies the old config to the new location, keeps the old file, reports a legacy LaunchAgent (without touching launchd), and is safe to run repeatedly. Starting without `migrate` also works: when the new config is missing, the old file is read as a fallback, and the first save writes the new path.

| Item | Old | New |
| --- | --- | --- |
| Binary | `ollama-nothink-proxy` | `llm-workbench` |
| Module | `github.com/jachy-h/ollama-nothink-proxy` | `github.com/jachy-h/llm-workbench` |
| macOS config | `~/Library/Application Support/ollama-nothink-proxy/config.json` | `~/Library/Application Support/llm-workbench/config.json` |
| macOS log | `~/Library/Logs/ollama-nothink-proxy.log` | `~/Library/Logs/llm-workbench.log` |
| LaunchAgent | `com.github.ollama-nothink-proxy` | `com.github.llm-workbench` |

`ONP_LANG` remains a compatibility alias for `LLM_WORKBENCH_LANG`.

## Console

The old console is still at <http://127.0.0.1:11500/__portal> (loopback only): upstream, timeout, injected params, language, macOS autostart and the last 100 request log lines. `/__portal` stays through v1.0.

## Inspector (request history)

Open <http://127.0.0.1:11500/__workbench/>:

- list: time, model, endpoint, status, latency, TTFT, tokens, stream;
- detail: original request, forwarded request, raw response, error, system prompt;
- filters: source, provider/model, status, full-text search;
- delete one row or clear by filter;
- export JSON / JSONL / CSV;
- local SQLite, default retention 14 days or 5000 rows.

Privacy defaults:

- the transparent proxy records **metadata only**; request/response bodies are not persisted;
- add `--capture-bodies` to inspect bodies;
- Playground / Experiments calls (Phase 2b+) record bodies by default;
- sensitive JSON keys (`api_key`, `authorization`, `token`, `password`, `secret`, ...) are redacted before storage;
- Authorization / Cookie headers are never stored.

```bash
./llm-workbench --capture-bodies          # record bodies
./llm-workbench --no-inspector            # disable history
./llm-workbench --db /path/workbench.db   # custom database
./llm-workbench --retention-days 7 --max-rows 2000
```

```text
macOS:   ~/Library/Application Support/llm-workbench/workbench.db
Linux:   ~/.local/share/llm-workbench/workbench.db
Windows: %LOCALAPPDATA%\llm-workbench\workbench.db
```

## Playground

Switch to **Playground** in the top bar, or open <http://127.0.0.1:11500/__workbench/>:

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

Every playground call is recorded in Inspector (`source=playground`) and records its bodies by default, even when the transparent proxy has `capture_bodies=false`. `--no-inspector` disables it.

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
  > startup profile / global params
      (CLI --set > PROXY_PARAMS > config file params > built-in defaults)
```

`--set` overlays the startup profile; a request-header profile is an independent snapshot and does not inherit global `--set`. An explicit client field always wins.

Built-in params: `temperature=0.2`, `top_p=0.9`, `max_tokens=96`, `keep_alive="30m"`.

### Thinking

| Mode | `/v1/chat/completions` | `/api/chat` |
| --- | --- | --- |
| `off` (default) | force `reasoning_effort:"none"`, remove `think` | force `think:false`, remove `reasoning_effort` |
| `on` | stop forcing `none`; keep the client's level | force `think:true` |
| `passthrough` | leave both fields alone | leave both fields alone |

```bash
llm-workbench --thinking passthrough
```

### System Prompt

| Mode | Behavior |
| --- | --- |
| `off` (default) | leave messages alone |
| `inject-if-missing` | prepend a system message only when none exists |
| `override` | remove existing system/developer messages and replace them |

```bash
llm-workbench --system-prompt "You are a terse assistant."
llm-workbench --system-prompt-file ./prompt.txt
llm-workbench --system-prompt "..." --system-prompt-override
```

### Profiles

A profile is a reusable bundle of thinking / system prompt / params. Built-ins:

- `nothink`: the historical default;
- `raw`: no params, thinking passthrough;
- `thinking`: thinking on, no params.

```bash
llm-workbench --profile raw
```

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
  "storage": {"path": "/Users/you/Library/Application Support/llm-workbench/workbench.db"},
  "lang": "en"
}
```

Paths:

```text
macOS:        ~/Library/Application Support/llm-workbench/config.json
other systems: ~/.config/llm-workbench/config.json
```

Only `host` / `port` need `./llm-workbench restart`; everything else is live.

## CLI

```text
llm-workbench [<command>] [<options>]
```

| Command | Behavior |
| --- | --- |
| (none) / `serve` / `start` | start the proxy; background by default and opens the console, `-f` for foreground |
| `restart` | stop the running instance and start again with these flags |
| `migrate` | copy the old `ollama-nothink-proxy` config |

| Flag / variable | Config key | Default |
| --- | --- | --- |
| `--host` | `host` | `127.0.0.1` |
| `--port`, `PORT` | `port` | `11500` |
| `--upstream`, `UPSTREAM` | `upstream` | `http://127.0.0.1:11434` |
| `--timeout` | `timeout` | `600` seconds |
| `--set KEY=VALUE` (repeatable) | `params` | see above |
| `PROXY_PARAMS` | `params` | — |
| `--no-defaults` | — | `false` |
| `--thinking` | `thinking` | `off` |
| `--system-prompt` / `--system-prompt-file` | `system_prompt` | off |
| `--system-prompt-override` | `system_prompt.mode` | `false` |
| `--profile` | `profile` | — |
| `--no-inspector` | `inspector.enabled` | `false` (history is on by default) |
| `--capture-bodies` | `inspector.capture_bodies` | `false` |
| `--db` | `storage.path` | platform path |
| `--retention-days` | `inspector.retention_days` | `14` |
| `--max-rows` | `inspector.max_rows` | `5000` |
| `--max-concurrent` | — | `8` |
| `--max-body-size` | — | `1048576` |
| `--lang`, `LLM_WORKBENCH_LANG`, `ONP_LANG` | `lang` | `auto` |
| `--config` / `--log` | — | platform paths |
| `-v`, `-f`, `--no-browser`, `--version` | — | — |

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
- `internal/workbench`: `/__workbench` API, Playground / Experiments and React UI;
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

- **Phase 1**: rename/migration, policy, Ollama provider, engine.
- **Phase 2a**: SQLite + Inspector (history, details, export, redaction).
- **Phase 2b**: Playground (streaming, system prompt, params, raw request/response).
- **Phase 3 (current)**: Experiments (prompt A/B, multi-model, parameter comparison, batches, ratings).
- **Phase 4**: more providers, record/replay, datasets, LLM-as-Judge.

Everything stays local by default: no cloud service, telemetry or remote fonts.
