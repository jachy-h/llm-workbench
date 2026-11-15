# LLM Workbench

[English](README.md) · 中文

> A local-first toolkit for controlling, inspecting and testing LLM inference.

LLM Workbench 是一个本地优先的 LLM 调用代理与实验工作台，Ollama 是第一优先级 Provider。

当前的代理部分源自 `ollama-nothink-proxy`，默认行为保持不变：

- 对 `/v1/chat/completions` 强制 `reasoning_effort: "none"`，删除 `think`；
- 对 `/api/chat` 强制 `think: false`；
- 只补齐客户端缺失的生成参数，客户端显式传的字段永远优先；
- 其他路径、状态码、流式响应尽量原样透传。

在这个基础上，v0.3.0 增加了可配置的 thinking / System Prompt / profile、Ollama Provider 和语义调用 Engine，为后续 Playground、Inspector、Experiments 打基础。

> 当前阶段：Phase 3。Proxy、Provider、Inspector、Playground、Experiments 已完成；Phase 4 是扩展与稳定化。
> 只监听本机，不做 TLS 与认证，不要暴露到局域网或公网。

## 安装

**推荐：源码构建**（需要 Go 1.24+；重新构建前端需要 Node 22+）

```bash
git clone https://github.com/jachy-h/llm-workbench
cd llm-workbench
make build && ./llm-workbench
```

仓库已提交 `web/dist`，所以只改 Go 代码时 `go build ./cmd/llm-workbench` 也能直接工作；`make build` 会重新构建前端。

