import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { useI18n } from "./i18n";
import type { MsgKey } from "./i18n";
import {
  errorMessage,
  getPortalLog,
  savePortalConfig,
  togglePortalAutostart,
  type PortalLogEntry,
  type PortalState,
} from "./portalApi";
import type { PortalController } from "./usePortal";

type Translate = (key: MsgKey, vars?: Record<string, string | number>) => string;

type Form = {
  upstream: string;
  timeout: string;
  params: string;
  maxConcurrent: string;
  maxBodySize: string;
  thinking: string;
  systemMode: string;
  systemText: string;
  profile: string;
  profiles: string;
};

type Message = {
  text: string;
  kind: "" | "ok" | "err";
};

type Meta = {
  text: string;
  bad: boolean;
};

function formFromState(state: PortalState): Form {
  return {
    upstream: state.upstream,
    timeout: String(state.timeout),
    params: JSON.stringify(state.params ?? {}, null, 2),
    maxConcurrent: String(state.max_concurrent),
    maxBodySize: String(state.max_body_size),
    thinking: state.thinking || "off",
    systemMode: state.system_prompt?.mode || "off",
    systemText: state.system_prompt?.text || "",
    profile: state.profile || "",
    profiles: JSON.stringify(state.profiles ?? {}, null, 2),
  };
}

function isObject(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

function jsonMeta(raw: string, t: Translate, kind: "params" | "profiles"): Meta {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw || "{}");
  } catch {
    return { text: t("proxy.invalidJSON"), bad: true };
  }
  if (!isObject(parsed)) {
    return { text: t("proxy.expectedObject"), bad: true };
  }
  const count = Object.keys(parsed).length;
  if (!count) return { text: t("proxy.emptyObject"), bad: false };
  return {
    text:
      kind === "params"
        ? t("proxy.paramsCount", { n: count })
        : t("proxy.profilesCount", { n: count }),
    bad: false,
  };
}

function shortenPath(path: string, max: number): string {
  if (!path) return "—";
  if (path.length <= max) return path;
  const head = 12;
  const tail = Math.max(4, max - head - 1);
  return path.slice(0, head) + "…" + path.slice(path.length - tail);
}

// FitPath keeps the head and file name visible when the column is narrower
// than the absolute path, without waiting for CSS to cut off the tail.
function FitPath({ value }: { value: string }) {
  const ref = useRef<HTMLSpanElement | null>(null);
  const [text, setText] = useState(value || "—");

  useLayoutEffect(() => {
    const node = ref.current;
    if (!node) return;
    const fit = () => {
      if (!value) {
        setText("—");
        return;
      }
      const budget = Math.max(16, Math.floor((node.clientWidth - 2) / 7));
      setText(shortenPath(value, budget));
    };
    fit();
    const observer = new ResizeObserver(fit);
    observer.observe(node);
    return () => observer.disconnect();
  }, [value]);

  return (
    <span ref={ref} className="proxy-fit" title={value}>
      {text}
    </span>
  );
}

const LEVEL_CLASS: Record<string, string> = {
  error: "lv-error",
  warn: "lv-warn",
  ok: "lv-ok",
  info: "",
};

const VERB_CLASS: Record<string, string> = {
  GET: "v-get",
  HEAD: "v-head",
  POST: "v-post",
  PUT: "v-put",
  PATCH: "v-patch",
  DELETE: "v-del",
};

const TEXT_RULES: { re: RegExp; cls: string }[] = [
  {
    re: /(失败|错误|异常|超时|Traceback|refused|timeout|Timeout|failed|Failed|error|502|503|504)/,
    cls: "proxy-hl-bad",
  },
  {
    re: /(已启动|已安装|已加载|已保存|已删除|已生效|已关闭|成功|installed|loaded|saved|applied|removed|started)/,
    cls: "proxy-hl-good",
  },
  { re: /(proxy:\s*\S+)/, cls: "proxy-hl-portal" },
  { re: /(injected:v1|injected:native|pass\([^)]*\)|\bstart\b|\bstop\b)/, cls: "proxy-hl-tag" },
  { re: /(\d+(?:\.\d+)?\s?(?:ms|秒|s)\b)/, cls: "proxy-hl-num" },
];

