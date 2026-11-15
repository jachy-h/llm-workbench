# Proxy 配置

本文描述透明代理（兼容平面）的配置、优先级和请求级控制。工作台平面（Playground / Inspector）尚未在 Phase 1 实现。

## 默认行为

- 监听 `127.0.0.1:11500`；
- 上游默认 `http://127.0.0.1:11434`；
- 只改写两个路径：`POST /v1/chat/completions`、`POST /api/chat`；
- 其他路径、方法、query、header、状态码和流式响应原样透传；
- 默认注入 `temperature=0.2`、`top_p=0.9`、`max_tokens=96`、`keep_alive="30m"`；
- 默认 thinking off。

## 优先级

从高到低：

1. 客户端 body 显式字段；
2. 请求级 profile（`X-Workbench-Profile`，完整快照）；
3. 启动时选中的 profile / 全局 params：
   - CLI `--set`
   - 环境变量 `PROXY_PARAMS`
   - 配置文件 `params`
   - 内置默认 profile
4. Provider 自身默认值。

说明：

- `--set` 会覆盖启动时选中的 profile 参数；
- 请求头选择的 profile 是一份独立快照，不叠加全局 `--set`；
- 客户端显式字段永远最高。

thinking 不适用第 1 条：它由模式决定，默认 `off` 会强制关闭。要允许客户端控制，必须使用 `on` / `passthrough` 或对应 profile。

## 参数

`internal/policy` 是唯一参数表。当前支持：

| 参数 | OpenAI 字段 | Ollama 位置 |
| --- | --- | --- |
| `temperature` | `temperature` | `options.temperature` |
| `top_p` | `top_p` | `options.top_p` |
| `top_k` | `top_k` | `options.top_k` |
| `max_tokens` | `max_tokens` | `options.num_predict` |
| `num_predict` | `num_predict` | `options.num_predict` |
| `num_ctx` | `num_ctx` | `options.num_ctx` |
| `num_keep` | `num_keep` | `options.num_keep` |
| `repeat_penalty` | `repeat_penalty` | `options.repeat_penalty` |
| `repeat_last_n` | `repeat_last_n` | `options.repeat_last_n` |
| `min_p` | `min_p` | `options.min_p` |
| `typical_p` | `typical_p` | `options.typical_p` |
| `presence_penalty` | `presence_penalty` | `options.presence_penalty` |
| `frequency_penalty` | `frequency_penalty` | `options.frequency_penalty` |
| `seed` | `seed` | `options.seed` |
| `stop` | `stop` | `options.stop` |
| `keep_alive` | `keep_alive` | 顶层 |
| `format` | `format` | 顶层 |

规则：

- 只补缺失字段，客户端显式值永远不覆盖；
- `max_tokens` 与 `num_predict` 是别名，同时存在时 `num_predict` 优先；
- 未知参数在 Native 路径留在顶层（保持历史行为）；
- 未知字段、未知嵌套结构和高精度数字按 `json.RawMessage` 保留。

## Thinking

| 模式 | `/v1/chat/completions` | `/api/chat` |
| --- | --- | --- |
| `off` | 强制 `reasoning_effort:"none"`，删除 `think` | 强制 `think:false`，删除 `reasoning_effort` |
| `on` | 不再强制 `none`，保留客户端 level，删除 `think` | 强制 `think:true`，删除 `reasoning_effort` |
| `passthrough` | 不改 thinking 字段 | 不改 thinking 字段 |

`on` 的语义是“停止强制关闭”，不是“强制某个 reasoning level”。

## System Prompt

```json
{"mode":"off","text":""}
```

| 模式 | 行为 |
| --- | --- |
| `off` | 不碰 messages |
| `inject-if-missing` | 没有 system/developer 时在最前面插入；已有则保留 |
| `override` | 删除所有 system/developer，替换为配置内容 |

约束：

- `messages` 不是数组时跳过注入，按原样透传；
- 其他消息对象即使包含未知字段（images、tool calls）也原样保留；
- `override` 是破坏性操作，必须显式开启。

## Profile

```json
{
  "profiles": {
    "fast": {
      "thinking": "off",
      "system_prompt": {"mode": "inject-if-missing", "text": "Be terse."},
      "params": {"temperature": 0.1, "top_k": 20}
    }
  },
  "profile": "fast"
}
```

内置 profile：

- `nothink`：thinking off + 默认参数；
- `raw`：thinking passthrough + 不注入参数；
- `thinking`：thinking on + 不注入参数。

配置文件中的 profile 会覆盖同名内置项。`--set` 在选中 profile 时仍会覆盖该 profile 的参数，避免参数被静默忽略。

## 请求级控制头

只在两个 chat 路径上消费，转发前删除：

```text
X-Workbench-Profile: nothink | raw | thinking | <custom>
X-Workbench-Thinking: off | on | passthrough
```

未知 profile / 非法 thinking 返回 400 JSON 错误，不会打到上游。

## 配置文件

路径：

```text
macOS:        ~/Library/Application Support/llm-workbench/config.json
Linux:        $XDG_CONFIG_HOME/llm-workbench/config.json
Windows:      %LOCALAPPDATA%\llm-workbench\config.json
```

示例：

```json
{
  "host": "127.0.0.1",
  "port": 11500,
  "upstream": "http://127.0.0.1:11434",
  "timeout": 600,
  "params": {"temperature": 0.2, "top_p": 0.9, "max_tokens": 96, "keep_alive": "30m"},
  "thinking": "off",
  "system_prompt": {"mode": "off", "text": ""},
  "profiles": {},
  "profile": "",
  "lang": "zh"
}
```

未知的顶层键会被保留，避免降级或旧控制台保存时丢配置。

## 热更新

- `upstream`、`timeout`、`params`、`thinking`、`system_prompt`、`profiles`、`profile`、`lang` 可即时生效；
- `host` / `port` 需要 `llm-workbench restart`；
- 配置文件先原子写入成功，才会切换内存值。

## 安全

- 只监听本机；`/__portal` 仅 loopback；
- 不记录请求 body/header（Phase 1）；
- 新增控制头不会转发给上游；
- 不要把服务暴露到局域网或公网。
