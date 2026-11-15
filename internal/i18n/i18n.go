// Package i18n holds every user-facing string in the proxy — CLI output, the
// banner, log lines, portal messages and the LaunchAgent plist — in Chinese
// and English, plus the process-wide language switch.
//
// The language is one process-wide value: the CLI resolves it once at startup
// (flag > LLM_WORKBENCH_LANG > locale) and the portal may change it at runtime, so the
// current value lives in an atomic instead of a plain variable.
//
// The default is Chinese, so a caller that never touches Set (the package
// tests, for instance) sees the strings the project is written in.
package i18n

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Lang is a supported language.
type Lang string

const (
	// ZH is Simplified Chinese.
	ZH Lang = "zh"
	// EN is English.
	EN Lang = "en"
	// Auto asks the environment to decide (only valid for flags/env values).
	Auto = "auto"
)

// String returns the language tag.
func (l Lang) String() string { return string(l) }

// Message is one translated string. Both fields must be set; a catalog test
// enforces that.
type Message struct {
	ZH string
	EN string
}

var current atomic.Pointer[Lang]

func init() {
	zh := ZH
	current.Store(&zh)
}

// Set switches the process-wide language. Anything other than EN falls back to
// ZH, so a bad value can never leave the process without text.
func Set(l Lang) {
	if l != EN {
		l = ZH
	}
	current.Store(&l)
}

// Current reports the language T formats with.
func Current() Lang {
	if l := current.Load(); l != nil {
		return *l
	}
	return ZH
}

// T renders key in the current language. A missing key returns the key itself,
// which makes a typo immediately visible instead of silently empty.
func T(key string, args ...any) string { return In(Current(), key, args...) }

// In renders key in a specific language.
func In(l Lang, key string, args ...any) string {
	message, ok := catalog[key]
	if !ok {
		return key
	}
	text := message.ZH
	if l == EN {
		text = message.EN
	}
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}

// Parse reads a language tag such as "zh", "en", "zh-CN" or "en_US.UTF-8".
// It reports false for anything it does not recognise, including "".
func Parse(tag string) (Lang, bool) {
	value := strings.ToLower(strings.TrimSpace(tag))
	if value == "" || value == Auto {
		return "", false
	}
	// Strip the modifier (en_US@euro) and the encoding (en_US.UTF-8) first,
	// then keep the language part of the territory tag (en_US -> en).
	if cut, _, found := strings.Cut(value, "@"); found {
		value = cut
	}
	if cut, _, found := strings.Cut(value, "."); found {
		value = cut
	}
	base, _, _ := strings.Cut(value, "-")
	base, _, _ = strings.Cut(base, "_")
	switch base {
	case "zh":
		return ZH, true
	case "en":
		return EN, true
	}
	return "", false
}

// Detect picks a language from LLM_WORKBENCH_LANG first, then its pre-rename
// ONP_LANG alias, then the locale variables, and falls back to Chinese (the
// project's primary language). lookup is os.LookupEnv in production and a
// stub in tests.
func Detect(lookup func(string) (string, bool)) Lang {
	if lookup == nil {
		return ZH
	}
	for _, key := range []string{"LLM_WORKBENCH_LANG", "ONP_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		value, ok := lookup(key)
		if !ok {
			continue
		}
		if lang, ok := Parse(value); ok {
			return lang
		}
	}
	return ZH
}

