import { useEffect, useMemo, useState } from "react";
import { deleteRequest, exportURL, getRequest, listRequests } from "./api";
import Select from "./components/Select";
import TokenUsage from "./components/TokenUsage";
import { useI18n } from "./i18n";
import type { Filters, Health, RequestDetail, RequestList } from "./types";

const initialFilters: Filters = { q: "", source: "", status: "", limit: 100, offset: 0 };
type Tab = "request" | "forwarded" | "response" | "raw";

function formatTime(value: string | undefined, lang: string): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(lang === "zh" ? "zh-CN" : "en-US");
}

function formatDuration(ms?: number): string {
  if (!ms || ms < 0) return "—";
  if (ms < 1000) return `${ms} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

function pretty(value?: string): string {
  if (!value) return "";
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}

function statusClass(status?: number): string {
  if (!status) return "status-muted";
  if (status >= 500) return "status-error";
  if (status >= 400) return "status-warn";
  return "status-ok";
}

type Props = {
  selected: string | null;
  onSelect: (id: string | null) => void;
  health: Health | null;
  onCreateExperiment: (detail: RequestDetail) => void;
};

export default function RequestsPage({ selected, onSelect, health, onCreateExperiment }: Props) {
  const { lang, t } = useI18n();
  const [filters, setFilters] = useState<Filters>(initialFilters);
  const [list, setList] = useState<RequestList>({ items: [], total: 0, limit: 100, offset: 0 });
  const [detail, setDetail] = useState<RequestDetail | null>(null);
  const [tab, setTab] = useState<Tab>("request");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [live, setLive] = useState(false);

  async function refresh(next = filters) {
    setLoading(true);
    setError(null);
    try {
      setList(await listRequests(next));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh(filters);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filters]);

  useEffect(() => {
    if (!live) return;
    const timer = window.setInterval(() => void refresh(filters), 3000);
    return () => window.clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, filters]);

  useEffect(() => {
    if (!selected) {
      setDetail(null);
      return;
    }
    let cancelled = false;
    let attempts = 0;
    const load = () => {
      getRequest(selected)
        .then((value) => {
          if (!cancelled) setDetail(value);
        })
        .catch((err: unknown) => {
          if (cancelled) return;
          // The inspector writes asynchronously; give it a moment after a
          // playground call jumps straight to the detail view.
          if (attempts++ < 8) {
            window.setTimeout(load, 200);
            return;
          }
          setError(err instanceof Error ? err.message : String(err));
        });
    };
    load();
    return () => {
      cancelled = true;
    };
  }, [selected]);

  const exportHref = useMemo(() => exportURL(filters, selected ?? undefined), [filters, selected]);

  async function removeSelected() {
    if (!detail) return;
    if (!window.confirm(t("requests.confirmDelete", { id: detail.id }))) return;
    try {
      await deleteRequest(detail.id);
      onSelect(null);
      setDetail(null);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  function changeFilters(patch: Partial<Filters>) {
    setFilters((current) => ({ ...current, ...patch, offset: patch.offset ?? 0 }));
  }

  const tabs: Array<{ id: Tab; label: string }> = [
    { id: "request", label: t("requests.tabRequest") },
    { id: "forwarded", label: t("requests.tabForwarded") },
    { id: "response", label: t("requests.tabResponse") },
    { id: "raw", label: t("requests.tabRaw") },
  ];

  return (
    <>
      <div className="toolbar">
        <input
          className="search"
          placeholder={t("requests.search")}
          value={filters.q}
          onChange={(event) => changeFilters({ q: event.target.value })}
        />
        <Select
          value={filters.source}
          onChange={(source) => changeFilters({ source })}
          options={[
            { value: "", label: t("requests.sourceAll") },
            { value: "proxy", label: "proxy" },
            { value: "playground", label: "playground" },
            { value: "experiment", label: "experiment" },
          ]}
        />
        <input
          className="status-input"
          placeholder={t("requests.statusHint")}
          value={filters.status}
          onChange={(event) => changeFilters({ status: event.target.value })}
        />
        <label className="live">
          <input type="checkbox" checked={live} onChange={(event) => setLive(event.target.checked)} />
          {t("requests.live")}
        </label>
        <button onClick={() => void refresh()} disabled={loading}>
          {loading ? t("common.loading") : t("action.refresh")}
        </button>
        <a className="button" href={exportHref} download>
          {t("action.export")}
        </a>
      </div>

      {error ? <div className="error">{error}</div> : null}

      <main className="requests">
        <section className="panel list-panel">
          <div className="panel-head">
            <h2>{t("nav.requests")}</h2>
            <span className="aside-note">{t("requests.matching", { n: list.total })}</span>
            <span className="spacer" />
            <span className="chip">
              {list.total === 0
                ? "0"
                : `${list.offset + 1}–${Math.min(list.offset + list.items.length, list.total)}`}
            </span>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t("common.time")}</th>
                  <th>{t("common.model")}</th>
                  <th>{t("common.endpoint")}</th>
                  <th>{t("common.status")}</th>
                  <th>{t("common.latency")}</th>
                  <th>{t("common.ttft")}</th>
                  <th>{t("common.tokens")}</th>
                </tr>
              </thead>
              <tbody>
                {list.items.map((item) => (
                  <tr
                    key={item.id}
                    className={selected === item.id ? "selected" : ""}
                    onClick={() => {
                      onSelect(item.id);
                      setTab("request");
                    }}
                  >
                    <td className="mono">{formatTime(item.created_at, lang)}</td>
                    <td>
                      <div className="model">{item.model || "—"}</div>
                      <div className="subtle">
                        {item.provider} · {item.source}
                      </div>
                    </td>
                    <td className="mono small">{item.endpoint}</td>
                    <td>
                      <span className={`status ${statusClass(item.status_code)}`}>
                        {item.status_code || "—"}
                      </span>
                      {item.stream ? <span className="tag">stream</span> : null}
                      {item.note ? <span className="tag note">{item.note}</span> : null}
                    </td>
                    <td className="mono">{formatDuration(item.duration_ms)}</td>
                    <td className="mono">{formatDuration(item.ttft_ms)}</td>
                    <td className="mono small">
                      <TokenUsage
                        prompt={item.prompt_tokens}
                        completion={item.completion_tokens}
                        total={item.total_tokens}
                      />
                    </td>
                  </tr>
                ))}
                {list.items.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="empty">
                      {t("requests.empty")}
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
          <div className="pager">
            <button
              disabled={filters.offset === 0}
              onClick={() => changeFilters({ offset: Math.max(0, filters.offset - filters.limit) })}
            >
              {t("action.previous")}
            </button>
            <button
              disabled={filters.offset + filters.limit >= list.total}
              onClick={() => changeFilters({ offset: filters.offset + filters.limit })}
            >
              {t("action.next")}
            </button>
          </div>
        </section>

        <aside className="panel detail-panel">
          {detail ? (
            <>
              <div className="detail-head">
                <div>
                  <h2>{detail.model || t("nav.requests")}</h2>
                  <div className="mono subtle">{detail.id}</div>
                </div>
                <div className="detail-actions">
                  <button className="accent" onClick={() => onCreateExperiment(detail)}>
                    {t("action.experiment")}
                  </button>
                  <button className="danger" onClick={() => void removeSelected()}>
                    {t("action.delete")}
                  </button>
                </div>
              </div>

              <div className="facts">
                <div><span>{t("common.time")}</span><strong>{formatTime(detail.created_at, lang)}</strong></div>
                <div><span>{t("common.source")}</span><strong>{detail.source}</strong></div>
                <div><span>{t("common.endpoint")}</span><strong className="mono">{detail.endpoint}</strong></div>
                <div><span>{t("common.status")}</span><strong>{detail.status_code ?? "—"}</strong></div>
                <div><span>{t("common.duration")}</span><strong>{formatDuration(detail.duration_ms)}</strong></div>
                <div><span>{t("common.ttft")}</span><strong>{formatDuration(detail.ttft_ms)}</strong></div>
                <div>
                  <span>{t("common.tokens")}</span>
                  <TokenUsage
                    prompt={detail.prompt_tokens}
                    completion={detail.completion_tokens}
                    total={detail.total_tokens}
                  />
                </div>
                <div><span>{t("common.client")}</span><strong>{detail.client || "—"}</strong></div>
              </div>

              {detail.error ? <div className="error">{detail.error}</div> : null}
              {detail.response_truncated ? <div className="notice">{t("requests.truncated")}</div> : null}

              <div className="tabs">
                {tabs.map((item) => (
                  <button key={item.id} className={tab === item.id ? "active" : ""} onClick={() => setTab(item.id)}>
                    {item.label}
                  </button>
                ))}
              </div>

              <pre className="code">
                {tab === "request"
                  ? pretty(detail.request_original) || t("requests.bodyOff")
                  : tab === "forwarded"
                    ? pretty(detail.request_forwarded) || t("requests.bodyMissing")
                    : tab === "response"
                      ? pretty(detail.response_body) || t("requests.bodyMissing")
                      : JSON.stringify(detail, null, 2)}
              </pre>
            </>
          ) : (
            <div className="detail-empty">
              <h2>{t("requests.title")}</h2>
              <p>{t("requests.intro")}</p>
              {health ? (
                <div className="detail-meta">
                  <div><span>{t("requests.database")}</span><code>{health.storage.path}</code></div>
                  <div><span>{t("requests.written")}</span>{health.inspector.written}</div>
                  <div><span>{t("requests.dropped")}</span>{health.inspector.dropped}</div>
                </div>
              ) : null}
            </div>
          )}
        </aside>
      </main>
    </>
  );
}
