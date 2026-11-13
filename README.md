# ollama-nothink-proxy

[English](README.en.md) · 中文

Ollama 前面的透明小代理：**强制关闭思考**、补齐缺失的生成参数。Ollama 的 `/v1` 会静默忽略 `think: false`，走 OpenAI 兼容接口的客户端因此每句话都要先思考——这个代理解决的就是这件事。

零第三方依赖，单个二进制（Go 1.24+）。只监听本机，不做 TLS 与认证，别暴露到局域网。

## 安装

> Homebrew 制作中：`brew install jachy-h/tap/ollama-nothink-proxy`（暂不可用）。

**推荐：源码构建**（不会有签名 / 隔离属性问题，需要 Go 1.24+）

```bash
git clone https://github.com/jachy-h/ollama-nothink-proxy
cd ollama-nothink-proxy
make build && ./ollama-nothink-proxy
```

**下载预编译产物**：[Releases](https://github.com/jachy-h/ollama-nothink-proxy/releases)，SHA256 见 `checksums.txt`：

| 平台 | 产物 |
| --- | --- |
| macOS Apple Silicon / Intel | `..._darwin_arm64.tar.gz` / `..._darwin_amd64.tar.gz` |
| Windows x64 | `..._windows_amd64.zip` |
| Linux x86_64 / arm64 | `..._linux_amd64.tar.gz` / `..._linux_arm64.tar.gz` |

```bash
tar -xzf ollama-nothink-proxy_0.2.0_darwin_arm64.tar.gz
./ollama-nothink-proxy
```

**macOS 首次运行可能被 Gatekeeper 拦下**（产物没有 Apple 签名与公证，浏览器下载的包带 `com.apple.quarantine`，会提示「无法验证开发者」）。任选一种放行：

- 右键点二进制 → 打开 → 再确认一次「打开」；
- `xattr -d com.apple.quarantine ./ollama-nothink-proxy`；
- 或改用上面的源码构建，编译产物不带隔离属性。

Windows 同理：SmartScreen 提示时选「更多信息 → 仍要运行」。

## 用法

```bash
./ollama-nothink-proxy                    # 后台运行并打开控制台
./ollama-nothink-proxy -f --no-browser    # 前台运行
./ollama-nothink-proxy restart            # 重启当前实例
```

把客户端的 OpenAI 兼容 endpoint 指向代理即可，其余不用改：

```text
http://127.0.0.1:11500/v1/chat/completions
```

## 配置：用控制台

启动后打开 <http://127.0.0.1:11500/__portal>（只允许本机访问），日常配置都在这里：

- **参数**：上游地址、超时、注入参数（JSON），保存即生效并写入配置文件，「恢复默认」填回内置值；
- **启动项**：macOS 登录自启开关（安装 / 删除 LaunchAgent）；
- **实时日志**：最近 100 条，可暂停；

只有 `host` / `port` 改完需要 `./ollama-nothink-proxy restart`。配置文件由控制台维护，一般不用手改：

```text
macOS:      ~/Library/Application Support/ollama-nothink-proxy/config.json
其它系统:    ~/.config/ollama-nothink-proxy/config.json
```

## CLI Doc

```text
ollama-nothink-proxy [<command>] [<options>]
```

| 命令 | 行为 |
| --- | --- |
| （无）/ `serve` / `start` | 启动代理：默认后台运行并打开控制台，`-f` 前台运行 |
| `restart` | 停掉当前实例，再用本命令的参数启动；没有实例时等同启动 |

```bash
ollama-nothink-proxy restart --port 11501   # 换端口重开
```

选项（优先级：**CLI > 环境变量 > 配置文件 > 内置默认值**；客户端显式传的字段永远优先于以上全部）：

| 选项 / 变量 | 配置文件键 | 默认值 | 何时生效 |
| --- | --- | --- | --- |
| `--host` | `host` | `127.0.0.1` | 需重启 |
| `--port`、`PORT` | `port` | `11500` | 需重启 |
| `--upstream`、`UPSTREAM` | `upstream` | `http://127.0.0.1:11434` | 即时 |
| `--timeout` | `timeout` | `600`（秒） | 即时 |
| `--set KEY=VALUE`（可重复） | `params` | `temperature=0.2`、`top_p=0.9`、`max_tokens=96`、`keep_alive="30m"` | 即时 |
| `PROXY_PARAMS` | `params` | — | 启动时 |
| `--no-defaults` | — | `false` | 启动时 |
| `--max-concurrent` | — | `8` | 需重启 |
| `--max-body-size` | — | `1048576` | 需重启 |
| `--lang`、`ONP_LANG` | `lang` | `auto`（跟随系统） | 即时 |
| `--config` | — | 见上文 | 启动时 |
| `--log` | — | `~/Library/Logs/ollama-nothink-proxy.log` | 启动时 |
| `-v`、`-f`、`--no-browser`、`--version` | — | 关闭 / 后台 / 打开 / — | 启动时 |
