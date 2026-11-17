# Proxy 配置

本文描述透明代理（兼容平面）的配置、优先级和请求级控制。工作台平面（Playground / Inspector）尚未在 Phase 1 实现。

## 默认行为

- 监听 `127.0.0.1:11500`；
- 上游默认 `http://127.0.0.1:11434`；
- 只改写两个路径：`POST /v1/chat/completions`、`POST /api/chat`；
- 只改写 thinking 相关字段；`messages`、tools、生成参数和其他字段原样透传；
- 不注入 `temperature`、`max_tokens`、`keep_alive` 等默认参数；
- 不会插入或替换 System Prompt；
- 默认 thinking off。

## 透传

代理不维护生成参数表，也不修改 messages。客户端显式传什么就转发什么，只应用 thinking 规则：

- 旧配置里的 `params` / `system_prompt` 会被忽略，并作为未知键继续保留在文件里（避免降级或页面保存时丢失）；
- `policy.Specs` 和 System Prompt 能力仍被 Playground / 实验场的语义引擎使用，但它们不作用于透明代理；
- 客户端字段始终最高，因为代理根本不覆盖它们。

## Thinking

| 模式 | `/v1/chat/completions` | `/api/chat` |
| --- | --- | --- |
| `off`（默认） | 强制 `reasoning_effort:"none"`，删除 `think` | 强制 `think:false`，删除 `reasoning_effort` |
| `on` | 删除 `think`，保留客户端的 `reasoning_effort` | 强制 `think:true`，删除 `reasoning_effort` |
| `passthrough` | 不改 thinking 字段 | 不改 thinking 字段 |

`on` 的语义是“停止强制关闭”，不是“强制某个 reasoning level”。模式在代理页面上配置，即时生效。

代理页面的 Thinking 卡片会按当前模式列出两个路径上具体写入/删除的字段和原因，并明确标注“只改写思考字段”。

## System Prompt（已移除）

全局 System Prompt 注入已从代理移除。`system_prompt` 配置键不再生效；旧配置里的值会被忽略并保留为未知键。实验场的 variant 仍可各自设置 system 消息，那是实验请求自身的字段。

## 已移除的 Profile

`profiles` / `profile` 配置和 `X-Workbench-Profile` / `X-Workbench-Thinking` 请求头已移除：

- 配置文件里出现 `profiles` 或 `profile` 会启动失败，并提示删除这些键；
- 在两个 chat 路径上携带这两个头会返回 400 JSON 错误，不会打到上游；
- thinking 只能由全局 `thinking` 模式控制。

## 配置文件

路径：

```text
所有系统:  ~/.llm-workbench/config.jsonc
日志:      ~/.llm-workbench/logs/llm-workbench.log
```

文件是 JSONC：允许 `//`、`/* */` 注释和尾逗号，保存时写回纯 JSON。旧版本的 `~/.config/llm-workbench/config.jsonc`、`~/Library/Application Support/llm-workbench/config.json`、`~/.config/llm-workbench/config.json` 以及改名前的 `ollama-nothink-proxy/config.json` 会在第一次启动时自动导入，原文件保留为 `*.bak`。旧位置的数据库会移动到 `~/.llm-workbench/workbench.db`，旧日志文件会被删除。

监听端口默认 11500，且永远不会是 11434（Ollama 占用它）：配置里存了 11434 或与 upstream 相同的端口时，会改成 11500 并写回；显式 `--port 11434` 同样会被纠正并提示。

示例：

```json
{
  "host": "127.0.0.1",
  "port": 11500,
  "upstream": "http://127.0.0.1:11434",
  "timeout": 600,
  "thinking": "off",
  "inspector": {"enabled": true, "capture_bodies": false, "max_body_bytes": 262144, "retention_days": 14, "max_rows": 5000},
  "storage": {"path": "~/.llm-workbench/workbench.db"},
  "max_concurrent": 8,
  "max_body_size": 1048576,
  "lang": "zh"
}
```

未知的顶层键会被保留，避免降级或页面保存时丢配置。

## 热更新

- `upstream`、`timeout`、`thinking`、`lang` 可即时生效；
- `host` / `port`、`storage.path`、`max_concurrent`、`max_body_size` 和 inspector 的保留设置需要 `llm-workbench restart`；
- 配置文件先原子写入成功，才会切换内存值。

## 安全

- 只监听本机；`/_/llm-proxy` 仅 loopback；
- 不记录请求 body/header（Phase 1）；
- 已移除的控制头会被拒绝，不会转发给上游；
- 不要把服务暴露到局域网或公网。