**下载预编译产物**：[Releases](https://github.com/jachy-h/llm-workbench/releases)，SHA256 见 `checksums.txt`：

```bash
tar -xzf llm-workbench_0.6.0_darwin_arm64.tar.gz
./llm-workbench
```

macOS 首次运行可能被 Gatekeeper 拦下（产物没有 Apple 签名与公证）。任选一种放行：

- 右键点二进制 → 打开 → 再确认一次「打开」；
- `xattr -d com.apple.quarantine ./llm-workbench`；
- 或改用源码构建，编译产物不带隔离属性。

Windows 同理：SmartScreen 提示时选「更多信息 → 仍要运行」。

## 快速开始

```bash
./llm-workbench                    # 后台运行并打开控制台
./llm-workbench -f --no-browser    # 前台运行
./llm-workbench restart            # 重启当前实例
```

把客户端的 OpenAI 兼容 endpoint 指向代理，其余不用改：

```text
http://127.0.0.1:11500/v1/chat/completions
```

Ollama 原生接口同样可用：

```text
http://127.0.0.1:11500/api/chat
```

默认监听 `127.0.0.1:11500`，默认上游 `http://127.0.0.1:11434`。

## 从 ollama-nothink-proxy 迁移

v0.3.0 完成了产品改名。API、CLI 参数、配置键和默认代理行为不变，变化的是二进制名、module path、配置/日志目录和 LaunchAgent label。

旧版本用户执行一次：

```bash
llm-workbench migrate
```

它会：

- 把旧配置复制到新路径，旧文件保留；
- 检测旧 LaunchAgent，并打印卸载/重装命令；
- 不删除旧日志，不静默操作 launchd；
- 可重复执行，不会覆盖已存在的新配置。

也可以不执行 `migrate` 直接启动：新配置不存在时会只读回退到旧配置，首次保存再写入新路径。

| 项 | 旧 | 新 |
| --- | --- | --- |
| 二进制 | `ollama-nothink-proxy` | `llm-workbench` |
| module | `github.com/jachy-h/ollama-nothink-proxy` | `github.com/jachy-h/llm-workbench` |
| macOS 配置 | `~/Library/Application Support/ollama-nothink-proxy/config.json` | `~/Library/Application Support/llm-workbench/config.json` |
| macOS 日志 | `~/Library/Logs/ollama-nothink-proxy.log` | `~/Library/Logs/llm-workbench.log` |
| LaunchAgent | `com.github.ollama-nothink-proxy` | `com.github.llm-workbench` |

`ONP_LANG` 继续作为 `LLM_WORKBENCH_LANG` 的兼容别名。

## 控制台

旧控制台仍在 <http://127.0.0.1:11500/__portal>（仅本机访问）：

- 上游地址、超时、注入参数、语言；
- macOS 登录自启；
- 最近 100 条请求日志。

旧 `/__portal` 在 v1.0 保留。

## Inspector（请求历史）

打开 <http://127.0.0.1:11500/__workbench/>：

- 请求列表：时间、模型、endpoint、状态、耗时、TTFT、tokens、stream；
- 详情：原始请求、实际转发请求、原始响应、错误、System Prompt；
- 筛选：source、provider/model、status、全文搜索；
- 删除单条 / 按条件清空；
- 导出 JSON / JSONL / CSV；
- 数据保存在本地 SQLite，默认保留 14 天或 5000 条。

隐私默认值：

- 透明代理**只记录元数据**，不落盘请求/响应正文；
- 需要检查正文时加 `--capture-bodies`；
- Playground / Experiments 自己发起的调用（Phase 2b+）默认记录正文；
- 敏感 JSON 键（`api_key`、`authorization`、`token`、`password`、`secret` 等）在落库前脱敏；
- 不记录 Authorization / Cookie 等请求头。

```bash
./llm-workbench --capture-bodies          # 记录正文
./llm-workbench --no-inspector            # 完全关闭历史
./llm-workbench --db /path/workbench.db   # 指定数据库
./llm-workbench --retention-days 7 --max-rows 2000
```

数据库默认路径：

```text
macOS:   ~/Library/Application Support/llm-workbench/workbench.db
Linux:   ~/.local/share/llm-workbench/workbench.db
Windows: %LOCALAPPDATA%\llm-workbench\workbench.db
```

## Playground

同一页面顶部切到 **Playground**，或直接打开 <http://127.0.0.1:11500/__workbench/>：

- Provider / Model 选择；
- System Prompt 编辑器；
- temperature、top_p、top_k、max_tokens、seed 参数；
- thinking：`off` / `on` / `passthrough`；
- 流式 / 非流式；
- 多轮消息、发送、停止、清空；
- TTFT、总耗时、usage；
- 查看实际请求与原始响应；
- 一键跳到 Inspector 查看本次请求；
- 保存 / 更新 / 删除 / 加载 Prompt（SQLite）。

Playground 的每次调用都会写入 Inspector（`source=playground`），并且**默认记录正文**，即使透明代理的 `capture_bodies=false`。如果启动了 `--no-inspector`，则不记录。

## Experiments

顶部切到 **Experiments**：

- 新建实验：name / notes / variants / cases；
- Variant：provider、model、system prompt、params（JSON）；
- Case：name、messages（JSON）；
- 一键运行整个 variant × case 矩阵；
- 进度、停止、失败隔离；
- 结果矩阵：输出、thinking、耗时、TTFT、usage；
- 人工评分 0–5 + 备注；
- 导出 JSON / CSV；
- 从 Inspector 请求详情点 `experiment` 可直接预填一个 case。

实验调用同样写入 Inspector（`source=experiment`），并默认记录正文。

## 配置

优先级：

```text
客户端 body 显式字段
  > 请求级 profile（完整快照，X-Workbench-Profile）
  > 启动时选中的 profile / 全局 params
      （CLI --set > PROXY_PARAMS > 配置文件 params > 内置默认）
```

`--set` 会覆盖启动时选中的 profile 参数；请求头选择的 profile 是一份独立快照，不叠加全局 `--set`。客户端显式字段永远最高。

默认注入参数：`temperature=0.2`、`top_p=0.9`、`max_tokens=96`、`keep_alive="30m"`。

### Thinking

| 模式 | `/v1/chat/completions` | `/api/chat` |
| --- | --- | --- |
| `off`（默认） | 强制 `reasoning_effort:"none"`，删除 `think` | 强制 `think:false`，删除 `reasoning_effort` |
| `on` | 不再强制 `none`，保留客户端 level | 强制 `think:true` |
| `passthrough` | 完全不碰这两个字段 | 完全不碰这两个字段 |

```bash
llm-workbench --thinking passthrough
```

### System Prompt

| 模式 | 行为 |
| --- | --- |
| `off`（默认） | 不碰 messages |
| `inject-if-missing` | 没有 system/developer 消息时插入一条 |
| `override` | 删除已有 system/developer，替换为配置内容 |

```bash
llm-workbench --system-prompt "You are a terse assistant."
llm-workbench --system-prompt-file ./prompt.txt
llm-workbench --system-prompt "..." --system-prompt-override
```

### Profile

Profile 是一组可复用的 thinking / System Prompt / 参数。内置：

- `nothink`：与历史默认行为一致；
- `raw`：不注入参数、thinking 透传；
- `thinking`：thinking 打开、不注入参数。

```bash
llm-workbench --profile raw
```

请求级选择（只对 chat 路径生效，头不会转发给上游）：

```bash
curl http://127.0.0.1:11500/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Workbench-Profile: raw' \
  -d '{"model":"qwen3:8b","messages":[{"role":"user","content":"hi"}]}'
```

也可以临时覆盖 thinking：

```text
X-Workbench-Thinking: off | on | passthrough
```

配置文件示例（由控制台或手工维护）：

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
  "lang": "zh"
}
```

配置文件路径：

```text
macOS:      ~/Library/Application Support/llm-workbench/config.json
其它系统:    ~/.config/llm-workbench/config.json
```

只有 `host` / `port` 改完需要 `./llm-workbench restart`，其他设置即时生效。

## CLI

```text
llm-workbench [<command>] [<options>]
```

| 命令 | 行为 |
| --- | --- |
| （无）/ `serve` / `start` | 启动代理：默认后台运行并打开控制台，`-f` 前台运行 |
| `restart` | 停掉当前实例，再用本命令的参数启动 |
| `migrate` | 从旧 `ollama-nothink-proxy` 配置迁移 |

| 选项 / 变量 | 配置文件键 | 默认值 |
| --- | --- | --- |
| `--host` | `host` | `127.0.0.1` |
| `--port`、`PORT` | `port` | `11500` |
| `--upstream`、`UPSTREAM` | `upstream` | `http://127.0.0.1:11434` |
| `--timeout` | `timeout` | `600` 秒 |
| `--set KEY=VALUE`（可重复） | `params` | 见上 |
| `PROXY_PARAMS` | `params` | — |
| `--no-defaults` | — | `false` |
| `--thinking` | `thinking` | `off` |
| `--system-prompt` / `--system-prompt-file` | `system_prompt` | off |
| `--system-prompt-override` | `system_prompt.mode` | `false` |
| `--profile` | `profile` | — |
| `--no-inspector` | `inspector.enabled` | `false`（即默认开启历史） |
| `--capture-bodies` | `inspector.capture_bodies` | `false` |
| `--db` | `storage.path` | 平台默认路径 |
| `--retention-days` | `inspector.retention_days` | `14` |
| `--max-rows` | `inspector.max_rows` | `5000` |
| `--max-concurrent` | — | `8` |
| `--max-body-size` | — | `1048576` |
| `--lang`、`LLM_WORKBENCH_LANG`、`ONP_LANG` | `lang` | `auto` |
| `--config` / `--log` | — | 平台默认路径 |
| `-v`、`-f`、`--no-browser`、`--version` | — | — |

