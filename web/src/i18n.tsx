import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";

export type Lang = "zh" | "en";

// The portal and the workbench share one choice: same key, same origin.
const LANG_KEY = "llm-workbench-lang";

const messages = {
  zh: {
    "app.title": "LLM Workbench · 工作台",
    "app.tagline": "本机 LLM 代理与实验工作台 · 支持 Ollama 与更多 Provider",
    "nav.proxy": "代理",
    "nav.requests": "请求",
    "nav.playground": "试验场",
    "nav.experiments": "实验",
    "action.lang": "切换界面语言",
    "action.refresh": "刷新",
    "action.export": "导出",
    "action.previous": "上一页",
    "action.next": "下一页",
    "action.delete": "删除",
    "action.inspect": "查看请求",
    "action.experiment": "转为实验",
    "action.close": "收起",
    "action.new": "新建",
    "action.update": "更新",
    "action.saveNew": "另存",
    "action.send": "发送",
    "action.stop": "停止",
    "action.clear": "清空",
    "action.run": "运行",
    "action.create": "创建",
    "action.cancel": "取消",
    "action.remove": "移除",
    "action.addVariant": "添加变体",
    "action.addCase": "添加用例",
    "action.json": "JSON",
    "action.csv": "CSV",
    "status.connected": "已连接",
    "status.connecting": "连接中…",
    "chip.requests": "{n} 条请求",
    "chip.bodiesOn": "记录正文",
    "chip.bodiesOff": "不记正文",

    "common.loading": "加载中…",
    "common.none": "—",
    "common.raw": "原始记录",
    "common.time": "时间",
    "common.model": "模型",
    "common.endpoint": "端点",
    "common.status": "状态",
    "common.latency": "耗时",
    "common.ttft": "首字",
    "common.tokens": "Token",
    "common.tokensInput": "输入 Token",
    "common.tokensOutput": "输出 Token",
    "common.tokensTotal": "合计 Token",
    "common.source": "来源",
    "common.duration": "总耗时",
    "common.client": "客户端",
    "common.output": "输出",
    "common.variant": "变体",
    "common.case": "用例",
    "common.rating": "评分",
    "common.note": "备注",
    "common.runs": "{n} 次运行",
    "common.variants": "{n} 个变体",
    "common.cases": "{n} 个用例",

    "select.searchPlaceholder": "搜索…",
    "select.empty": "没有匹配的选项",
    "select.none": "未选择",

    "requests.search": "搜索 prompt、模型或正文…",
    "requests.sourceAll": "全部来源",
    "requests.statusHint": "状态",
    "requests.live": "实时",
    "requests.matching": "{n} 条匹配",
    "requests.empty": "还没有请求记录。用试验场发一条，或把客户端指向本代理。",
    "requests.title": "请求详情",
    "requests.intro": "选中一行，查看原始请求、转发内容和收到的响应。",
    "requests.database": "数据库",
    "requests.written": "已写入",
    "requests.dropped": "已丢弃",
    "requests.tabRequest": "原始请求",
    "requests.tabForwarded": "转发请求",
    "requests.tabResponse": "响应",
    "requests.tabRaw": "完整记录",
    "requests.bodyOff": "没有记录正文（inspector.capture_bodies 已关闭）。",
    "requests.bodyMissing": "没有记录正文。",
    "requests.truncated": "正文在捕获上限处被截断。",
    "requests.confirmDelete": "删除 {id}？",

    "playground.model": "模型",
    "playground.provider": "Provider",
    "playground.modelName": "模型名称",
    "playground.modelPlaceholder": "选择模型",
    "playground.stream": "流式输出",
    "playground.thinking": "思考",
    "playground.thinkingOff": "关闭",
    "playground.thinkingOn": "开启",
    "playground.thinkingPassthrough": "透传",
    "playground.system": "系统提示",
    "playground.systemPlaceholder": "可选的 system prompt",
    "playground.params": "参数",
    "playground.prompts": "已存 Prompt",
    "playground.promptName": "Prompt 名称",
    "playground.loadPrompt": "载入 Prompt…",
    "playground.emptyTitle": "开发者试验场",
    "playground.emptyBody": "选择模型，编辑 system prompt 和参数，然后发送。每次调用都会记录到请求里。",
    "playground.subtitle": "调试 prompt，不只是聊天",
    "playground.promptPlaceholder": "用户消息…",
    "playground.raw": "原始请求 / 响应",
    "playground.request": "请求",
    "playground.response": "响应",
    "playground.promptNameRequired": "Prompt 名称不能为空",
    "playground.confirmDeletePrompt": "删除 Prompt {name}？",
    "playground.stopped": "已停止",
    "playground.streamFailed": "流式输出失败",

    "experiments.title": "实验",
    "experiments.emptyList": "还没有实验。",
    "experiments.new": "新建实验",
    "experiments.name": "名称",
    "experiments.notes": "备注",
    "experiments.variants": "变体",
    "experiments.cases": "用例",
    "experiments.provider": "Provider",
    "experiments.model": "模型",
    "experiments.system": "系统提示",
    "experiments.params": "参数 JSON",
    "experiments.caseName": "用例名称",
    "experiments.messages": "消息 JSON",
    "experiments.running": "运行中…",
    "experiments.emptyRuns": "还没有运行。点「运行」执行全部变体 × 用例。",
    "experiments.emptyTitle": "实验",
    "experiments.emptyBody": "用同一组 Prompt 对比不同模型的回答，找到更合适的那一个。",
    "experiments.formIntro": "三步完成：起个名字、选择要对比的模型、写下 Prompt。",
    "experiments.stepBasics": "基本信息",
    "experiments.stepBasicsHint": "给实验一个以后还能看懂的名字。",
    "experiments.stepModels": "选择模型",
    "experiments.stepModelsHint": "可以加入多个模型；每个模型都会回答下面所有 Prompt。",
    "experiments.stepPrompts": "写下 Prompt",
    "experiments.stepPromptsHint": "每个 Prompt 会在所有模型上各运行一次。",
    "experiments.namePlaceholder": "例如：客服回复对比",
    "experiments.notesPlaceholder": "可选备注",
    "experiments.modelPlaceholder": "选择模型",
    "experiments.promptName": "Prompt 名称",
    "experiments.promptNamePlaceholder": "可选，例如：退款咨询",
    "experiments.prompt": "Prompt",
    "experiments.promptPlaceholder": "输入要发给模型的用户消息…",
    "experiments.advanced": "高级设置",
    "experiments.systemPlaceholder": "可选的 system prompt",
    "experiments.addModel": "添加模型",
    "experiments.addPrompt": "添加 Prompt",
    "experiments.runsTotal": "{n} 次运行",
    "experiments.readyTitle": "准备就绪",
    "experiments.readyBody": "点右上角「运行」，所有模型会依次回答每个 Prompt。",
    "experiments.answer": "回答",
    "experiments.casePending": "等待运行",
    "experiments.welcomeStep1": "给实验起名",
    "experiments.welcomeStep2": "选择两个或更多模型",
    "experiments.welcomeStep3": "写下要比较的 Prompt，然后运行",
    "experiments.nameRequired": "实验名称不能为空",
    "experiments.modelRequired": "至少选择一个模型",
    "experiments.providerRequired": "请选择一个 Provider",
    "experiments.promptRequired": "每个 Prompt 都需要内容",
    "experiments.paramsInvalid": "参数必须是合法 JSON 对象",
    "experiments.variantRequired": "每个变体都需要 provider 和模型",
    "experiments.caseRequired": "每个用例都需要非空的消息数组",
    "experiments.confirmDelete": "删除实验 {name}？",
    "experiments.fromModel": "来自 {model}",
    "experiments.fromRequest": "来自请求 {id}",
    "experiments.fromRequestCase": "来自请求",
    "experiments.case": "用例",
    "experiments.meta": "{v} 个变体 · {c} 个用例 · {r} 次运行",
    "experiments.thinking": "思考",
    "experiments.status.pending": "等待",
    "experiments.status.running": "运行中",
    "experiments.status.succeeded": "成功",
    "experiments.status.failed": "失败",
    "experiments.status.canceled": "已取消"
  },
  en: {
    "app.title": "LLM Workbench · workbench",
    "app.tagline": "local LLM proxy & workbench · Ollama and more providers",
    "nav.proxy": "Proxy",
    "nav.requests": "Requests",
    "nav.playground": "Playground",
    "nav.experiments": "Experiments",
    "action.lang": "Switch the interface language",
    "action.refresh": "refresh",
    "action.export": "export",
    "action.previous": "previous",
    "action.next": "next",
    "action.delete": "delete",
    "action.inspect": "inspect",
    "action.experiment": "experiment",
    "action.close": "close",
    "action.new": "new",
    "action.update": "update",
    "action.saveNew": "save new",
    "action.send": "send",
    "action.stop": "stop",
    "action.clear": "clear",
    "action.run": "run",
    "action.create": "create",
    "action.cancel": "cancel",
    "action.remove": "remove",
    "action.addVariant": "add variant",
    "action.addCase": "add case",
    "action.json": "JSON",
    "action.csv": "CSV",
    "status.connected": "connected",
    "status.connecting": "connecting…",
    "chip.requests": "{n} requests",
    "chip.bodiesOn": "bodies on",
    "chip.bodiesOff": "bodies off",

    "common.loading": "loading…",
    "common.none": "—",
    "common.raw": "raw",
    "common.time": "time",
    "common.model": "model",
    "common.endpoint": "endpoint",
    "common.status": "status",
    "common.latency": "latency",
    "common.ttft": "ttft",
    "common.tokens": "tokens",
    "common.tokensInput": "input tokens",
    "common.tokensOutput": "output tokens",
    "common.tokensTotal": "total tokens",
    "common.source": "source",
    "common.duration": "duration",
    "common.client": "client",
    "common.output": "output",
    "common.variant": "variant",
    "common.case": "case",
    "common.rating": "rating",
    "common.note": "note",
    "common.runs": "{n} runs",
    "common.variants": "{n} variants",
    "common.cases": "{n} cases",

    "select.searchPlaceholder": "Search…",
    "select.empty": "No matching options",
    "select.none": "Not selected",

    "requests.search": "Search prompt, model or body…",
    "requests.sourceAll": "all sources",
    "requests.statusHint": "status",
    "requests.live": "live",
    "requests.matching": "{n} matching",
    "requests.empty": "No requests recorded yet. Use the Playground or point a client at this proxy.",
    "requests.title": "Request detail",
    "requests.intro": "Select a row to inspect the original request, what was forwarded upstream, and the raw response.",
    "requests.database": "database",
    "requests.written": "written",
    "requests.dropped": "dropped",
    "requests.tabRequest": "request",
    "requests.tabForwarded": "forwarded",
    "requests.tabResponse": "response",
    "requests.tabRaw": "raw",
    "requests.bodyOff": "Body not recorded (inspector.capture_bodies is off).",
    "requests.bodyMissing": "Body not recorded.",
    "requests.truncated": "Body was truncated at the capture limit.",
    "requests.confirmDelete": "Delete {id}?",

    "playground.model": "Model",
    "playground.provider": "Provider",
    "playground.modelName": "Model",
    "playground.modelPlaceholder": "select a model",
    "playground.stream": "stream",
    "playground.thinking": "Thinking",
    "playground.thinkingOff": "off",
    "playground.thinkingOn": "on",
    "playground.thinkingPassthrough": "passthrough",
    "playground.system": "System prompt",
    "playground.systemPlaceholder": "Optional system prompt",
    "playground.params": "Parameters",
    "playground.prompts": "Saved prompts",
    "playground.promptName": "prompt name",
    "playground.loadPrompt": "load a prompt…",
    "playground.emptyTitle": "Developer playground",
    "playground.emptyBody": "Pick a model, edit the system prompt and params, then send. Every call is recorded in Inspector.",
    "playground.subtitle": "debug a prompt, not just chat",
    "playground.promptPlaceholder": "User message…",
    "playground.raw": "raw request / response",
    "playground.request": "request",
    "playground.response": "response",
    "playground.promptNameRequired": "prompt name is required",
    "playground.confirmDeletePrompt": "Delete prompt {name}?",
    "playground.stopped": "stopped",
    "playground.streamFailed": "stream failed",

    "experiments.title": "Experiments",
    "experiments.emptyList": "No experiments yet.",
    "experiments.new": "New experiment",
    "experiments.name": "Name",
    "experiments.notes": "Notes",
    "experiments.variants": "Variants",
    "experiments.cases": "Cases",
    "experiments.provider": "provider",
    "experiments.model": "model",
    "experiments.system": "system prompt",
    "experiments.params": "params JSON",
    "experiments.caseName": "case name",
    "experiments.messages": "messages JSON",
    "experiments.running": "running…",
    "experiments.emptyRuns": "No runs yet. Press run to execute every variant × case.",
    "experiments.emptyTitle": "Experiments",
    "experiments.emptyBody": "Run the same prompts through several models and compare the answers.",
    "experiments.formIntro": "Three steps: name it, pick the models to compare, then write the prompts.",
    "experiments.stepBasics": "Basics",
    "experiments.stepBasicsHint": "Give the experiment a name you will recognise later.",
    "experiments.stepModels": "Models",
    "experiments.stepModelsHint": "Add more than one model; each one answers every prompt below.",
    "experiments.stepPrompts": "Prompts",
    "experiments.stepPromptsHint": "Every prompt runs once on every model.",
    "experiments.namePlaceholder": "e.g. support replies",
    "experiments.notesPlaceholder": "optional note",
    "experiments.modelPlaceholder": "select a model",
    "experiments.promptName": "Prompt name",
    "experiments.promptNamePlaceholder": "optional, e.g. refund question",
    "experiments.prompt": "Prompt",
    "experiments.promptPlaceholder": "Write the user message to send…",
    "experiments.advanced": "Advanced",
    "experiments.systemPlaceholder": "optional system prompt",
    "experiments.addModel": "Add model",
    "experiments.addPrompt": "Add prompt",
    "experiments.runsTotal": "{n} runs",
    "experiments.readyTitle": "Ready to run",
    "experiments.readyBody": "Press Run in the top corner; every model will answer each prompt.",
    "experiments.answer": "Answer",
    "experiments.casePending": "Waiting to run",
    "experiments.welcomeStep1": "Name the experiment",
    "experiments.welcomeStep2": "Pick two or more models",
    "experiments.welcomeStep3": "Write the prompts and run",
    "experiments.nameRequired": "experiment name is required",
    "experiments.modelRequired": "choose at least one model",
    "experiments.providerRequired": "choose a provider",
    "experiments.promptRequired": "every prompt needs text",
    "experiments.paramsInvalid": "params must be a valid JSON object",
    "experiments.variantRequired": "every variant needs provider and model",
    "experiments.caseRequired": "every case needs a non-empty messages array",
    "experiments.confirmDelete": "Delete experiment {name}?",
    "experiments.fromModel": "From {model}",
    "experiments.fromRequest": "Created from {id}",
    "experiments.fromRequestCase": "from request",
    "experiments.case": "case",
    "experiments.meta": "{v} variants · {c} cases · {r} runs",
    "experiments.thinking": "thinking",
    "experiments.status.pending": "pending",
    "experiments.status.running": "running",
    "experiments.status.succeeded": "succeeded",
    "experiments.status.failed": "failed",
    "experiments.status.canceled": "canceled"
  }
} as const;