function hhmmss(stamp: string): string {
  const parts = String(stamp || "").split(" ");
  return parts.length > 1 ? parts[1] : String(stamp || "");
}

function statusClass(code?: number): string {
  const value = Number(code) || 0;
  if (value >= 500) return "st-5xx";
  if (value >= 400) return "st-4xx";
  if (value >= 300) return "st-3xx";
  return "st-2xx";
}

function noteClass(note?: string): string {
  const value = String(note || "");
  if (value.indexOf("injected:native") === 0) return "note-native";
  if (value.indexOf("injected") === 0) return "note-inject";
  if (value.indexOf("non-json") !== -1 || value.indexOf("pass(") === 0 || value.indexOf("busy") === 0) {
    return "note-warn";
  }
  return "note-pass";
}

function highlightText(text: string): ReactNode[] {
  let rest = String(text || "");
  let guard = 0;
  const parts: ReactNode[] = [];
  while (rest && guard++ < 40) {
    let best: { index: number; len: number; cls: string } | null = null;
    for (const rule of TEXT_RULES) {
      const match = rule.re.exec(rest);
      if (match && (!best || match.index < best.index)) {
        best = { index: match.index, len: match[0].length, cls: rule.cls };
      }
    }
    if (!best) break;
    if (best.index > 0) parts.push(rest.slice(0, best.index));
    parts.push(
      <span className={best.cls} key={parts.length}>
        {rest.slice(best.index, best.index + best.len)}
      </span>,
    );
    rest = rest.slice(best.index + best.len);
  }
  if (rest) parts.push(rest);
  return parts;
}

function PathText({ path }: { path?: string }) {
  const raw = String(path || "");
  if (!raw) return null;
  const cut = raw.indexOf("?");
  const target = cut === -1 ? raw : raw.slice(0, cut);
  const query = cut === -1 ? "" : raw.slice(cut);
  const match = /^(\/(?:api|v1))(\/.*)?$/.exec(target);
  return (
    <span className="proxy-logpath">
      {match ? (
        <>
          <span className={`proxy-path-root ${match[1] === "/v1" ? "root-v1" : "root-api"}`}>
            {match[1]}
          </span>
          {match[2] || null}
        </>
      ) : (
        target
      )}
      {query ? <span className="proxy-path-query">{query}</span> : null}
    </span>
  );
}

function LogEntryRow({ entry }: { entry: PortalLogEntry }) {
  const rowClass =
    "proxy-logrow " +
    (LEVEL_CLASS[entry.level] || "") +
    (entry.kind === "request" ? "" : " plain");

  if (entry.kind === "request") {
    return (
      <div className={rowClass} title={entry.text}>
        <span className="proxy-logtime">{hhmmss(entry.time)}</span>
        <span className={`proxy-verb ${VERB_CLASS[entry.method || ""] || ""}`}>
          {entry.method || ""}
        </span>
        <PathText path={entry.path} />
        <span className="proxy-arrow">→</span>
        <span className={`proxy-log-status ${statusClass(entry.status)}`}>
          {String(entry.status ?? "")}
        </span>
        {entry.note ? (
          <span className={`proxy-note ${noteClass(entry.note)}`}>{entry.note}</span>
        ) : null}
        {entry.client ? <span className="proxy-client">{entry.client}</span> : null}
      </div>
    );
  }

  return (
    <div className={rowClass} title={entry.text}>
      <span className="proxy-logtime">{hhmmss(entry.time)}</span>
      <span className="proxy-logtext">{highlightText(entry.text)}</span>
    </div>
  );
}

