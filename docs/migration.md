# 从 ollama-nothink-proxy 迁移

v0.3.0 把项目改名为 **LLM Workbench**。这是一次只影响名称和路径的 breaking change：

- API 路径不变：`/v1/chat/completions`、`/api/chat`、`/__portal`；
- CLI 参数不变，新增 `migrate` 和 thinking / system prompt / profile 相关参数；
- 配置文件键不变，新增 `thinking`、`system_prompt`、`profiles`、`profile`；
- 默认代理行为不变：thinking off、只补缺失参数、非 chat 路径透传。

## 一条命令迁移

```bash
llm-workbench migrate
```

行为：

1. 检测旧配置（macOS `~/Library/Application Support/ollama-nothink-proxy/config.json`，Linux `~/.config/ollama-nothink-proxy/config.json`）。
2. 新配置不存在时复制过去；已存在时不覆盖。
3. 旧配置、旧日志保留，不删除。
4. 检测旧 LaunchAgent `com.github.ollama-nothink-proxy`，打印卸载/重装命令，不静默操作 launchd。
5. 幂等，可重复执行。

指定新路径：

```bash
llm-workbench migrate --config /path/to/config.json
```

## 不执行 migrate 也能启动

如果新配置不存在，代理会**只读**回退到旧配置；第一次保存（控制台或 `Apply`）才写入新路径。这样老用户直接替换二进制不会丢配置。

## 路径变化

| 项 | 旧 | 新 |
| --- | --- | --- |
| 二进制 | `ollama-nothink-proxy` | `llm-workbench` |
| module | `github.com/jachy-h/ollama-nothink-proxy` | `github.com/jachy-h/llm-workbench` |
| macOS 配置 | `~/Library/Application Support/ollama-nothink-proxy/config.json` | `~/Library/Application Support/llm-workbench/config.json` |
| Linux 配置 | `~/.config/ollama-nothink-proxy/config.json` | `~/.config/llm-workbench/config.json` |
| macOS 日志 | `~/Library/Logs/ollama-nothink-proxy.log` | `~/Library/Logs/llm-workbench.log` |
| Linux 日志 | `~/.local/state/ollama-nothink-proxy/...` | `~/.local/state/llm-workbench/...` |
| LaunchAgent | `com.github.ollama-nothink-proxy` | `com.github.llm-workbench` |
| 环境变量 | `ONP_LANG` | `LLM_WORKBENCH_LANG`（`ONP_LANG` 仍兼容） |

## LaunchAgent 手动迁移

```bash
launchctl bootout gui/$(id -u)/com.github.ollama-nothink-proxy
rm ~/Library/LaunchAgents/com.github.ollama-nothink-proxy.plist
# 然后在新控制台里重新打开「开机自动启动」
```

`migrate` 只打印这些命令，不会自动执行。

## 回滚

旧二进制仍可读取旧配置；新配置独立，不会被新版本破坏。回滚步骤：

1. 停掉新实例；
2. 放回旧二进制；
3. 旧配置仍在原路径，可直接使用。

Phase 1 尚未引入 SQLite，因此不存在需要回滚的数据库格式。

## 兼容性说明

- 旧二进制名不再发布；脚本/服务文件里的路径需要改。
- GitHub 仓库改名后旧 URL 会重定向。
- 请求头 `X-Workbench-*` 是新增的可选控制方式；不使用时不改变行为。
