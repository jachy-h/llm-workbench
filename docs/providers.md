# Provider 扩展说明

Provider 是工作台平面（Playground / Experiments）与具体模型运行环境之间的适配层。透明代理**不**使用它：代理直接转发原始请求，Provider 只负责语义调用。

## 当前状态

Phase 1 只有一个实现：`internal/provider/ollama`。

```go
type Provider interface {
    ID() string
    Name() string
    Capabilities() Capabilities
    ListModels(ctx context.Context) ([]Model, error)
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest, emit func(StreamEvent) error) (ChatResponse, error)
}
```

设计约束：

- 接口只描述已验证后端需要的能力；
- 不提前实现 llama.cpp / LM Studio / MLX 的空壳；
- 不自动重试；
- 必须尊重 `context` 取消；
- 参数映射从 `internal/policy` 读取，Provider 不维护第二份参数表。

## Ollama 实现

| 能力 | 实现 |
| --- | --- |
| 模型列表 | `GET /api/tags` |
| 非流式 | `POST /api/chat`，`stream:false` |
| 流式 | `POST /api/chat`，`stream:true`，逐帧 NDJSON |
| thinking | `think: true/false`；`nil` 时不发送（passthrough） |
| usage | `prompt_eval_count` / `eval_count` |
| 耗时 | `total_duration`、`load_duration`、`prompt_eval_duration`、`eval_duration` |
| 错误 | 非 2xx 保留状态码和有界 body，返回 `*provider.HTTPError` |
| 取消 | `http.Request` 绑定 ctx；客户端断开即取消上游 |
| 原始响应 | 非流式为完整 body；流式为有界帧数组 |

参数位置由 `policy.NativePlacement` 决定：

- `options.*` 进 `options`；
- `keep_alive`、`format` 留顶层；
- `max_tokens` 映射到 `options.num_predict`；
- `num_predict` 优先于 `max_tokens`。

当前未实现：

- tool calls（接口有 `Capabilities.ToolCalls`，Ollama 实现为 false）；
- 模型 digest 自动查询（`/api/show`，Phase 2 记录实验快照时再加）；
- thinking level（`low/medium/high`）；当前按 bool 处理；
- 多模态 images 已透传，但工作台 UI 尚未使用。

## 添加新 Provider 的检查清单

在真实后端可用之前不要开始。添加时至少确认：

1. `ListModels` 的真实响应；
2. 非流式和流式的 wire format；
3. thinking / reasoning 的控制字段和取值；
4. 采样参数名与位置（不要假设和 Ollama 相同）；
5. usage 是否提供、字段名；
6. 错误状态码和 body；
7. 取消/超时行为；
8. system role 是否支持；
9. 流式结束标记；
10. 参数映射是否复用 `policy`，而不是复制一份。

需要改 `provider.Provider` 接口时，先确认是否真的所有后端都需要；不要为了单一后端扩大接口。

## 超时

`provider.Client(timeout)` 只设置：

- 连接超时（由 Transport 默认）；
- `ResponseHeaderTimeout`；
- 空闲连接超时。

它**不**设置总超时，避免长流被截断。调用方用 `context` 控制总时长。

## 测试

每个 Provider 必须用 `httptest` 假上游覆盖：

- 模型列表；
- 非流式；
- 流式分块、空行、畸形 JSON；
- 上游 4xx/5xx；
- 超时；
- 取消；
- usage；
- thinking 开关。

不要求 CI 连接真实 Ollama；真实后端冒烟测试在本地手动执行。