function usePortalLog(disabled: boolean) {
  const [entries, setEntries] = useState<PortalLogEntry[]>([]);
  const [paused, setPaused] = useState(false);
  const [failed, setFailed] = useState(false);
  const [capacity, setCapacity] = useState(100);
  const listRef = useRef<HTMLDivElement | null>(null);
  const seqRef = useRef(0);
  const pausedRef = useRef(false);
  const inflightRef = useRef(false);
  const disabledRef = useRef(disabled);
  const stickRef = useRef(true);

  useEffect(() => {
    pausedRef.current = paused;
  }, [paused]);

  useEffect(() => {
    disabledRef.current = disabled;
  }, [disabled]);

  const tick = useCallback(async () => {
    if (disabledRef.current || pausedRef.current || inflightRef.current) return;
    inflightRef.current = true;
    try {
      const data = await getPortalLog(seqRef.current);
      const stale =
        seqRef.current > 0 &&
        ((data.oldest > 0 && seqRef.current < data.oldest - 1) ||
          (data.latest > 0 && data.latest < seqRef.current));
      const batch = data.entries || [];
      const cap = data.capacity > 0 ? data.capacity : 100;
      if (!seqRef.current || stale) {
        seqRef.current = 0;
        setEntries(batch.slice(-cap));
      } else if (batch.length) {
        setEntries((previous) => [...previous, ...batch].slice(-cap));
      }
      for (const entry of batch) {
        if (entry.seq > seqRef.current) seqRef.current = entry.seq;
      }
      setCapacity(cap);
      setFailed(false);
    } catch {
      setFailed(true);
    } finally {
      inflightRef.current = false;
    }
  }, []);

  useEffect(() => {
    if (disabled) return;
    void tick();
    const timer = window.setInterval(() => void tick(), 1000);
    return () => window.clearInterval(timer);
  }, [disabled, tick]);

  useLayoutEffect(() => {
    const node = listRef.current;
    if (node && stickRef.current) node.scrollTop = node.scrollHeight;
  }, [entries]);

  const onScroll = useCallback(() => {
    const node = listRef.current;
    if (!node) return;
    stickRef.current = node.scrollHeight - node.scrollTop - node.clientHeight < 24;
  }, []);

  const togglePause = useCallback(() => {
    setPaused((current) => {
      const next = !current;
      pausedRef.current = next;
      if (!next) void tick();
      return next;
    });
  }, [tick]);

  return { entries, paused, failed, capacity, listRef, onScroll, togglePause, refresh: tick };
}

