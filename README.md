# ollama-nothink-proxy

让 Ollama 的思考型模型在 **OpenAI 兼容接口**上真正"不思考"，并顺手补上兼容层会丢掉的生成参数。

零第三方依赖，单文件实现，用 [uv](https://github.com/astral-sh/uv) 管理。

## 为什么需要它

Ollama 的 `/v1/chat/completions` 和原生 `/api/chat` 对思考开关的处理不一致，实测（Ollama 0.40.16, qwen3.5:0.8b-mlx）：

| 方式 | 结果 |
| --- | --- |
| `/api/chat` + `"think": false` | ✅ 生效 |
| `/v1` + `"think": false` | ❌ 被静默忽略，仍在思考 |
| `/v1` + `chat_template_kwargs.enable_thinking=false` | ❌ 被忽略 |
| `/v1` + `"reasoning_effort": "none"` | ✅ 唯一有效开关 |
| `Modelfile` 里 `PARAMETER think false` | ❌ `unknown parameter 'think'` |
| 自定义 `TEMPLATE` 去掉思考块 | ❌ 无效，thinking 在 Go 侧 parser 里 |
| `/v1` + `num_ctx` / `num_predict` | ❌ 非标准字段，被丢弃 |

后果很具体：走 `/v1` 的客户端（例如 Easydict 的 Ollama 服务）每句话都要先思考 —— 短词 1s 起步、短句 2~5s；思考还会吃掉 `max_tokens` 预算，导致正文变成空字符串；请求不带 token 上限时甚至能无限思考，把 runner 卡死。

## 实测（M1 16GB，Ollama 0.40.16，qwen3.5:0.8b-mlx）

| 场景 | 直连 `/v1` | 经代理 |
| --- | --- | --- |
| 单词翻译 | 3.6s，且正文是空字符串 | **0.12–0.17s**，正常输出 |
| 短句翻译 | 2–5s | **0.27–0.37s** |
| 不带 token 上限 | 单次请求跑了 **264s**、10666 个 SSE 分块 | 0.26s、10 个分块 |
| 流式首块延迟 | 58ms | 83ms（+25ms 代理开销） |
| 8 条 p50 | — | **167ms** |

## 它做什么

1. 对 chat 请求**强制关思考**：`/v1` 注入 `reasoning_effort: none`，`/api/chat` 注入 `think: false`；
2. 给**缺失**的生成参数填默认值（`temperature` / `top_p` / `max_tokens` / `keep_alive`）；
3. 其余请求（`/v1/models`、`/api/tags`、`/api/show` …）一律原样透传；
4. 响应按块转发，流式不被攒包。

**客户端显式传了的字段永远不会被改写**，所以它和任何 UI 的参数设置共存。

## 用法

```bash
cd ~/work/open-source/ollama-nothink-proxy

# 默认监听 11500，转发到 127.0.0.1:11434
uv run ollama-nothink-proxy

# 换个端口 / 上游
uv run ollama-nothink-proxy --port 11500 --upstream http://127.0.0.1:11434

# 调参：--set 可重复，值按 JSON 解析
uv run ollama-nothink-proxy --set temperature=0 --set max_tokens=128

# 只关思考，不补任何参数
uv run ollama-nothink-proxy --no-defaults

# 看每个请求
uv run ollama-nothink-proxy -v
```

启动后会打印实际生效的注入参数和该填给客户端的 endpoint。

### 接到 Easydict

Easydict 的「Ollama」服务把 endpoint 硬编码成 `http://localhost:11434/v1/chat/completions`，改不了。两条路：

**A. 用「自定义 OpenAI」服务（推荐）**

| 字段 | 值 |
| --- | --- |
| Endpoint | `http://127.0.0.1:11500/v1/chat/completions` |
| API Key | 随便填 |
| 模型 | `qwen3.5:0.8b-mlx` |

**B. 端口互换，Easydict 的 Ollama 服务原样不动**

```bash
launchctl setenv OLLAMA_HOST 127.0.0.1:11435   # 然后重启 Ollama.app
uv run ollama-nothink-proxy --port 11434 --upstream http://127.0.0.1:11435
```

## 参数注入规则

默认值（见 `DEFAULT_PARAMS`）：

```python
{"temperature": 0.2, "top_p": 0.9, "max_tokens": 96, "keep_alive": "30m"}
```

优先级：`默认值 < 环境变量 PROXY_PARAMS < 命令行 --set`，且**客户端显式传值 > 以上全部**。

```bash
PROXY_PARAMS='{"temperature": 0.1, "max_tokens": 64}' uv run ollama-nothink-proxy
PROXY_PARAMS='{}' uv run ollama-nothink-proxy      # 等价 --no-defaults
```

原生 `/api/chat` 上会自动做字段映射：`max_tokens → options.num_predict`、`temperature/top_p → options.*`、`keep_alive → 顶层`。

`num_ctx` 和 `repeat_penalty` 在 `/v1` 层改不了（见上表）。翻译场景用默认 4096 上下文足够；要改这两项只能建一个 Modelfile 衍生模型。

## 登录自启（launchd）

```bash
mkdir -p ~/Library/LaunchAgents
cp contrib/com.github.ollama-nothink-proxy.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.github.ollama-nothink-proxy.plist
launchctl list | grep ollama-nothink-proxy
```

日志在 `~/Library/Logs/ollama-nothink-proxy.log`。plist 里的路径要按你的实际位置改（`uv` 与项目目录）。

## 测试

```bash
uv run python -m unittest discover -s tests -v
uv run ruff check .
```

测试用假上游验证注入逻辑与透传行为，不依赖真实 Ollama。

## 已知边界

- 只做 `http`，不做 TLS/认证 —— 监听 `127.0.0.1`，不要暴露到局域网。
- 只注入已知字段，不做请求体校验以外的任何改写。
- 非 chat 路径完全透传，代理本身不缓存任何东西。