// catalog is every user-facing string in the proxy. Keys are grouped by the
// package that prints them. Strings that carry data use fmt verbs, so call
// T(key, args...) rather than concatenating.
var catalog = map[string]Message{
	// ---- shared ---------------------------------------------------------
	"common.none":      {ZH: "（无）", EN: "(none)"},
	"common.unlimited": {ZH: "无限制", EN: "unlimited"},
	"common.off":       {ZH: "关闭", EN: "off"},
	"common.on":        {ZH: "开启", EN: "on"},

	// ---- startup banner (cmd/llm-workbench/banner.go) ------------
	"banner.limits":           {ZH: "并发 %d · 请求体 ≤ %s · 上游超时 %s", EN: "concurrency %d · body ≤ %s · upstream timeout %s"},
	"banner.forceOff":         {ZH: "+ 强制关闭思考", EN: "+ thinking forced off"},
	"banner.thinkingOn":       {ZH: "+ 不再强制关闭思考", EN: "+ thinking no longer forced off"},
	"banner.thinkingPass":     {ZH: "+ 思考原样透传", EN: "+ thinking passed through"},
	"banner.verboseNote":      {ZH: "（每个请求打印一行）", EN: " (one line per request)"},
	"banner.hintWhere":        {ZH: "改参数、看日志", EN: "settings & logs"},
	"banner.hintStop":         {ZH: "停止服务", EN: "stop"},
	"banner.statusForeground": {ZH: "前台运行 (pid %d)", EN: "foreground (pid %d)"},
	"banner.statusBackground": {ZH: "后台运行 (pid %d)", EN: "background (pid %d)"},
	"banner.statusReused":     {ZH: "后台运行 · 复用已在运行的实例", EN: "background · reusing the running instance"},
	"banner.statusLaunchd":    {ZH: "后台运行 · launchd 托管 (pid %d)", EN: "background · launchd supervised (pid %d)"},
	"banner.noteLaunchd":      {ZH: "已通过 launchctl 重启，参数来自自启动的 plist", EN: "restarted with launchctl; flags come from the autostart plist"},
	"banner.stopPortal":       {ZH: "portal 页面上的「关闭服务」", EN: "“Shut down” on the portal page"},
	"banner.stopPortalOrKill": {ZH: "portal 页面上的「关闭服务」，或 kill %d", EN: "“Shut down” on the portal page, or kill %d"},
	"banner.stopForeground":   {ZH: "Ctrl-C（或 kill %d）", EN: "Ctrl-C (or kill %d)"},

	// ---- CLI (cmd/llm-workbench/main.go, internal/daemon) --------
	"cli.errParse":       {ZH: "参数解析失败: %v", EN: "could not parse arguments: %v"},
	"cli.seeHelp":        {ZH: "用 --help 查看用法", EN: "run --help for usage"},
	"cli.errKickstart":   {ZH: "通过 launchd 重启失败: %v", EN: "restart through launchd failed: %v"},
	"cli.errLaunchdWait": {ZH: "已请求 launchd 重启，但 %s 没有响应", EN: "asked launchd to restart, but %s never answered"},
	"cli.hintLog":        {ZH: "  看看日志: %s", EN: "  see the log: %s"},
	"cli.tipPlist":       {ZH: "  提示   : 自启动实例只认 plist 里的命令，本次命令行参数没有生效；改了参数请重新打开「开机自动启动」开关", EN: "  note   : a launchd instance only runs the plist command, so these flags had no effect; reopen the autostart switch after changing them"},
	"cli.errStopOld":     {ZH: "停止旧实例失败: %v", EN: "could not stop the old instance: %v"},
	"cli.noteNoInstance": {ZH: "没有在运行的实例，直接启动", EN: "nothing was running, starting fresh"},
	"cli.noteStoppedOld": {ZH: "已停掉旧实例 · %s", EN: "stopped the old instance · %s"},
	"cli.errBackground":  {ZH: "后台启动失败: %v", EN: "background start failed: %v"},
	"cli.errStart":       {ZH: "启动失败: %v", EN: "start failed: %v"},
	"cli.hintAddrInUse":  {ZH: "  %s 可能已被占用", EN: "  %s may already be in use"},
	"cli.errServe":       {ZH: "服务异常退出: %v", EN: "server exited unexpectedly: %v"},
	"cli.warnHistory":    {ZH: "请求历史不可用: %v（代理继续运行）", EN: "request history unavailable: %v (proxy continues)"},
	"cli.warnProvider":   {ZH: "Playground Provider 不可用: %v（代理继续运行）", EN: "playground provider unavailable: %v (proxy continues)"},
	// The lifecycle marker stays language-neutral: the portal highlights
	// start/stop and scripts grep for them.
	"cli.logStart": {ZH: "start listen=%s upstream=%s params=%s", EN: "start listen=%s upstream=%s params=%s"},

	// ---- migrate (cmd/llm-workbench/main.go) ---------------------------
	"cli.errMigrate":         {ZH: "迁移失败: %v", EN: "migration failed: %v"},
	"cli.migrateCopied":      {ZH: "已把旧配置复制到 %s（旧文件保留）", EN: "copied the old config to %s (old file kept)"},
	"cli.migrateExists":      {ZH: "新配置已存在，未覆盖: %s", EN: "the new config already exists; not overwriting: %s"},
	"cli.migrateMissing":     {ZH: "没有找到旧配置，无需迁移: %s", EN: "no old config found; nothing to migrate: %s"},
	"cli.migrateLegacyAgent": {ZH: "检测到旧 LaunchAgent: %s\n  请先执行 launchctl bootout gui/$(id -u) %s，再在新 portal 里重新打开「开机自动启动」。", EN: "found the old LaunchAgent: %s\n  run launchctl bootout gui/$(id -u) %s first, then turn “Start at login” on again in the new portal."},
	"cli.migrateDone":        {ZH: "迁移检查完成。API、CLI 参数和默认行为均未改变。", EN: "migration check complete. API, CLI flags and default behavior are unchanged."},

	// ---- configuration (internal/config) --------------------------------
	"cfg.errUpstreamEmpty":       {ZH: "upstream 必须是非空字符串", EN: "upstream must be a non-empty string"},
	"cfg.errTimeoutNumber":       {ZH: "timeout 必须是数字, 收到: %s", EN: "timeout must be a number, got: %s"},
	"cfg.errTimeoutPositive":     {ZH: "timeout 必须大于 0", EN: "timeout must be greater than 0"},
	"cfg.errParamsObject":        {ZH: "params 必须是 JSON 对象", EN: "params must be a JSON object"},
	"cfg.errWriteConfig":         {ZH: "写入配置文件失败: %s", EN: "could not write the config file: %s"},
	"cfg.errPortFlag":            {ZH: "无效的端口: %q", EN: "invalid port: %q"},
	"cfg.errTimeoutFlag":         {ZH: "无效的超时: %q", EN: "invalid timeout: %q"},
	"cfg.errConcurrentFlag":      {ZH: "无效的并发上限: %q", EN: "invalid concurrency limit: %q"},
	"cfg.errBodySizeFlag":        {ZH: "无效的请求体上限: %q", EN: "invalid body-size limit: %q"},
	"cfg.errExtraArgs":           {ZH: "多余的参数: %s", EN: "unexpected arguments: %s"},
	"cfg.errSetForm":             {ZH: "--set 需要 key=value 形式，收到: %q", EN: "--set wants key=value, got: %q"},
	"cfg.errParamsEnv":           {ZH: "PROXY_PARAMS 需要 JSON 对象, 收到: %q", EN: "PROXY_PARAMS must be a JSON object, got: %q"},
	"cfg.errPortRange":           {ZH: "端口超出范围: %d", EN: "port out of range: %d"},
	"cfg.errMaxConcurrent":       {ZH: "--max-concurrent 必须大于 0", EN: "--max-concurrent must be greater than 0"},
	"cfg.errMaxBodySize":         {ZH: "--max-body-size 必须大于 0", EN: "--max-body-size must be greater than 0"},
	"cfg.errUpstreamScheme":      {ZH: "upstream 只支持 http/https: %q", EN: "upstream only supports http/https: %q"},
	"cfg.errUpstreamURL":         {ZH: "upstream 不是合法 URL: %q", EN: "upstream is not a valid URL: %q"},
	"cfg.errLangValue":           {ZH: "无效的语言: %q（可选 zh / en / auto）", EN: "invalid language: %q (use zh, en or auto)"},
	"cfg.errLangString":          {ZH: "lang 必须是 zh 或 en", EN: "lang must be zh or en"},
	"cfg.errThinking":            {ZH: "thinking 必须是 off / on / passthrough, 收到: %s", EN: "thinking must be off / on / passthrough, got: %s"},
	"cfg.errProfileString":       {ZH: "profile 必须是字符串", EN: "profile must be a string"},
	"cfg.errProfileUnknown":      {ZH: "未知的 profile: %q", EN: "unknown profile: %q"},
	"cfg.errProfilesObject":      {ZH: "profiles 必须是 JSON 对象", EN: "profiles must be a JSON object"},
	"cfg.errProfileName":         {ZH: "profile 名称不能为空", EN: "profile names must not be empty"},
	"cfg.errProfileObject":       {ZH: "profile %q 必须是 JSON 对象", EN: "profile %q must be a JSON object"},
	"cfg.errProfileThinking":     {ZH: "profile %q 的 thinking 无效: %q", EN: "profile %q has an invalid thinking mode: %q"},
	"cfg.errProfileSystemPrompt": {ZH: "profile %q 的 system_prompt 无效: %v", EN: "profile %q has an invalid system_prompt: %v"},
	"cfg.errSystemPromptObject":  {ZH: "system_prompt 必须是 JSON 对象", EN: "system_prompt must be a JSON object"},
	"cfg.errSystemPromptMode":    {ZH: "system_prompt.mode 必须是 off / inject-if-missing / override, 收到: %q", EN: "system_prompt.mode must be off / inject-if-missing / override, got: %q"},
	"cfg.errSystemPromptBoth":    {ZH: "--system-prompt 与 --system-prompt-file 只能选一个", EN: "--system-prompt and --system-prompt-file are mutually exclusive"},
	"cfg.errSystemPromptFile":    {ZH: "读取 system prompt 文件失败: %s: %v", EN: "could not read the system prompt file: %s: %v"},
	"cfg.errInspectorObject":     {ZH: "inspector 必须是 JSON 对象", EN: "inspector must be a JSON object"},
	"cfg.errInspectorBool":       {ZH: "inspector 的布尔字段无效", EN: "invalid boolean in inspector settings"},
	"cfg.errInspectorNumber":     {ZH: "inspector 的数字字段无效", EN: "invalid number in inspector settings"},
	"cfg.errInspectorNegative":   {ZH: "inspector 的保留设置不能为负数", EN: "inspector retention settings must not be negative"},
	"cfg.errStorageObject":       {ZH: "storage 必须是 JSON 对象", EN: "storage must be a JSON object"},
	"cfg.errStoragePath":         {ZH: "storage.path 必须是字符串", EN: "storage.path must be a string"},
	"cfg.errRetentionFlag":       {ZH: "无效的 retention-days: %q", EN: "invalid retention-days: %q"},
	"cfg.errMaxRowsFlag":         {ZH: "无效的 max-rows: %q", EN: "invalid max-rows: %q"},

	// ---- --help ---------------------------------------------------------
	"help.usage": {
		ZH: `用法: llm-workbench [命令] [选项]

本地优先的 LLM 代理与实验工作台（Ollama 优先）。
代理默认关闭思考并补齐缺失的生成参数；其余路径原样透传。

命令:
  (无)                     启动代理；默认后台运行并自动打开 portal 页面，-f 前台运行
  restart                  重启当前实例：停掉在跑的那个，再用本命令的参数启动
  migrate                  把旧 ollama-nothink-proxy 配置复制到新位置（不删旧文件）

选项:
  --host string            监听地址 (默认 %[1]s)
  --port int               监听端口 (默认 %[2]s)
  --upstream string        Ollama 地址 (默认 %[3]s)
  --set KEY=VALUE          覆盖注入参数，可重复；值按 JSON 解析
  --no-defaults            不注入内置默认生成参数
  --timeout float          上游连接/读取超时秒数 (默认 %[4]s)
  --max-concurrent int     最大并发请求数 (默认 %[5]s)
  --max-body-size int      可改写请求体上限字节数 (默认 %[6]s)
  --config string          配置文件 (默认 %[7]s)
  --log string             日志文件 (默认 %[8]s)
  --lang string            输出语言: zh / en / auto (默认 auto，跟随系统)
  --thinking string        thinking 模式: off / on / passthrough (默认 off)
  --system-prompt string   缺失时注入的 system prompt
  --system-prompt-file string  从文件读取 system prompt
  --system-prompt-override     替换已有 system/developer 消息
  --profile string         选择命名 profile（内置 nothink / raw / thinking）
  --no-inspector           关闭请求历史记录
  --capture-bodies         在历史中保存请求/响应正文（默认关闭）
  --db string              SQLite 数据库路径 (默认 %[9]s)
  --retention-days int     历史保留天数，0 表示永久 (默认 %[10]s)
  --max-rows int           最多保留的历史条数，0 表示不限 (默认 %[11]s)
  -v, --verbose            打印每个请求的日志
  -f, --foreground         前台运行（默认后台）
  --no-browser             启动后不自动打开 portal 页面
  --version                显示版本号

环境变量: PORT, UPSTREAM, PROXY_PARAMS, LLM_WORKBENCH_LANG (兼容 ONP_LANG)
示例:
  llm-workbench
  llm-workbench -f --no-browser
  llm-workbench --lang en               # 英文输出与英文页面
  llm-workbench restart                 # 重启（沿用当前参数）
  llm-workbench migrate                 # 从旧 ollama-nothink-proxy 配置迁移
  llm-workbench restart --port 11501    # 停掉旧实例，换端口重开
  llm-workbench --port 11500 --upstream http://127.0.0.1:11434
  llm-workbench --set temperature=0 --set max_tokens=128
  PROXY_PARAMS='{}' llm-workbench       # 只关思考, 不补参数
`,
		EN: `Usage: llm-workbench [command] [options]

A local-first LLM proxy and experiment workbench, Ollama first.
The proxy turns thinking off and fills in missing generation params by
default; every other path is passed through untouched.

Commands:
  (none)                   start the proxy; background by default, -f for foreground
  restart                  stop the running instance and start again with these flags
  migrate                  copy the old ollama-nothink-proxy config to the new location

Options:
  --host string            listen address (default %[1]s)
  --port int               listen port (default %[2]s)
  --upstream string        Ollama address (default %[3]s)
  --set KEY=VALUE          override an injected param, repeatable; parsed as JSON
  --no-defaults            inject no built-in generation params
  --timeout float          upstream connect/read timeout in seconds (default %[4]s)
  --max-concurrent int     max requests in flight (default %[5]s)
  --max-body-size int      max bytes of a rewritable request body (default %[6]s)
  --config string          config file (default %[7]s)
  --log string             log file (default %[8]s)
  --lang string            output language: zh / en / auto (default auto, from the locale)
  --thinking string        thinking mode: off / on / passthrough (default off)
  --system-prompt string   system prompt to inject when missing
  --system-prompt-file string  read the system prompt from a file
  --system-prompt-override     replace existing system/developer messages
  --profile string         select a named profile (built-ins: nothink / raw / thinking)
  --no-inspector           disable request history
  --capture-bodies         persist request/response bodies (off by default)
  --db string              SQLite database path (default %[9]s)
  --retention-days int     keep history for N days; 0 keeps forever (default %[10]s)
  --max-rows int           keep at most N rows; 0 keeps all (default %[11]s)
  -v, --verbose            print one log line per request
  -f, --foreground         run in the foreground (background by default)
  --no-browser             do not open the portal page on start
  --version                print the version

Environment: PORT, UPSTREAM, PROXY_PARAMS, LLM_WORKBENCH_LANG (ONP_LANG alias)
Examples:
  llm-workbench
  llm-workbench -f --no-browser
  llm-workbench --lang en               # English output and page
  llm-workbench restart                 # restart with the current flags
  llm-workbench migrate                 # import the old ollama-nothink-proxy config
  llm-workbench restart --port 11501    # stop the old one, reopen on a new port
  llm-workbench --port 11500 --upstream http://127.0.0.1:11434
  llm-workbench --set temperature=0 --set max_tokens=128
  PROXY_PARAMS='{}' llm-workbench       # thinking off only, no params
`,
	},

	// ---- proxy (internal/proxy) -----------------------------------------
	"proxy.errUpstreamEmpty":  {ZH: "proxy: upstream 不能为空", EN: "proxy: upstream must not be empty"},
	"proxy.errUpstreamScheme": {ZH: "proxy: 不支持的 upstream 协议: %q", EN: "proxy: unsupported upstream scheme: %q"},
	"proxy.errInvalidPath":    {ZH: "proxy: 非法路径", EN: "proxy: invalid request path"},
	"proxy.errBusy":           {ZH: "proxy: 并发已满，请稍后重试", EN: "proxy: all slots busy, please retry shortly"},
	"proxy.errReadBody":       {ZH: "proxy: 读取请求体失败: %s", EN: "proxy: could not read the request body: %s"},
	"proxy.errBodyTooLarge":   {ZH: "proxy: 请求体超过上限 (%d 字节)", EN: "proxy: request body over the limit (%d bytes)"},
	"proxy.errProfile":        {ZH: "proxy: 未知的 profile: %q", EN: "proxy: unknown profile: %q"},
	"proxy.errThinking":       {ZH: "proxy: thinking 必须是 off / on / passthrough, 收到: %q", EN: "proxy: thinking must be off / on / passthrough, got: %q"},

	// ---- portal (internal/portal) ---------------------------------------
	"portal.errLoopback":      {ZH: "portal 只允许本机访问", EN: "the portal only accepts local requests"},
	"portal.errNotFound":      {ZH: "portal: 未找到 %s", EN: "portal: no route for %s"},
	"portal.errReadBody":      {ZH: "读取请求体失败: %s", EN: "could not read the request body: %s"},
	"portal.errBodyTooLarge":  {ZH: "请求体过大", EN: "request body too large"},
	"portal.errBodyObject":    {ZH: "请求体必须是 JSON 对象", EN: "the request body must be a JSON object"},
	"portal.errBodyJSON":      {ZH: "请求体不是合法 JSON: %s", EN: "request body is not valid JSON: %s"},
	"portal.alreadyStopping":  {ZH: "服务已经在关闭中", EN: "the service is already shutting down"},
	"portal.shutdownDone":     {ZH: "服务已关闭，可以关掉这个页面了", EN: "service stopped — you can close this page"},
	"portal.logShutdown":      {ZH: "portal: 收到关闭请求，服务即将退出", EN: "portal: shutdown requested, exiting"},
	"portal.logAutostartAdd":  {ZH: "portal: 自启动安装 → %s", EN: "portal: autostart install → %s"},
	"portal.logAutostartDrop": {ZH: "portal: 自启动删除 → %s", EN: "portal: autostart remove → %s"},

	// ---- autostart (internal/autostart) ---------------------------------
	"autostart.errOnlyMac":     {ZH: "只有 macOS 支持开机自动启动", EN: "autostart is only supported on macOS"},
	"autostart.errNoCommand":   {ZH: "无法推断启动命令", EN: "could not infer the launch command"},
	"autostart.upToDate":       {ZH: "自启动已是最新，无需改动", EN: "autostart is already up to date"},
	"autostart.errWritePlist":  {ZH: "写 plist 失败: %s", EN: "could not write the plist: %s"},
	"autostart.errLoad":        {ZH: "plist 已写入，但加载失败: %s", EN: "plist written, but loading it failed: %s"},
	"autostart.installed":      {ZH: "已安装并加载自启动", EN: "installed and loaded the LaunchAgent"},
	"autostart.errRemovePlist": {ZH: "删除 plist 失败: %s", EN: "could not remove the plist: %s"},
	"autostart.errStillLoaded": {ZH: "已尝试卸载，但 launchd 仍报告在运行", EN: "unload attempted, but launchd still reports the job as loaded"},
	"autostart.removed":        {ZH: "已删除自启动配置", EN: "removed the autostart configuration"},
	"autostart.notLoaded":      {ZH: "自启动未加载", EN: "the LaunchAgent is not loaded"},
	"autostart.errBootstrap":   {ZH: "launchctl bootstrap 失败", EN: "launchctl bootstrap failed"},
	"autostart.plistComment":   {ZH: "由 llm-workbench 生成：LLM_WORKBENCH_LANG 让 launchd 起的实例与当前界面语言一致；手工改动会在下次保存自启动开关时被覆盖。", EN: "Generated by llm-workbench: LLM_WORKBENCH_LANG keeps the launchd instance in the same language as this UI; manual edits are overwritten the next time autostart is saved."},

	// ---- daemon (internal/daemon) ---------------------------------------
	"daemon.errNotRunning":  {ZH: "没有在运行的实例", EN: "no instance is running"},
	"daemon.errStopTimeout": {ZH: "旧实例在 %s 内没有退出", EN: "the old instance did not exit within %s"},
	"daemon.errExecutable":  {ZH: "无法定位可执行文件: %s", EN: "could not locate the executable: %s"},
	"daemon.errLogDir":      {ZH: "无法创建日志目录: %s", EN: "could not create the log directory: %s"},
	"daemon.errLogFile":     {ZH: "无法打开日志文件: %s", EN: "could not open the log file: %s"},
	"daemon.errChild":       {ZH: "无法启动子进程: %s", EN: "could not start the child process: %s"},
	"daemon.errChildExited": {ZH: "子进程已退出", EN: "the child process exited"},
	"daemon.errNoResponse":  {ZH: "%s 内没有响应", EN: "no response within %s"},
}
