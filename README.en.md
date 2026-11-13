# ollama-nothink-proxy

English · [中文](README.md)

A tiny transparent proxy in front of Ollama that **forces thinking off** and fills in missing generation params. Ollama's `/v1` silently ignores `think: false`, so any OpenAI-compatible client pays for a thinking pass on every request — that is the problem this proxy removes.

No third-party dependencies, one native binary (Go 1.24+). Loopback only: no TLS, no auth — do not expose it to a LAN.

## Install

> Homebrew formula in progress: `brew install jachy-h/tap/ollama-nothink-proxy` (not usable yet).

**Recommended: build from source** (no code-signing or quarantine issues; needs Go 1.24+)

```bash
git clone https://github.com/jachy-h/ollama-nothink-proxy
cd ollama-nothink-proxy
make build && ./ollama-nothink-proxy
```

**Download a prebuilt archive**: [Releases](https://github.com/jachy-h/ollama-nothink-proxy/releases); SHA256 is in `checksums.txt`:

| Platform | Artifact |
| --- | --- |
| macOS Apple Silicon / Intel | `..._darwin_arm64.tar.gz` / `..._darwin_amd64.tar.gz` |
| Windows x64 | `..._windows_amd64.zip` |
| Linux x86_64 / arm64 | `..._linux_amd64.tar.gz` / `..._linux_arm64.tar.gz` |

```bash
tar -xzf ollama-nothink-proxy_0.2.0_darwin_arm64.tar.gz
./ollama-nothink-proxy
```

**On macOS the first run may be blocked by Gatekeeper** — the archive is not signed or notarized by Apple, and a browser download carries `com.apple.quarantine`, so macOS reports "developer cannot be verified". Allow it in one of three ways:

- right-click the binary → Open → confirm "Open" once more;
- `xattr -d com.apple.quarantine ./ollama-nothink-proxy`;
- or use the source build above, which carries no quarantine attribute.

Windows is the same idea: on the SmartScreen prompt choose "More info → Run anyway".

## Usage

```bash
./ollama-nothink-proxy                    # run in the background, open the console
./ollama-nothink-proxy -f --no-browser    # foreground
./ollama-nothink-proxy restart            # restart the running instance
```

Point a client's OpenAI-compatible endpoint at the proxy; nothing else changes:

```text
http://127.0.0.1:11500/v1/chat/completions
```

## Configure it in the console

Open <http://127.0.0.1:11500/__portal> (loopback only). Day-to-day settings live there:

- **Params**: upstream URL, timeout, injected params (JSON) — saved, persisted and live immediately; "Restore defaults" fills the built-in values back in;
- **Startup**: the macOS login-autostart switch (install / remove the LaunchAgent);
- **Live log**: last 100 lines, pausable;

Only `host` / `port` need `./ollama-nothink-proxy restart`. The console maintains the config file, so you rarely edit it by hand:

```text
macOS:        ~/Library/Application Support/ollama-nothink-proxy/config.json
other systems: ~/.config/ollama-nothink-proxy/config.json
```

## CLI Doc

```text
ollama-nothink-proxy [<command>] [<options>]
```

| Command | Behaviour |
| --- | --- |
| (none) / `serve` / `start` | start the proxy: background by default and opens the console, `-f` for foreground |
| `restart` | stop the running instance and start again with these flags; same as a start when nothing runs |

```bash
ollama-nothink-proxy restart --port 11501   # reopen on another port
```

Options (precedence: **CLI > environment > config file > built-in defaults**, and a field the client sends explicitly always beats all of them):

| Flag / variable | Config key | Default | Applies |
| --- | --- | --- | --- |
| `--host` | `host` | `127.0.0.1` | restart |
| `--port`, `PORT` | `port` | `11500` | restart |
| `--upstream`, `UPSTREAM` | `upstream` | `http://127.0.0.1:11434` | live |
| `--timeout` | `timeout` | `600` (seconds) | live |
| `--set KEY=VALUE` (repeatable) | `params` | `temperature=0.2`, `top_p=0.9`, `max_tokens=96`, `keep_alive="30m"` | live |
| `PROXY_PARAMS` | `params` | — | at start |
| `--no-defaults` | — | `false` | at start |
| `--max-concurrent` | — | `8` | restart |
| `--max-body-size` | — | `1048576` | restart |
| `--lang`, `ONP_LANG` | `lang` | `auto` (system locale) | live |
| `--config` | — | see above | at start |
| `--log` | — | `~/Library/Logs/ollama-nothink-proxy.log` | at start |
| `-v`, `-f`, `--no-browser`, `--version` | — | off / background / open / — | at start |