export type MsgKey = keyof typeof messages.en;

function detectLang(): Lang {
  try {
    const saved = window.localStorage.getItem(LANG_KEY);
    if (saved === "en" || saved === "zh") return saved;
  } catch {
    // private mode
  }
  return navigator.language.toLowerCase().startsWith("zh") ? "zh" : "en";
}

function render(lang: Lang, key: MsgKey, vars?: Record<string, string | number>): string {
  let text: string = messages[lang][key] ?? messages.en[key] ?? key;
  if (vars) {
    for (const [name, value] of Object.entries(vars)) {
      text = text.split(`{${name}}`).join(String(value));
    }
  }
  return text;
}

type I18nValue = {
  lang: Lang;
  setLang: (lang: Lang) => void;
  toggle: () => void;
  t: (key: MsgKey, vars?: Record<string, string | number>) => string;
};

const I18nContext = createContext<I18nValue | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(detectLang);

  useEffect(() => {
    document.documentElement.lang = lang === "en" ? "en" : "zh-CN";
    document.title = render(lang, "app.title");
    try {
      window.localStorage.setItem(LANG_KEY, lang);
    } catch {
      // private mode
    }
  }, [lang]);

  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key !== LANG_KEY) return;
      if (event.newValue === "en" || event.newValue === "zh") setLangState(event.newValue);
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  const setLang = useCallback((next: Lang) => setLangState(next), []);
  const toggle = useCallback(() => setLangState((current) => (current === "en" ? "zh" : "en")), []);
  const value = useMemo<I18nValue>(
    () => ({
      lang,
      setLang,
      toggle,
      t: (key, vars) => render(lang, key, vars),
    }),
    [lang, setLang, toggle],
  );

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nValue {
  const value = useContext(I18nContext);
  if (!value) throw new Error("useI18n must be used inside I18nProvider");
  return value;
}
