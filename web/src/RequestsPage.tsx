import { useEffect, useMemo, useState } from "react";
import { deleteRequest, exportURL, getRequest, listRequests } from "./api";
import type { Filters, Health, RequestDetail, RequestList, RequestSummary } from "./types";

const initialFilters: Filters = { q: "", source: "", status: "", limit: 100, offset: 0 };
type Tab = "request" | "forwarded" | "response" | "raw";

function formatTime(value?: string): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function formatDuration(ms?: number): string {
  if (!ms || ms < 0) return "—";
  if (ms < 1000) return `${ms} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

function formatTokens(summary: RequestSummary): string {
  if (summary.total_tokens == null) return "—";
  return `${summary.prompt_tokens ?? "?"} / ${summary.completion_tokens ?? "?"} / ${summary.total_tokens}`;
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
    if (!window.confirm(`Delete ${detail.id}?`)) return;
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

  return (
    <>
      <div className="toolbar">
        <input
          className="search"
          placeholder="Search prompt, model, body…"
          value={filters.q}
          onChange={(event) => changeFilters({ q: event.target.value })}
        />
        <select value={filters.source} onChange={(event) => changeFilters({ source: event.target.value })}>
          <option value="">all sources</option>
          <option value="proxy">proxy</option>
          <option value="playground">playground</option>
          <option value="experiment">experiment</option>
        </select>
        <input
          className="status-input"
          placeholder="status"
          value={filters.status}
          onChange={(event) => changeFilters({ status: event.target.value })}
        />
        <label className="live">
          <input type="checkbox" checked={live} onChange={(event) => setLive(event.target.checked)} />
          live
        </label>
        <button onClick={() => void refresh()} disabled={loading}>
          {loading ? "loading…" : "refresh"}
        </button>
        <a className="button" href={exportHref} download>
          export
        </a>
      </div>

      {error ? <div className="error">{error}</div> : null}

      <main className="content">
        <section className="list-panel">
          <div className="list-head">
            <span>{list.total} matching</span>
            <span>
              {list.total === 0
                ? "0"
                : `${list.offset + 1}–${Math.min(list.offset + list.items.length, list.total)}`}
            </span>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>time</th>
                  <th>model</th>
                  <th>endpoint</th>
                  <th>status</th>
                  <th>latency</th>
                  <th>ttft</th>
                  <th>tokens</th>
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
                    <td className="mono">{formatTime(item.created_at)}</td>
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
                    <td className="mono small">{formatTokens(item)}</td>
                  </tr>
                ))}
                {list.items.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="empty">
                      No requests recorded yet. Use the Playground or point a client at this proxy.
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
              previous
            </button>
            <button
              disabled={filters.offset + filters.limit >= list.total}
              onClick={() => changeFilters({ offset: filters.offset + filters.limit })}
            >
              next
            </button>
          </div>
        </section>

        <aside className="detail-panel">
          {detail ? (
            <>
              <div className="detail-head">
                <div>
                  <h2>{detail.model || "request"}</h2>
                  <div className="mono subtle">{detail.id}</div>
                </div>
                <div className="detail-actions">
                  <button className="accent" onClick={() => onCreateExperiment(detail)}>experiment</button>
                  <button className="danger" onClick={() => void removeSelected()}>delete</button>
                </div>
              </div>

              <div className="facts">
                <div><span>time</span><strong>{formatTime(detail.created_at)}</strong></div>
                <div><span>source</span><strong>{detail.source}</strong></div>
                <div><span>endpoint</span><strong className="mono">{detail.endpoint}</strong></div>
                <div><span>status</span><strong>{detail.status_code ?? "—"}</strong></div>
                <div><span>duration</span><strong>{formatDuration(detail.duration_ms)}</strong></div>
                <div><span>ttft</span><strong>{formatDuration(detail.ttft_ms)}</strong></div>
                <div><span>tokens</span><strong>{formatTokens(detail)}</strong></div>
                <div><span>client</span><strong>{detail.client || "—"}</strong></div>
              </div>

              {detail.error ? <div className="error">{detail.error}</div> : null}
              {detail.response_truncated ? <div className="notice">Body was truncated at the capture limit.</div> : null}

              <div className="tabs">
                {(["request", "forwarded", "response", "raw"] as Tab[]).map((name) => (
                  <button key={name} className={tab === name ? "active" : ""} onClick={() => setTab(name)}>
                    {name}
                  </button>
                ))}
              </div>

              <pre className="code">
                {tab === "request"
                  ? pretty(detail.request_original) || "Body not recorded (inspector.capture_bodies is off)."
                  : tab === "forwarded"
                    ? pretty(detail.request_forwarded) || "Body not recorded."
                    : tab === "response"
                      ? pretty(detail.response_body) || "Body not recorded."
                      : JSON.stringify(detail, null, 2)}
              </pre>
            </>
          ) : (
            <div className="detail-empty">
              <h2>Request detail</h2>
              <p>Select a row to inspect the original request, what was forwarded upstream, and the raw response.</p>
              {health ? (
                <div className="detail-meta">
                  <div><span>database</span><code>{health.storage.path}</code></div>
                  <div><span>written</span>{health.inspector.written}</div>
                  <div><span>dropped</span>{health.inspector.dropped}</div>
                </div>
              ) : null}
            </div>
          )}
        </aside>
      </main>
    </>
  );
}