export default function ProxyPage({
  portal,
  onOpenSettings,
}: {
  portal: PortalController;
  onOpenSettings?: () => void;
}) {
  const { t } = useI18n();
  const [form, setForm] = useState<Form | null>(null);
  const [message, setMessage] = useState<Message>({ text: "", kind: "" });
  const [autostartMessage, setAutostartMessage] = useState<Message>({ text: "", kind: "" });
  const [saving, setSaving] = useState(false);
  const [autostartBusy, setAutostartBusy] = useState(false);

  const disabled = portal.stopped || !!portal.state?.stopping;
  const log = usePortalLog(disabled);

  useEffect(() => {
    if (portal.state && !form) setForm(formFromState(portal.state));
  }, [portal.state, form]);

  const paramsMeta = useMemo(
    () => (form ? jsonMeta(form.params, t, "params") : { text: "", bad: false }),
    [form?.params, t],
  );
  const profilesMeta = useMemo(
    () => (form ? jsonMeta(form.profiles, t, "profiles") : { text: "", bad: false }),
    [form?.profiles, t],
  );

  if (!portal.state && portal.error) {
    return (
      <main className="proxy-shell">
        <p className="proxy-error">
          {t("proxy.loadFailed")}: {portal.error}
        </p>
      </main>
    );
  }
  if (!portal.state || !form) {
    return (
      <main className="proxy-shell">
        <p className="proxy-loading">{t("proxy.loading")}</p>
      </main>
    );
  }

  const state = portal.state;
  const autostart = state.autostart;
  const autostartLabel = !autostart.supported
    ? t("proxy.autostartMacOnly")
    : autostart.installed
      ? autostart.loaded
        ? t("proxy.autostartInstalledLoaded")
        : t("proxy.autostartInstalledNotLoaded")
      : t("proxy.autostartNotInstalled");

  const logLabel = disabled
    ? t("proxy.liveStopped")
    : log.paused
      ? t("proxy.livePaused")
      : t("proxy.liveEverySecond");
  const logCount = log.failed
    ? t("proxy.logsFailed")
    : log.entries.length
      ? t("proxy.logsCount", { n: log.entries.length })
      : t("proxy.logsCapacity", { n: log.capacity });

  function update<K extends keyof Form>(key: K, value: Form[K]) {
    setForm((current) => (current ? { ...current, [key]: value } : current));
  }

  async function save() {
    if (!form) return;
    setMessage({ text: "", kind: "" });

    let params: unknown;
    try {
      params = JSON.parse(form.params || "{}");
    } catch (err) {
      setMessage({ text: t("proxy.paramsInvalid", { msg: errorMessage(err) }), kind: "err" });
      return;
    }
    if (!isObject(params)) {
      setMessage({ text: t("proxy.paramsNotObject"), kind: "err" });
      return;
    }

    let profiles: unknown;
    try {
      profiles = JSON.parse(form.profiles || "{}");
    } catch (err) {
      setMessage({ text: t("proxy.profilesInvalid", { msg: errorMessage(err) }), kind: "err" });
      return;
    }
    if (!isObject(profiles)) {
      setMessage({ text: t("proxy.profilesNotObject"), kind: "err" });
      return;
    }

    setSaving(true);
    try {
      const next = await savePortalConfig({
        upstream: form.upstream.trim(),
        timeout: Number.parseFloat(form.timeout),
        params,
        max_concurrent: Number.parseInt(form.maxConcurrent, 10),
        max_body_size: Number.parseInt(form.maxBodySize, 10),
        thinking: form.thinking,
        system_prompt: {
          mode: form.systemMode,
          text: form.systemText,
        },
        profile: form.profile.trim(),
        profiles,
      });
      portal.setState(next);
      setForm(formFromState(next));
      setMessage({ text: t("proxy.saved"), kind: "ok" });
      void log.refresh();
    } catch (err) {
      setMessage({ text: errorMessage(err), kind: "err" });
    } finally {
      setSaving(false);
    }
  }

  function applyDefaults() {
    const defaults = state.defaults;
    if (!defaults) {
      setMessage({ text: t("proxy.noDefaults"), kind: "err" });
      return;
    }
    setForm((current) =>
      current
        ? {
            ...current,
            upstream: defaults.upstream,
            timeout: String(defaults.timeout),
            params: JSON.stringify(defaults.params ?? {}, null, 2),
            maxConcurrent: String(defaults.max_concurrent),
            maxBodySize: String(defaults.max_body_size),
            thinking: defaults.thinking || "off",
            systemMode: defaults.system_prompt?.mode || "off",
            systemText: defaults.system_prompt?.text || "",
          }
        : current,
    );
    setMessage({ text: t("proxy.defaultsFilled"), kind: "ok" });
  }

  async function changeAutostart(install: boolean) {
    setAutostartBusy(true);
    setAutostartMessage({
      text: install ? t("proxy.installing") : t("proxy.removing"),
      kind: "",
    });
    try {
      const next = await togglePortalAutostart(install);
      portal.setState(next);
      setAutostartMessage({ text: next.message, kind: next.ok ? "ok" : "err" });
    } catch (err) {
      setAutostartMessage({ text: errorMessage(err), kind: "err" });
      void portal.refresh();
    } finally {
      setAutostartBusy(false);
    }
  }

  const notice = portal.stopped
    ? { text: t("proxy.stopped"), kind: "ok" as const }
    : message.text
      ? message
      : portal.error
        ? { text: portal.error, kind: "err" as const }
        : { text: "", kind: "" as const };

  return (
    <main className="proxy-shell">
      <div className="proxy-col proxy-left">
        <section className="panel proxy-settings">
          <div className="proxy-settings-scroll">
            <section className="proxy-section">
              <div className="proxy-section-head">
                <h2>{t("proxy.panelConnection")}</h2>
                <span className="aside-note">{t("proxy.panelConnectionNote")}</span>
              </div>
              <div className="proxy-section-body">
                <label>
                  <span>{t("proxy.fieldUpstream")}</span>
                  <input
                    type="text"
                    spellCheck={false}
                    autoComplete="off"
                    value={form.upstream}
                    onChange={(event) => update("upstream", event.target.value)}
                  />
                </label>
                <div className="proxy-grid proxy-grid-3">
                  <label>
                    <span>{t("proxy.fieldTimeout")}</span>
                    <input
                      type="number"
                      min={1}
                      step={1}
                      value={form.timeout}
                      onChange={(event) => update("timeout", event.target.value)}
                    />
                  </label>
                  <label>
                    <span>{t("proxy.fieldMaxConcurrent")}</span>
                    <input
                      type="number"
                      min={1}
                      step={1}
                      value={form.maxConcurrent}
                      onChange={(event) => update("maxConcurrent", event.target.value)}
                    />
                  </label>
                  <label>
                    <span>{t("proxy.fieldMaxBodySize")}</span>
                    <input
                      type="number"
                      min={1}
                      step={1}
                      value={form.maxBodySize}
                      onChange={(event) => update("maxBodySize", event.target.value)}
                    />
                  </label>
                </div>
              </div>
            </section>

            <section className="proxy-section">
              <div className="proxy-section-head">
                <h2>{t("proxy.panelRewrite")}</h2>
                <span className="aside-note">{t("proxy.panelPolicyNote")}</span>
              </div>
              <div className="proxy-section-body">
                <div className="proxy-grid proxy-grid-2">
                  <label>
                    <span>{t("proxy.fieldThinking")}</span>
                    <select
                      value={form.thinking}
                      onChange={(event) => update("thinking", event.target.value)}
                    >
                      <option value="off">{t("proxy.thinkingOff")}</option>
                      <option value="on">{t("proxy.thinkingOn")}</option>
                      <option value="passthrough">{t("proxy.thinkingPassthrough")}</option>
                    </select>
                  </label>
                  <label>
                    <span>{t("proxy.fieldSystemMode")}</span>
                    <select
                      value={form.systemMode}
                      onChange={(event) => update("systemMode", event.target.value)}
                    >
                      <option value="off">{t("proxy.promptOff")}</option>
                      <option value="inject-if-missing">{t("proxy.promptInject")}</option>
                      <option value="override">{t("proxy.promptOverride")}</option>
                    </select>
                  </label>
                </div>
                <label className="proxy-growable">
                  <span>{t("proxy.fieldSystemText")}</span>
                  <textarea
                    spellCheck={false}
                    autoComplete="off"
                    value={form.systemText}
                    onChange={(event) => update("systemText", event.target.value)}
                  />
                </label>
                <label className="proxy-growable">
                  <span className="proxy-label-row">
                    <span>{t("proxy.fieldParams")}</span>
                    <span className={`aside-note ${paramsMeta.bad ? "bad" : "good"}`}>
                      {paramsMeta.text}
                    </span>
                  </span>
                  <textarea
                    spellCheck={false}
                    autoComplete="off"
                    placeholder='{"max_tokens": 96, "temperature": 0.2}'
                    value={form.params}
                    onChange={(event) => update("params", event.target.value)}
                  />
                </label>
              </div>
            </section>

            <section className="proxy-section">
              <div className="proxy-section-head">
                <h2>{t("proxy.panelProfiles")}</h2>
                <span className="aside-note">{t("proxy.panelProfilesNote")}</span>
              </div>
              <div className="proxy-section-body">
                <div className="proxy-split">
                  <label>
                    <span>{t("proxy.fieldProfile")}</span>
                    <input
                      type="text"
                      spellCheck={false}
                      autoComplete="off"
                      placeholder="nothink / raw / thinking"
                      value={form.profile}
                      onChange={(event) => update("profile", event.target.value)}
                    />
                  </label>
                  <label className="proxy-growable">
                    <span className="proxy-label-row">
                      <span>{t("proxy.fieldProfiles")}</span>
                      <span className={`aside-note ${profilesMeta.bad ? "bad" : "good"}`}>
                        {profilesMeta.text}
                      </span>
                    </span>
                    <textarea
                      spellCheck={false}
                      autoComplete="off"
                      value={form.profiles}
                      onChange={(event) => update("profiles", event.target.value)}
                    />
                  </label>
                </div>
              </div>
            </section>

            <section className="proxy-section">
              <div className="proxy-section-head">
                <h2>{t("proxy.panelHistoryMoved")}</h2>
                {onOpenSettings ? (
                  <button className="ghost small proxy-section-action" onClick={onOpenSettings}>
                    {t("proxy.openSettings")}
                  </button>
                ) : null}
              </div>
              <div className="proxy-section-body">
                <p className="proxy-hint">{t("proxy.historyMovedNote")}</p>
              </div>
            </section>

            <section className="proxy-section">
              <div className="proxy-section-head">
                <h2>{t("proxy.panelAutostart")}</h2>
                <span className="aside-note">{t("proxy.panelAutostartNote")}</span>
              </div>
              <div className="proxy-section-body">
                <div className="proxy-switch-row">
                  <label className="proxy-switch">
                    <input
                      type="checkbox"
                      checked={autostart.installed}
                      disabled={
                        !autostart.supported ||
                        !state.launch_ready ||
                        disabled ||
                        autostartBusy
                      }
                      onChange={(event) => void changeAutostart(event.target.checked)}
                    />
                    <span className="proxy-track">
                      <span className="proxy-knob" />
                    </span>
                    <span className="proxy-switch-label">{t("proxy.fieldAutostart")}</span>
                  </label>
                  <span className={`proxy-switch-state ${autostart.installed ? "on" : "off"}`}>
                    {autostartLabel}
                  </span>
                </div>
                <p className="proxy-pathline">
                  <FitPath value={autostart.plist_path} />
                </p>
                {!autostart.supported ? (
                  <p className="proxy-hint">{t("proxy.hintNoAutostart")}</p>
                ) : null}
                {autostartMessage.text ? (
                  <p className={`proxy-msg ${autostartMessage.kind}`} role="status">
                    {autostartMessage.text}
                  </p>
                ) : null}
              </div>
            </section>
          </div>

          <div className="proxy-footer">
            {notice.text ? (
              <span className={`proxy-msg ${notice.kind}`} role="status">
                {notice.text}
              </span>
            ) : null}
            <div className="proxy-footer-actions">
              <button className="ghost" onClick={applyDefaults} disabled={disabled}>
                {t("proxy.defaults")}
              </button>
              <button onClick={() => void save()} disabled={saving || disabled}>
                {t("proxy.save")}
              </button>
            </div>
          </div>
        </section>
      </div>

      <div className="proxy-col proxy-right">
        <section className="panel proxy-logs">
          <div className="panel-head">
            <h2>{t("proxy.panelLogs")}</h2>
            <span className={`proxy-live ${log.paused || disabled ? "paused" : ""}`}>
              <i className="proxy-pulse" />
              <span>{logLabel}</span>
            </span>
            <span className="spacer" />
            <span className="chip" title={log.failed ? logCount : undefined}>
              {logCount}
            </span>
            <button
              className="ghost small"
              onClick={log.togglePause}
              disabled={disabled}
            >
              {log.paused ? t("proxy.resume") : t("proxy.pause")}
            </button>
          </div>
          <div
            ref={log.listRef}
            className={`proxy-loglist ${log.entries.length ? "" : "is-empty"}`}
            onScroll={log.onScroll}
          >
            {log.entries.length ? (
              log.entries.map((entry) => <LogEntryRow key={entry.seq} entry={entry} />)
            ) : (
              <p className="proxy-log-empty">{t("proxy.logsEmpty")}</p>
            )}
          </div>
        </section>
      </div>
    </main>
  );
}
