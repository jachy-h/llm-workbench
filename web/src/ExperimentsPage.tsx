import { useEffect, useMemo, useState } from "react";
import {
  cancelExperiment,
  createExperiment,
  deleteExperiment,
  experimentExportURL,
  getExperiment,
  listExperiments,
  listRuns,
  runExperiment,
  rateRun,
} from "./api";
import type {
  ChatMessage,
  Experiment,
  ExperimentInput,
  ExperimentSummary,
  RequestDetail,
  Run,
} from "./types";

type Props = {
  seed: RequestDetail | null;
  onSeedConsumed: () => void;
  onOpenRequest: (id: string) => void;
};

type VariantForm = { provider: string; model: string; system: string; params: string };
type CaseForm = { name: string; messages: string };

const initialVariant: VariantForm = {
  provider: "ollama",
  model: "",
  system: "",
  params: '{"temperature": 0.2}',
};
const initialCase: CaseForm = {
  name: "case 1",
  messages: '[{"role":"user","content":"Hello"}]',
};

function parseJSON<T>(text: string, fallback: T): T {
  try {
    return JSON.parse(text) as T;
  } catch {
    return fallback;
  }
}

function messagesFromRequest(detail: RequestDetail): ChatMessage[] {
  const raw = detail.request_forwarded || detail.request_original || "";
  const parsed = parseJSON<{ messages?: ChatMessage[] }>(raw, {});
  return Array.isArray(parsed.messages) ? parsed.messages : [];
}

