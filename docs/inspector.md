# Inspector（请求历史）

Inspector 把经过代理的 chat 请求写入本地 SQLite，并在 `/_/workbench` 中展示。它是 Phase 2a 的交付。

## 默认行为

- 记录元数据：请求 ID、时间、method/path、client、状态码、耗时、TTFT、stream、usage、note。
- **不落盘请求/响应正文**（`capture_bodies=false`）。
- 不记录 Authorization / Cookie 等请求头。
- 只捕获 `POST /v1/chat/completions`、`POST /api/chat`，以及 Playground / Experiments 的语义调用；其他代理路径完全透传，不产生记录。
- 捕获异步写入，队列满时丢弃并计数，不阻塞代理。

写入正文：在设置页面打开「记忆正文」。

完全关闭：在设置页面关闭「记录请求历史」。

关闭后不再写入新记录，已有历史仍可通过 `/_/workbench` 查看；只有数据库本身无法打开时，历史接口才返回 503。

## 数据位置

```text
macOS:   ~/.llm-workbench/workbench.db
Linux:   ~/.llm-workbench/workbench.db
Windows: %USERPROFILE%\.llm-workbench\workbench.db
```

自定义：在设置页面填写数据库路径，或用 `--db` 作为启动项覆盖。

SQLite 使用 WAL、`synchronous=NORMAL`、5s busy timeout。写入串行化，避免并发写导致 `SQLITE_BUSY`。

## 记录内容

| 字段 | 说明 |
| --- | --- |
| `id` | `req_...`，代理内部生成；默认不写到响应头 |
| `created_at` | UTC RFC3339Nano |
| `source` | `proxy` / `playground` / `experiment` |
| `provider` / `model` | 从请求体提取；Provider 当前为 `ollama` |
| `endpoint` / `method` | 请求路径与方法 |
| `stream` | 响应是否为 SSE/NDJSON |
| `status_code` / `error` | 上游状态码与代理错误 |
| `duration_ms` / `ttft_ms` | 总耗时与首 token/首字节 |
| `prompt_tokens` / `completion_tokens` / `total_tokens` | OpenAI 或 Ollama 的 usage |
| `system_prompt` | 第一条 system/developer 消息 |
| `request_original` | 客户端原始请求（脱敏、可关闭） |
| `request_forwarded` | policy 改写后实际发往上游的请求（脱敏、可关闭） |
| `response_body` | 上游响应（脱敏、有界、可关闭） |
| `response_truncated` | 正文超过上限 |

上限：

- 单字段默认 256 KiB（`inspector.max_body_bytes`）；
- 超限标记 `response_truncated=true`；
- 流式原始帧也按同一上限保留。

## 脱敏

落库前执行，不是展示层处理：

- JSON 对象键精确匹配：`api_key`、`apikey`、`x_api_key`、`authorization`、`token`、`access_token`、`refresh_token`、`password`、`secret`、`client_secret`、`cookie`、`set_cookie`；
- 值替换为 `"[REDACTED]"`；
- 不误伤 `max_tokens`（只匹配精确键名）；
- 不保存请求头；`RedactURL` 可用于日志/错误中的 query 和 userinfo；
- SSE/NDJSON 响应原样保存（模型输出不做键级脱敏），但不会包含请求头凭据。

## 保留策略

```json
"inspector": {
  "enabled": true,
  "capture_bodies": false,
  "max_body_bytes": 262144,
  "retention_days": 14,
  "max_rows": 5000
}
```

- `retention_days=0`：不按时间删除；
- `max_rows=0`：不按条数删除；
- 启动时和之后每小时清理一次；
- 删除操作：
  - 单条：`DELETE /_/workbench/api/requests/{id}`；
  - 按筛选：`DELETE /_/workbench/api/requests?source=proxy`；
  - 全部：必须显式 `?all=true`。

## API

所有路由仅 loopback，并校验 Host / Origin：

```text
GET    /_/workbench/api/health
GET    /_/workbench/api/stats
GET    /_/workbench/api/requests?limit=&offset=&q=&source=&provider=&model=&status=&since=&until=
GET    /_/workbench/api/requests/{id}
DELETE /_/workbench/api/requests/{id}
DELETE /_/workbench/api/requests?...
GET    /_/workbench/api/requests/export?format=json|jsonl|csv
GET    /_/workbench/api/requests/{id}/export
```

列表默认 100 条，最大 500；导出默认 1000 条，最大 5000。时间筛选使用 RFC3339。

## UI

`/_/workbench/` 是 React + Vite 构建的本地 SPA：

- Requests 列表 + 筛选 + 分页；
- 详情：request / forwarded / response / raw 四个视图；
- 耗时、TTFT、tokens、stream、system prompt；
- 删除、导出；
- health 状态：数据库路径、条数、大小、队列写入/丢弃数。

`web/dist` 提交到仓库，Go 通过 `//go:embed` 打包；`make build` 会重新构建前端。

## 安全

- 仅接受 loopback 客户端；
- 拒绝非本机 Host（DNS rebinding）和非本机 Origin；
- 无认证，属于本机信任模型；
- 不引入云服务或遥测；
- 不要把服务暴露到局域网/公网。
