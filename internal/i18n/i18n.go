// Package i18n holds every user-facing string in the proxy — CLI output, the
// banner, log lines, portal messages and the LaunchAgent plist — in Chinese
// and English, plus the process-wide language switch.
//
// The language is one process-wide value: the config file decides at startup
// and the portal may change it at runtime, so the current value lives in an
// atomic instead of a plain variable. Without a stored choice the system
// locale is used.
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
	if value == "" {
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

// Detect picks a language from the system locale variables and falls back to
// Chinese (the project's primary language). lookup is os.LookupEnv in
// production and a stub in tests.
func Detect(lookup func(string) (string, bool)) Lang {
	if lookup == nil {
		return ZH
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
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
	"banner.hintWhere":        {ZH: "改参数、看日志", EN: "settings & logs"},
	"banner.hintStop":         {ZH: "停止服务", EN: "stop"},
	"banner.statusForeground": {ZH: "前台运行 (pid %d)", EN: "foreground (pid %d)"},
	"banner.statusBackground": {ZH: "后台运行 (pid %d)", EN: "background (pid %d)"},
	"banner.statusReused":     {ZH: "后台运行 · 复用已在运行的实例", EN: "background · reusing the running instance"},
	"banner.statusLaunchd":    {ZH: "后台运行 · launchd 托管 (pid %d)", EN: "background · launchd supervised (pid %d)"},
	"banner.noteLaunchd":      {ZH: "已通过 launchctl 重启，参数来自自启动的 plist", EN: "restarted with launchctl; flags come from the autostart plist"},
	"banner.stopPortal":       {ZH: "代理页面上的「关闭服务」", EN: "“Shut down” on the proxy page"},
	"banner.stopPortalOrKill": {ZH: "代理页面上的「关闭服务」，或 kill %d", EN: "“Shut down” on the proxy page, or kill %d"},
	"banner.stopForeground":   {ZH: "Ctrl-C（或 kill %d）", EN: "Ctrl-C (or kill %d)"},

	// ---- CLI (cmd/llm-workbench/main.go, internal/daemon) --------
	"cli.errParse":       {ZH: "参数解析失败: %v", EN: "could not parse arguments: %v"},
	"cli.seeHelp":        {ZH: "用 --help 查看用法", EN: "run --help for usage"},
	"cli.errKickstart":   {ZH: "通过 launchd 重启失败: %v", EN: "restart through launchd failed: %v"},
	"cli.errLaunchdWait": {ZH: "已请求 launchd 重启，但 %s 没有响应", EN: "asked launchd to restart, but %s never answered"},
	"cli.hintLog":        {ZH: "  看看日志: %s", EN: "  see the log: %s"},
	"cli.tipPlist":       {ZH: "  提示   : 自启动实例只认 plist 里的命令，本次命令行参数没有生效；改了参数请重新打开「开机自动启动」开关", EN: "  note   : a launchd instance only runs the plist command, so these flags had no effect; reopen the autostart switch after changing them"},
	"cli.errStopOld":     {ZH: "停止旧实例失败: %v", EN: "could not stop the old instance: %v"},
	"cli.stopNothing":    {ZH: "没有在运行的实例", EN: "nothing is running"},
	"cli.stopped":        {ZH: "已停止服务 · %s", EN: "stopped the service · %s"},
	"cli.stoppedLaunchd": {ZH: "已停止 launchd 托管的实例 · %s（自启动仍保留，下次登录会再启动）", EN: "stopped the launchd-supervised instance · %s (autostart stays on and starts it again at the next login)"},
	"cli.errStop":        {ZH: "停止服务失败: %v", EN: "could not stop the service: %v"},
	"cli.noteNoInstance": {ZH: "没有在运行的实例，直接启动", EN: "nothing was running, starting fresh"},
	"cli.noteStoppedOld": {ZH: "已停掉旧实例 · %s", EN: "stopped the old instance · %s"},
	"cli.errBackground":  {ZH: "后台启动失败: %v", EN: "background start failed: %v"},
	"cli.errStart":       {ZH: "启动失败: %v", EN: "start failed: %v"},
	"cli.hintAddrInUse":  {ZH: "  %s 可能已被占用", EN: "  %s may already be in use"},
	"cli.configMigrated": {ZH: "已迁移旧配置: %s → %s（原文件保留为 *.bak）", EN: "migrated the legacy config: %s → %s (the original is kept as *.bak)"},
	"cli.dataMigrated":   {ZH: "已迁移旧数据库: %s → %s", EN: "migrated the legacy database: %s → %s"},
	"cli.portAdjusted":   {ZH: "端口 %d 不能用（11434 被 Ollama 占用，代理也不能和 upstream 用同一个端口），已改用 %d", EN: "port %d cannot be used (11434 belongs to Ollama, and the proxy cannot share the upstream's port); using %d instead"},
	"cli.errServe":       {ZH: "服务异常退出: %v", EN: "server exited unexpectedly: %v"},
	"cli.warnHistory":    {ZH: "请求历史不可用: %v（代理继续运行）", EN: "request history unavailable: %v (proxy continues)"},
	"cli.warnProvider":   {ZH: "Playground Provider 不可用: %v（代理继续运行）", EN: "playground provider unavailable: %v (proxy continues)"},
	// The lifecycle marker stays language-neutral: the portal highlights
	// start/stop and scripts grep for them.
	"cli.logStart": {ZH: "start listen=%s upstream=%s thinking=%s", EN: "start listen=%s upstream=%s thinking=%s"},

	// ---- configuration (internal/config) --------------------------------
	"cfg.errUpstreamEmpty":      {ZH: "upstream 必须是非空字符串", EN: "upstream must be a non-empty string"},
	"cfg.errTimeoutNumber":      {ZH: "timeout 必须是数字, 收到: %s", EN: "timeout must be a number, got: %s"},
	"cfg.errTimeoutPositive":    {ZH: "timeout 必须大于 0", EN: "timeout must be greater than 0"},
	"cfg.errWriteConfig":        {ZH: "写入配置文件失败: %s", EN: "could not write the config file: %s"},
	"cfg.errPortFlag":           {ZH: "无效的端口: %q", EN: "invalid port: %q"},
	"cfg.errExtraArgs":          {ZH: "多余的参数: %s", EN: "unexpected arguments: %s"},
	"cfg.errPortRange":          {ZH: "端口超出范围: %d", EN: "port out of range: %d"},
	"cfg.errMaxConcurrent":      {ZH: "并发上限必须大于 0", EN: "the concurrency limit must be greater than 0"},
	"cfg.errMaxConcurrentValue": {ZH: "max_concurrent 必须是大于 0 的整数, 收到: %s", EN: "max_concurrent must be an integer greater than 0, got: %s"},
	"cfg.errMaxBodySize":        {ZH: "请求体上限必须大于 0", EN: "the body-size limit must be greater than 0"},
	"cfg.errMaxBodySizeValue":   {ZH: "max_body_size 必须是大于 0 的数字, 收到: %s", EN: "max_body_size must be a number greater than 0, got: %s"},
	"cfg.errUpstreamScheme":     {ZH: "upstream 只支持 http/https: %q", EN: "upstream only supports http/https: %q"},
	"cfg.errUpstreamURL":        {ZH: "upstream 不是合法 URL: %q", EN: "upstream is not a valid URL: %q"},
	"cfg.errLangString":         {ZH: "lang 必须是 zh 或 en", EN: "lang must be zh or en"},
	"cfg.errThinking":           {ZH: "thinking 必须是 off / on / passthrough, 收到: %s", EN: "thinking must be off / on / passthrough, got: %s"},
	"cfg.errProfileRemoved":     {ZH: "profile 功能已移除，配置中不能再包含 %s", EN: "profiles were removed; the config may not contain %s"},
	"cfg.errProfileRemovedFile": {ZH: "配置文件 %s 仍包含已移除的 %s；请删除后重新启动", EN: "config file %s still contains removed %s; delete them and restart"},
	"cfg.errInspectorObject":    {ZH: "inspector 必须是 JSON 对象", EN: "inspector must be a JSON object"},
	"cfg.errInspectorBool":      {ZH: "inspector 的布尔字段无效", EN: "invalid boolean in inspector settings"},
	"cfg.errInspectorNumber":    {ZH: "inspector 的数字字段无效", EN: "invalid number in inspector settings"},
	"cfg.errInspectorNegative":  {ZH: "inspector 的保留设置不能为负数", EN: "inspector retention settings must not be negative"},
	"cfg.errStorageObject":      {ZH: "storage 必须是 JSON 对象", EN: "storage must be a JSON object"},
	"cfg.errStoragePath":        {ZH: "storage.path 必须是字符串", EN: "storage.path must be a string"},

	// ---- --help ---------------------------------------------------------
	"help.usage": {
		ZH: `用法: llm-workbench [命令] [选项]

本地优先的 LLM 代理与实验工作台（支持 Ollama 与更多 Provider）。
代理默认只改写思考字段；生成参数、消息和其他内容原样透传。
除启动项外，全部配置都在代理页面上完成。

命令:
  (无) / start             启动代理；默认后台运行并自动打开代理页面
  stop                     停止正在运行的实例
  restart                  重启当前实例：停掉在跑的那个，再用本命令的参数启动

选项:
  --host string            监听地址 (默认 %[1]s)
  --port int               监听端口 (默认 %[2]s)
  --config string          配置文件 (默认 %[3]s)
  --log string             日志文件 (默认 %[4]s)
  --db string              SQLite 数据库路径 (默认 %[5]s)
  -v, --version            显示版本号

上游地址、超时、thinking、历史记录和语言都在
代理页面 (/_/llm-proxy) 上配置。
示例:
  llm-workbench
  llm-workbench stop
  llm-workbench restart                 # 重启（沿用当前配置）
  llm-workbench restart --port 11501    # 停掉旧实例，换端口重开
`,
		EN: `Usage: llm-workbench [command] [options]

A local-first LLM proxy and experiment workbench with Ollama and more providers.
The proxy only rewrites thinking-related fields; generation params, messages
and every other field are passed through untouched.
Everything except bootstrapping is configured on the proxy page.

Commands:
  (none) / start           start the proxy; background by default, opens the proxy page
  stop                     stop the running instance
  restart                  stop the running instance and start again with these flags

Options:
  --host string            listen address (default %[1]s)
  --port int               listen port (default %[2]s)
  --config string          config file (default %[3]s)
  --log string             log file (default %[4]s)
  --db string              SQLite database path (default %[5]s)
  -v, --version            print the version

Upstream, timeout, thinking, history and language are configured on the
proxy page (/_/llm-proxy).
Examples:
  llm-workbench
  llm-workbench stop
  llm-workbench restart                 # restart with the current config
  llm-workbench restart --port 11501    # stop the old one, reopen on a new port
`,
	},

	// ---- proxy (internal/proxy) -----------------------------------------
	"proxy.errUpstreamEmpty":  {ZH: "proxy: upstream 不能为空", EN: "proxy: upstream must not be empty"},
	"proxy.errUpstreamScheme": {ZH: "proxy: 不支持的 upstream 协议: %q", EN: "proxy: unsupported upstream scheme: %q"},
	"proxy.errInvalidPath":    {ZH: "proxy: 非法路径", EN: "proxy: invalid request path"},
	"proxy.errBusy":           {ZH: "proxy: 并发已满，请稍后重试", EN: "proxy: all slots busy, please retry shortly"},
	"proxy.errReadBody":       {ZH: "proxy: 读取请求体失败: %s", EN: "proxy: could not read the request body: %s"},
	"proxy.errBodyTooLarge":   {ZH: "proxy: 请求体超过上限 (%d 字节)", EN: "proxy: request body over the limit (%d bytes)"},
	"proxy.errProfileHeader":  {ZH: "proxy: X-Workbench-Profile 已移除，请改用代理页面的全局设置", EN: "proxy: X-Workbench-Profile was removed; use the global settings on the proxy page"},
	"proxy.errThinkingHeader": {ZH: "proxy: X-Workbench-Thinking 已移除，请改用代理页面的 Thinking 模式", EN: "proxy: X-Workbench-Thinking was removed; use the Thinking mode on the proxy page"},

	// ---- portal (internal/portal) ---------------------------------------
	"portal.errLoopback":      {ZH: "代理页面只允许本机访问", EN: "the proxy page only accepts local requests"},
	"portal.errNotFound":      {ZH: "proxy: 未找到 %s", EN: "proxy: no route for %s"},
	"portal.errReadBody":      {ZH: "读取请求体失败: %s", EN: "could not read the request body: %s"},
	"portal.errBodyTooLarge":  {ZH: "请求体过大", EN: "request body too large"},
	"portal.errBodyObject":    {ZH: "请求体必须是 JSON 对象", EN: "the request body must be a JSON object"},
	"portal.errBodyJSON":      {ZH: "请求体不是合法 JSON: %s", EN: "request body is not valid JSON: %s"},
	"portal.alreadyStopping":  {ZH: "服务已经在关闭中", EN: "the service is already shutting down"},
	"portal.shutdownDone":     {ZH: "服务已关闭，可以关掉这个页面了", EN: "service stopped — you can close this page"},
	"portal.logShutdown":      {ZH: "proxy: 收到关闭请求，服务即将退出", EN: "proxy: shutdown requested, exiting"},
	"portal.logAutostartAdd":  {ZH: "proxy: 自启动安装 → %s", EN: "proxy: autostart install → %s"},
	"portal.logAutostartDrop": {ZH: "proxy: 自启动删除 → %s", EN: "proxy: autostart remove → %s"},

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
	"autostart.plistComment":   {ZH: "由 llm-workbench 生成；手工改动会在下次保存自启动开关时被覆盖。", EN: "Generated by llm-workbench; manual edits are overwritten the next time autostart is saved."},

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