## 架构

```text
第三方 Agent/SDK ──► /v1/* /api/* ──► transparent proxy ──► Ollama
                                            │
                                      policy（唯一策略源）

Playground/Experiments（Phase 2+）──► engine ──► provider/ollama ──► Ollama
```

- `internal/policy`：参数表、thinking 映射、System Prompt 规则，代理和 Provider 共用；
- `internal/provider/ollama`：模型列表、非流式与 NDJSON 流式、usage、取消；
- `internal/engine`：语义调用编排、请求 ID、TTFT；
- `internal/inspector`：捕获、脱敏、TTFT、异步写入；
- `internal/storage`：SQLite 迁移、查询、保留策略；
- `internal/workbench`：`/__workbench` API、Playground / Experiments 与 React UI；
- `internal/experiments`：variant × case 批量执行、取消、评分；
- `internal/proxy`：兼容平面，只做明确配置的改写。

## 开发

```bash
make test      # go test ./...
make race      # go test -race ./...
make vet       # go vet ./...
make build     # 前端构建 + 单二进制
make web       # 只重建前端
make dist      # 跨平台产物
```

规划与进度：

- `docs/v1.0.0-plan.md`
- `docs/v1.0.0-progress.md`

## 路线图

- **Phase 1**：改名迁移、policy、Ollama Provider、Engine。
- **Phase 2a**：SQLite + Inspector（请求历史、详情、导出、脱敏）。
- **Phase 2b**：Playground（流式输出、System Prompt、参数、原始请求/响应）。
- **Phase 3（当前）**：Experiments（Prompt A/B、多模型、参数对比、批量、评分）。
- **Phase 4**：更多 Provider、录制回放、数据集、LLM-as-Judge。

所有数据默认保存在本地；不引入云服务、遥测或远程字体。