function formatTime(value?: string): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export default function ExperimentsPage({ seed, onSeedConsumed, onOpenRequest }: Props) {
  const [items, setItems] = useState<ExperimentSummary[]>([]);
  const [selected, setSelected] = useState<string>("");
  const [experiment, setExperiment] = useState<Experiment | null>(null);
  const [runs, setRuns] = useState<Run[]>([]);
  const [active, setActive] = useState(false);
  const [batch, setBatch] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [formOpen, setFormOpen] = useState(false);
  const [name, setName] = useState("");
  const [notes, setNotes] = useState("");
  const [variants, setVariants] = useState<VariantForm[]>([initialVariant]);
  const [cases, setCases] = useState<CaseForm[]>([initialCase]);

  async function refreshList() {
    try {
      setItems(await listExperiments());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  useEffect(() => {
    void refreshList();
  }, []);

  useEffect(() => {
    if (!seed) return;
    const messages = messagesFromRequest(seed);
    setName(`From ${seed.model || "request"}`);
    setNotes(`Created from ${seed.id}`);
    setVariants([{
      provider: seed.provider || "ollama",
      model: seed.model || "",
      system: seed.system_prompt || "",
      params: "{}",
    }]);
    setCases([{
      name: "from request",
      messages: JSON.stringify(messages.length ? messages : [{ role: "user", content: "Hello" }], null, 2),
    }]);
    setFormOpen(true);
    onSeedConsumed();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [seed]);

  async function selectExperiment(id: string) {
    setSelected(id);
    setError(null);
    setLoading(true);
    try {
      const detail = await getExperiment(id);
      setExperiment(detail);
      const result = await listRuns(id);
      setRuns(result.items);
      setActive(result.active);
      setBatch(result.items[0]?.batch_id ?? "");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    if (!selected || !batch) return;
    let cancelled = false;
    const timer = window.setInterval(async () => {
      try {
        const result = await listRuns(selected, batch);
        if (cancelled) return;
        setRuns(result.items);
        setActive(result.active);
        if (!result.active) {
          setBatch("");
          window.clearInterval(timer);
        }
      } catch {
        // keep polling on transient errors
      }
    }, 800);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [selected, batch]);

  async function create() {
    setError(null);
    if (!name.trim()) {
      setError("experiment name is required");
      return;
    }
    const parsedVariants = variants.map((variant) => ({
      provider: variant.provider.trim(),
      model: variant.model.trim(),
      system: variant.system,
      params: parseJSON<Record<string, unknown>>(variant.params, {}),
    }));
    const parsedCases = cases.map((item) => ({
      name: item.name || "case",
      messages: parseJSON<ChatMessage[]>(item.messages, []),
    }));
    if (parsedVariants.some((variant) => !variant.provider || !variant.model)) {
      setError("every variant needs provider and model");
      return;
    }
    if (parsedCases.some((item) => !item.messages.length)) {
      setError("every case needs a non-empty messages array");
      return;
    }
    const input: ExperimentInput = { name: name.trim(), notes, variants: parsedVariants, cases: parsedCases };
    try {
      const created = await createExperiment(input);
      setFormOpen(false);
      await refreshList();
      await selectExperiment(created.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function startRun() {
    if (!selected) return;
    setError(null);
    try {
      const result = await runExperiment(selected);
      setBatch(result.batch_id);
      setActive(true);
      const current = await listRuns(selected, result.batch_id);
      setRuns(current.items);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function stopRun() {
    if (!selected) return;
    try {
      await cancelExperiment(selected);
      setActive(false);
      const result = await listRuns(selected);
      setRuns(result.items);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function removeExperiment() {
    if (!experiment) return;
    if (!window.confirm(`Delete experiment ${experiment.name}?`)) return;
    try {
      await deleteExperiment(experiment.id);
      setSelected("");
      setExperiment(null);
      setRuns([]);
      await refreshList();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function setRating(run: Run, rating: number, note: string) {
    try {
      const updated = await rateRun(run.id, rating || null, note);
      setRuns((current) => current.map((item) => (item.id === updated.id ? updated : item)));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  const variantByID = useMemo(() => {
    const map = new Map<string, Experiment["variants"][number]>();
    experiment?.variants.forEach((variant) => map.set(variant.id, variant));
    return map;
  }, [experiment]);
  const caseByID = useMemo(() => {
    const map = new Map<string, Experiment["cases"][number]>();
    experiment?.cases.forEach((item) => map.set(item.id, item));
    return map;
  }, [experiment]);

  return (
    <main className="experiments">
      <aside className="exp-list">
        <div className="exp-list-head">
          <h2>Experiments</h2>
          <button onClick={() => setFormOpen((value) => !value)}>{formOpen ? "close" : "new"}</button>
        </div>
        {items.map((item) => (
          <button
            key={item.id}
            className={`exp-item ${selected === item.id ? "active" : ""}`}
            onClick={() => void selectExperiment(item.id)}
          >
            <strong>{item.name}</strong>
            <span>{item.variant_count} variants · {item.case_count} cases · {item.run_count} runs</span>
            <span>{formatTime(item.updated_at)}</span>
          </button>
        ))}
        {!items.length ? <p className="subtle exp-empty">No experiments yet.</p> : null}
      </aside>

      <section className="exp-main">
        {formOpen ? (
          <div className="exp-form">
            <h2>New experiment</h2>
            <label>Name<input value={name} onChange={(event) => setName(event.target.value)} /></label>
            <label>Notes<input value={notes} onChange={(event) => setNotes(event.target.value)} /></label>

            <h3>Variants</h3>
            {variants.map((variant, index) => (
              <div className="exp-row" key={index}>
                <input placeholder="provider" value={variant.provider}
                  onChange={(event) => setVariants((current) => current.map((item, i) => i === index ? { ...item, provider: event.target.value } : item))} />
                <input placeholder="model" value={variant.model}
                  onChange={(event) => setVariants((current) => current.map((item, i) => i === index ? { ...item, model: event.target.value } : item))} />
                <input placeholder="system prompt" value={variant.system}
                  onChange={(event) => setVariants((current) => current.map((item, i) => i === index ? { ...item, system: event.target.value } : item))} />
                <textarea placeholder="params JSON" value={variant.params}
                  onChange={(event) => setVariants((current) => current.map((item, i) => i === index ? { ...item, params: event.target.value } : item))} />
                <button onClick={() => setVariants((current) => current.filter((_, i) => i !== index))}>remove</button>
              </div>
            ))}
            <button onClick={() => setVariants((current) => [...current, initialVariant])}>add variant</button>

            <h3>Cases</h3>
            {cases.map((item, index) => (
              <div className="exp-row" key={index}>
                <input placeholder="case name" value={item.name}
                  onChange={(event) => setCases((current) => current.map((value, i) => i === index ? { ...value, name: event.target.value } : value))} />
                <textarea placeholder="messages JSON" value={item.messages}
                  onChange={(event) => setCases((current) => current.map((value, i) => i === index ? { ...value, messages: event.target.value } : value))} />
                <button onClick={() => setCases((current) => current.filter((_, i) => i !== index))}>remove</button>
              </div>
            ))}
            <button onClick={() => setCases((current) => [...current, { name: `case ${current.length + 1}`, messages: initialCase.messages }])}>add case</button>

            <div className="button-row">
              <button className="primary" onClick={() => void create()}>create</button>
            </div>
          </div>
        ) : experiment ? (
          <>
            <div className="exp-head">
              <div>
                <h2>{experiment.name}</h2>
                <div className="subtle">{experiment.notes || "—"}</div>
              </div>
              <div className="button-row">
                <button className="primary" onClick={() => void startRun()} disabled={active}>run</button>
                {active ? <button onClick={() => void stopRun()}>stop</button> : null}
                <a className="button" href={experimentExportURL(experiment.id, "json")} download>json</a>
                <a className="button" href={experimentExportURL(experiment.id, "csv")} download>csv</a>
                <button onClick={() => void removeExperiment()}>delete</button>
              </div>
            </div>

            <div className="exp-meta">
              <span>{experiment.variants.length} variants</span>
              <span>{experiment.cases.length} cases</span>
              <span>{runs.length} runs</span>
              {active ? <span className="pill on">running…</span> : null}
            </div>

            <div className="run-table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>variant</th>
                    <th>case</th>
                    <th>status</th>
                    <th>output</th>
                    <th>latency</th>
                    <th>rating</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {runs.map((run) => {
                    const variant = variantByID.get(run.variant_id);
                    const item = caseByID.get(run.case_id);
                    return (
                      <tr key={run.id}>
                        <td>
                          <div className="model">{variant?.model || run.variant_id}</div>
                          <div className="subtle">{variant?.provider}</div>
                        </td>
                        <td>{item?.name || run.case_id}</td>
                        <td>
                          <span className={`status ${run.status === "succeeded" ? "status-ok" : run.status === "failed" ? "status-error" : "status-muted"}`}>
                            {run.status}
                          </span>
                          {run.error ? <div className="subtle status-error">{run.error}</div> : null}
                        </td>
                        <td className="run-output">
                          <details>
                            <summary>{(run.output || "—").slice(0, 120)}</summary>
                            <pre>{run.output || "—"}</pre>
                            {run.thinking ? <><h4>thinking</h4><pre>{run.thinking}</pre></> : null}
                          </details>
                        </td>
                        <td className="mono small">
                          {run.duration_ms ? `${run.duration_ms} ms` : "—"}
                          <div className="subtle">{run.ttft_ms ? `${run.ttft_ms} ms ttft` : ""}</div>
                        </td>
                        <td>
                          <select value={run.rating ?? 0}
                            onChange={(event) => void setRating(run, Number(event.target.value), run.rating_note ?? "")}>
                            <option value={0}>—</option>
                            {[1, 2, 3, 4, 5].map((value) => <option key={value} value={value}>{value}</option>)}
                          </select>
                          <input className="rating-note" placeholder="note" value={run.rating_note ?? ""}
                            onChange={(event) => void setRating(run, run.rating ?? 0, event.target.value)} />
                        </td>
                        <td>
                          {run.request_id ? <button onClick={() => onOpenRequest(run.request_id!)}>inspect</button> : null}
                        </td>
                      </tr>
                    );
                  })}
                  {!runs.length ? (
                    <tr><td colSpan={7} className="empty">No runs yet. Press run to execute every variant × case.</td></tr>
                  ) : null}
                </tbody>
              </table>
            </div>
          </>
        ) : (
          <div className="exp-empty">
            <h2>Experiments</h2>
            <p>Create an experiment from the sidebar, or open a proxy request and press “experiment” to seed one.</p>
          </div>
        )}
        {error ? <div className="error">{error}</div> : null}
        {loading ? <div className="subtle">loading…</div> : null}
      </section>
    </main>
  );
}
