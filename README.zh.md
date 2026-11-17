# LLM Workbench

[English](README.md) · 中文

> A local-first toolkit for controlling, inspecting and testing LLM inference.

LLM Workbench 是一个本地优先的 LLM 调用代理与实验工作台。当前支持 Ollama，Provider 层可继续扩展。

当前的代理默认行为：

- 对 `/v1/chat/completions` 强制 `reasoning_effort: "none"`，删除 `think`；
- 对 `/api/chat` 强制 `think: false`，删除 `reasoning_effort`；
- 生成参数、`messages`、tools 和其他客户端字段全部原样透传，不注入任何默认值；
- 其他路径、状态码、流式响应尽量原样透传。

v0.3.0 增加了可配置的 thinking、Ollama Provider 和语义调用 Engine，为后续 Playground、Inspector、Experiments 打基础。

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
./llm-workbench                    # 后台运行并打开代理页面
./llm-workbench stop               # 停止正在运行的实例
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

默认监听 `127.0.0.1:11500`，默认上游 `http://127.0.0.1:11434`。监听端口不会用 11434：那个端口是 Ollama 的。

## 代理

代理页面在 <http://127.0.0.1:11500/_/llm-proxy>（仅本机访问）：

- 上游地址、超时、并发与请求体上限；
- thinking 模式；
- 语言、macOS 登录自启；
- 最近 100 条请求日志。

请求历史的开关、正文、保留策略与数据库路径在顶部导航的「设置」页面。

除启动项（`--host`、`--port`、`--config`、`--log`、`--db`）外，全部配置都在界面里完成：代理行为在代理页面，请求历史、隐私与存储在设置页面。顶部导航与请求、试验场、实验同级；中英文切换由所有页面共用，并保存在浏览器里。

## Inspector（请求历史）

打开 <http://127.0.0.1:11500/_/workbench/>：

- 请求列表：时间、模型、endpoint、状态、耗时、TTFT、tokens、stream；
- 详情：原始请求、实际转发请求、原始响应、错误、System Prompt；
- 筛选：source、provider/model、status、全文搜索；
- 删除单条 / 按条件清空；
- 导出 JSON / JSONL / CSV；
- 数据保存在本地 SQLite，默认保留 14 天或 5000 条。

隐私默认值：

- 透明代理**只记录元数据**，不落盘请求/响应正文；
- 在设置页面打开「记忆正文」即可检查正文；
- Playground / Experiments 自己发起的调用（Phase 2b+）默认记录正文；
- 敏感 JSON 键（`api_key`、`authorization`、`token`、`password`、`secret` 等）在落库前脱敏；
- 不记录 Authorization / Cookie 等请求头。

历史设置的开关、正文捕获、正文上限、保留天数、条数和数据库路径都在设置页面；改动后重启生效。`--db` 仍可作为启动项覆盖数据库路径。

数据库默认路径：

```text
macOS:   ~/.llm-workbench/workbench.db
Linux:   ~/.llm-workbench/workbench.db
Windows: %USERPROFILE%\.llm-workbench\workbench.db
```

## Playground

同一页面顶部切到 **Playground**，或直接打开 <http://127.0.0.1:11500/_/workbench/>：

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

Playground 的每次调用都会写入 Inspector（`source=playground`），并且**默认记录正文**，即使透明代理关闭了正文捕获。在设置页面关闭历史记录即可禁用。

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

代理是透明的：客户端 body 原样转发，只在两个 chat 路径上应用 thinking 开关。

### Thinking

| 模式 | `/v1/chat/completions` | `/api/chat` |
| --- | --- | --- |
| `off`（默认） | 强制 `reasoning_effort:"none"`，删除 `think` | 强制 `think:false`，删除 `reasoning_effort` |
| `on` | 删除 `think`，保留客户端的 `reasoning_effort` | 强制 `think:true`，删除 `reasoning_effort` |
| `passthrough` | 完全不碰这两个字段 | 完全不碰这两个字段 |

