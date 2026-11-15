# Playground

Playground 是 `/__workbench` 的调试界面，走语义 Engine，不复用透明代理路径。

## 使用

1. 打开 <http://127.0.0.1:11500/__workbench/>。
2. 顶部切到 **Playground**。
3. 选择 Provider / Model，编辑 System Prompt 和参数。
4. 输入消息，`send`；流式模式下可以 `stop`。

每次调用会写入 Inspector，`source=playground`。

## 能力

- Provider / Model 选择；模型列表来自 `GET /api/providers/{id}/models`，也允许手输模型名。
- System Prompt 编辑器（非空时使用 `override`，替换已有 system/developer 消息）。
- 参数：temperature、top_p、top_k、max_tokens、seed。
- thinking：`off` / `on` / `passthrough`。
- 流式 / 非流式。
- 多轮消息、清空、停止。
- TTFT、总耗时、usage、request ID。
- 实际 Provider 请求与原始响应。
- “inspect” 跳转 Inspector 详情。
- Prompt 保存 / 更新 / 删除 / 加载。

## Thinking 映射

API `POST /api/chat` 的 `think`：

| 请求值 | Engine 模式 | Ollama wire |
| --- | --- | --- |
| `true` | `on` | `think:true` |
| `false` | `off` | `think:false` |
| `null` / 省略 | `passthrough` | 不发送 `think` |

UI 默认 `off`。

## API

```text
GET  /__workbench/api/providers
GET  /__workbench/api/providers/{id}/models
POST /__workbench/api/chat
GET  /__workbench/api/prompts
POST /__workbench/api/prompts
GET  /__workbench/api/prompts/{id}
PUT  /__workbench/api/prompts/{id}
DELETE /__workbench/api/prompts/{id}
```

`POST /api/chat`：

```json
{
  "provider": "ollama",
  "model": "qwen3:8b",
  "system": "Be terse.",
  "messages": [{"role": "user", "content": "hi"}],
  "params": {"temperature": 0.2, "top_k": 40, "max_tokens": 512},
  "think": false,
  "stream": true
}
```

非流式返回 JSON；流式返回 SSE：

```text
event: meta
data: {"request_id":"req_...","provider":"ollama","model":"qwen3:8b","started_at":"..."}

event: delta
data: {"text":"Hello"}

event: thinking
data: {"text":"..."}

event: usage
data: {"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}

event: done
data: {"request_id":"req_...","finish_reason":"stop","duration_ms":1234,"ttft_ms":210,"usage":{...},"raw":[...]}

event: error
data: {"message":"..."}
```

## Prompt

Prompt 保存在同一 SQLite 数据库（migration v2），记录：

- name；
- system prompt；
- messages（JSON 数组）；
- params（JSON 对象）；
- created_at / updated_at。

ID 前缀 `prm_`。只保存文本消息，不保存 images / tool calls。

## Inspector 记录

- `source=playground`；
- `provider` / `model`；
- 原始 API 请求；
- Engine 实际发送给 Provider 的请求（`Prepared`）；
- 原始响应（非流式为 body，流式为帧数组）；
- TTFT、耗时、usage、状态码、错误；
- Playground 单次捕获默认记录正文，即使透明代理 `capture_bodies=false`；
- `--no-inspector` 时完全不记录。

## 限制

- 只有 Ollama Provider；`providers` 配置段尚未解析。
- 与透明代理共享上游并发，没有独立配额。
- 不做上下文长度 / token 预算检查。
- 取消后不会自动重试；已收到的部分文本会保留在 UI。
- 未接真实 Ollama 的自动化冒烟测试。
