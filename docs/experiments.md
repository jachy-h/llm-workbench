# Experiments

Experiments 是 Phase 3 的交付：在同一组输入上比较多个模型 / Prompt / 参数，记录每次运行并支持人工评分与导出。

## 模型

```text
Experiment
├── Variant   provider + model + system + params
├── Case      name + messages
└── Run       variant × case 的一次执行
```

- 一次 run = 一个 variant × 一个 case。
- 每轮运行生成新的 run 行，不覆盖历史。
- Run 状态：`pending` / `running` / `succeeded` / `failed` / `canceled`。

## 使用

1. 打开 `/__workbench`，切到 **Experiments**。
2. `new` 创建实验：
   - name / notes；
   - variants：provider、model、system、params（JSON）；
   - cases：name、messages（JSON）。
3. `create` 保存。
4. `run` 执行完整矩阵；前端轮询进度。
5. 查看输出、thinking、耗时、TTFT。
6. 人工评分 0–5 + 备注。
7. 导出 JSON / CSV。
8. 从 Inspector 请求详情点 `experiment` 可预填一个 case。

## API

```text
GET    /__workbench/api/experiments
POST   /__workbench/api/experiments
GET    /__workbench/api/experiments/{id}
PUT    /__workbench/api/experiments/{id}
DELETE /__workbench/api/experiments/{id}
POST   /__workbench/api/experiments/{id}/run
POST   /__workbench/api/experiments/{id}/cancel
GET    /__workbench/api/experiments/{id}/runs?batch=
GET    /__workbench/api/experiments/{id}/export?format=json|csv
GET    /__workbench/api/runs/{id}
PATCH  /__workbench/api/runs/{id}
```

创建示例：

```json
{
  "name": "terse vs verbose",
  "notes": "same inputs, two prompts",
  "variants": [
    {"provider": "ollama", "model": "qwen3:8b", "system": "Be terse.", "params": {"temperature": 0.2}},
    {"provider": "ollama", "model": "qwen3:8b", "system": "Explain in detail.", "params": {"temperature": 0.2}}
  ],
  "cases": [
    {"name": "greeting", "messages": [{"role": "user", "content": "hi"}]}
  ]
}
```

运行返回：

```json
{"batch_id": "bat_...", "run_ids": ["run_...", "..."]}
```

评分：

```json
PATCH /__workbench/api/runs/{id}
{"rating": 4, "note": "clear and short"}
```

`rating` 为 `null` 表示清除评分；否则必须 0–5。

## Runner

- 默认 2 并发（`internal/experiments.New(..., workers)`）。
- 后台执行；API 返回 `202`，前端轮询 runs。
- 单个 run 失败不影响其他 run。
- 取消会取消 provider 请求，并把未完成 run 标记 `canceled`。
- 进程退出时 `Shutdown` 取消所有活跃批次。

## Inspector

每个 experiment run 都会写入 Inspector：

- `source=experiment`；
- 原始 variant/case 输入；
- Engine 实际发送的请求；
- 原始响应；
- TTFT、耗时、usage、状态、错误；
- 默认记录正文。

## 限制（有意的范围控制）

- 不做线上流量分流。
- 不做统计显著性。
- 不做 LLM-as-Judge（Phase 4 评估）。
- 不自动重试。
- 没有模型 digest 比较。
- 实验表单使用 JSON 文本框，不是完整可视化编辑器。