在代理页面选择模式（Thinking 模式），即时生效。

### 透传

`temperature`、`max_tokens`、`keep_alive` 等生成参数不会被注入；System Prompt 不会被插入或替换。旧配置文件里的 `params` 和 `system_prompt` 键会被忽略并作为未知键保留；想清理文件可以手动删除。

### 已移除：Profile

`profiles` / `profile` 配置和 `X-Workbench-Profile` / `X-Workbench-Thinking` 请求头已移除。配置文件里仍有 `profiles` 或 `profile` 会启动失败，并提示删除这些键；在两个 chat 路径上携带这两个头会返回 400 JSON 错误，不会打到上游。thinking 只由全局模式控制。

配置文件示例（由代理页面或手工维护）：

```json
{
  "host": "127.0.0.1",
  "port": 11500,
  "upstream": "http://127.0.0.1:11434",
  "timeout": 600,
  "thinking": "off",
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
  "lang": "zh"
}
```

配置文件路径：

```text
所有系统:  ~/.llm-workbench/config.jsonc
日志:      ~/.llm-workbench/logs/llm-workbench.log
```

文件是 JSONC：允许 `//`、`/* */` 注释和尾逗号，保存时写回纯 JSON。旧版本把它放在 `~/.config/llm-workbench/config.jsonc`、`~/Library/Application Support/llm-workbench/config.json`（macOS）或 `~/.config/llm-workbench/config.json`，改名之前还在 `ollama-nothink-proxy` 目录下；升级后第一次启动会自动导入找到的第一个，顺手修掉不能用的监听端口，把旧数据库移到 `~/.llm-workbench`，并保留配置原文件为 `config.json.bak`。旧日志文件会被删除。

监听端口必须是代理自己能占住的，所以永远不会是 11434：配置里的 `"port": 11434`（或与 upstream 相同的端口）会被改成 11500 并写回一次；显式 `--port 11434` 同样会被纠正，并在启动时提示。

上游、超时、thinking、语言和请求历史捕获即时生效；`host`、`port`、数据库路径、并发和请求体上限在 `./llm-workbench restart` 后生效。

## CLI

```text
llm-workbench [<command>] [<options>]
```

| 命令 | 行为 |
| --- | --- |
| （无）/ `start` | 启动代理：默认后台运行并打开代理页面 |
| `stop` | 停止正在运行的实例（没有实例时也算成功） |
| `restart` | 停掉当前实例，再用本命令的参数启动 |

| 选项 | 配置文件键 | 默认值 |
| --- | --- | --- |
| `--host` | `host` | `127.0.0.1` |
| `--port` | `port` | `11500` |
| `--config` | — | `~/.llm-workbench/config.jsonc` |
| `--log` | — | `~/.llm-workbench/logs/llm-workbench.log` |
| `--db` | `storage.path` | `~/.llm-workbench/workbench.db` |
| `-v`、`--version` | — | — |

其余全部在代理页面配置。没有任何环境变量设置。

从 0.6.0 升级：已经安装的 LaunchAgent 仍会传入已删除的参数，升级后请重新打开一次「开机自动启动」开关，否则 launchd 会反复拉起一个立即退出的进程。

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
- `internal/workbench`：`/_/workbench` API、Playground / Experiments 与其共享的 React UI（代理页也由它提供）；
- `internal/portal`：`/_/llm-proxy` 的配置、日志、自启、关机 API；
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

- **Phase 1**：代理基础、policy、Ollama Provider、Engine。
- **Phase 2a**：SQLite + Inspector（请求历史、详情、导出、脱敏）。
- **Phase 2b**：Playground（流式输出、System Prompt、参数、原始请求/响应）。
- **Phase 3（当前）**：Experiments（Prompt A/B、多模型、参数对比、批量、评分）。
- **Phase 4**：更多 Provider、录制回放、数据集、LLM-as-Judge。

所有数据默认保存在本地；不引入云服务、遥测或远程字体。
