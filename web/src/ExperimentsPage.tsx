import { useEffect, useMemo, useState } from "react";
import {
  cancelExperiment,
  createExperiment,
  deleteExperiment,
  experimentExportURL,
  getExperiment,
  listExperiments,
  listModels,
  listProviders,
  listRuns,
  runExperiment,
  rateRun,
} from "./api";
import SearchSelect from "./components/SearchSelect";
import { useI18n } from "./i18n";
import { toModelOptions } from "./modelOptions";
import type {
  ChatMessage,
  Experiment,
  ExperimentInput,
  ExperimentSummary,
  ModelInfo,
  ProviderInfo,
  RequestDetail,
  Run,
} from "./types";

type Props = {
  seed: RequestDetail | null;
  onSeedConsumed: () => void;
  onOpenRequest: (id: string) => void;
};

type VariantForm = { provider: string; model: string; system: string; params: string };
type CaseForm = { name: string; prompt: string; messages: ChatMessage[] | null };

function emptyVariant(provider = ""): VariantForm {
  return { provider, model: "", system: "", params: '{"temperature": 0.2}' };
}

function emptyCase(): CaseForm {
  return { name: "", prompt: "", messages: null };
}

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

function lastUserText(messages: ChatMessage[]): string {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index].role === "user") return messages[index].content;
  }
  return messages[messages.length - 1]?.content ?? "";
}

function formatTime(value: string | undefined, lang: string): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(lang === "zh" ? "zh-CN" : "en-US");
}

function formatDuration(ms?: number): string {
  if (!ms) return "—";
  if (ms < 1000) return `${ms} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

export default function ExperimentsPage({ seed, onSeedConsumed, onOpenRequest }: Props) {
  const { lang, t } = useI18n();
  const [items, setItems] = useState<ExperimentSummary[]>([]);
  const [selected, setSelected] = useState<string>("");
  const [experiment, setExperiment] = useState<Experiment | null>(null);
  const [runs, setRuns] = useState<Run[]>([]);
  const [active, setActive] = useState(false);
  const [batch, setBatch] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [formOpen, setFormOpen] = useState(false);
  const [providers, setProviders] = useState<ProviderInfo[]>([]);
  const [models, setModels] = useState<Record<string, ModelInfo[]>>({});
  const [name, setName] = useState("");
  const [notes, setNotes] = useState("");
  const [variants, setVariants] = useState<VariantForm[]>([emptyVariant()]);
  const [cases, setCases] = useState<CaseForm[]>([emptyCase()]);

  async function refreshList() {
    try {
      setItems(await listExperiments());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function loadProviders() {
    try {
      const response = await listProviders();
      setProviders(response.items);
      const preferred = response.items.find((item) => item.default) ?? response.items[0];
      if (preferred) {
        setVariants((current) => current.map((variant) => (
          variant.provider ? variant : { ...variant, provider: preferred.id }
        )));
      }
      const entries = await Promise.all(response.items.map(async (provider) => {
        try {
          return [provider.id, await listModels(provider.id)] as const;
        } catch {
          return [provider.id, []] as const;
        }
      }));
      setModels(Object.fromEntries(entries));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  useEffect(() => {
    void refreshList();
    void loadProviders();
  }, []);

  useEffect(() => {
    if (!seed) return;
    const messages = messagesFromRequest(seed);
    setName(t("experiments.fromModel", { model: seed.model || t("requests.title") }));
    setNotes(t("experiments.fromRequest", { id: seed.id }));
    setVariants([{
      provider: seed.provider || "ollama",
      model: seed.model || "",
      system: seed.system_prompt || "",
      params: "{}",
    }]);
    setCases([{
      name: t("experiments.fromRequestCase"),
      prompt: lastUserText(messages),
      messages: messages.length ? messages : null,
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

  function resetForm() {
    setName("");
    setNotes("");
    setVariants([emptyVariant(providers.find((item) => item.default)?.id ?? providers[0]?.id ?? "")]);
    setCases([emptyCase()]);
    setError(null);
  }

  function closeForm() {
    setFormOpen(false);
    resetForm();
  }

  function openForm() {
    resetForm();
    setFormOpen(true);
  }

  async function create() {
    setError(null);
    if (!name.trim()) {
      setError(t("experiments.nameRequired"));
      return;
    }
    const usableVariants = variants.filter((variant) => variant.model.trim());
    if (!usableVariants.length) {
      setError(t("experiments.modelRequired"));
      return;
    }
    if (usableVariants.some((variant) => !variant.provider.trim())) {
      setError(t("experiments.providerRequired"));
      return;
    }
    if (cases.some((item) => !item.prompt.trim() && !item.messages?.length)) {
      setError(t("experiments.promptRequired"));
      return;
    }
    let invalidParams = false;
    const parsedVariants = usableVariants.map((variant) => {
      const params = parseJSON<Record<string, unknown> | null>(variant.params || "{}", null);
      if (params === null) invalidParams = true;
      return {
        provider: variant.provider.trim(),
        model: variant.model.trim(),
        system: variant.system,
        params: params ?? {},
      };
    });
    if (invalidParams) {
      setError(t("experiments.paramsInvalid"));
      return;
    }
    const parsedCases = cases.map((item, index) => ({
      name: item.name.trim() || `${t("experiments.case")} ${index + 1}`,
      messages: item.messages?.length
        ? item.messages
        : [{ role: "user" as const, content: item.prompt.trim() }],
    }));
    const input: ExperimentInput = { name: name.trim(), notes, variants: parsedVariants, cases: parsedCases };
    try {
      const created = await createExperiment(input);
      setFormOpen(false);
      resetForm();
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
    if (!window.confirm(t("experiments.confirmDelete", { name: experiment.name }))) return;
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

  async function setRating(run: Run, rating: number) {
    try {
      const updated = await rateRun(run.id, rating || null, run.rating_note ?? "");
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

  const runsByCase = useMemo(() => {
    const map = new Map<string, Run[]>();
    runs.forEach((run) => {
      const current = map.get(run.case_id) ?? [];
      current.push(run);
      map.set(run.case_id, current);
    });
    return map;
  }, [runs]);

  const totalRuns = experiment ? experiment.variants.length * experiment.cases.length : 0;

  return (
    <main className="experiments">
      <aside className="panel exp-list">
        <div className="panel-head">
          <h2>{t("experiments.title")}</h2>
          <span className="spacer" />
          <button className="ghost small" onClick={() => (formOpen ? closeForm() : openForm())}>
            {formOpen ? t("action.close") : t("action.new")}
          </button>
        </div>
        <div className="panel-body exp-list-body">
          {items.map((item) => (
            <button
              key={item.id}
              className={`exp-item ${selected === item.id ? "active" : ""}`}
              onClick={() => {
                setFormOpen(false);
                void selectExperiment(item.id);
              }}
            >
              <strong>{item.name}</strong>
              <span>{t("experiments.meta", {
                v: item.variant_count,
                c: item.case_count,
                r: item.run_count,
              })}</span>
              <span>{formatTime(item.updated_at, lang)}</span>
            </button>
          ))}
          {!items.length ? <p className="subtle exp-empty">{t("experiments.emptyList")}</p> : null}
        </div>
      </aside>

      <section className="panel exp-main">
        <div className="exp-main-body">
          {formOpen ? (
            <div className="exp-form">
              <div className="form-intro">
                <h2>{t("experiments.new")}</h2>
                <p>{t("experiments.formIntro")}</p>
              </div>

              <section className="form-step">
                <div className="step-head">
                  <span className="step-num">1</span>
                  <div>
                    <h3>{t("experiments.stepBasics")}</h3>
                    <p>{t("experiments.stepBasicsHint")}</p>
                  </div>
                </div>
                <div className="field-grid">
                  <label>
                    {t("experiments.name")}
                    <input
                      value={name}
                      placeholder={t("experiments.namePlaceholder")}
                      onChange={(event) => setName(event.target.value)}
                    />
                  </label>
                  <label>
                    {t("experiments.notes")}
                    <input
                      value={notes}
                      placeholder={t("experiments.notesPlaceholder")}
                      onChange={(event) => setNotes(event.target.value)}
                    />
                  </label>
                </div>
              </section>

              <section className="form-step">
                <div className="step-head">
                  <span className="step-num">2</span>
                  <div>
                    <h3>{t("experiments.stepModels")}</h3>
                    <p>{t("experiments.stepModelsHint")}</p>
                  </div>
                </div>
                <div className="model-rows">
                  {variants.map((variant, index) => (
                    <div className="model-row" key={index}>
                      <label>
                        {t("experiments.provider")}
                        <select
                          value={variant.provider}
                          onChange={(event) => setVariants((current) => current.map((item, i) => (
                            i === index ? { ...item, provider: event.target.value, model: "" } : item
                          )))}
                        >
                          <option value="">—</option>
                          {providers.map((provider) => (
                            <option key={provider.id} value={provider.id}>{provider.name}</option>
                          ))}
                        </select>
                      </label>
                      <label>
                        {t("experiments.model")}
                        <SearchSelect
                          value={variant.model}
                          options={toModelOptions(models[variant.provider] ?? [])}
                          placeholder={t("experiments.modelPlaceholder")}
                          onChange={(model) => setVariants((current) => current.map((item, i) => (
                            i === index ? { ...item, model } : item
                          )))}
                        />
                      </label>
                      <button
                        className="ghost small remove"
                        onClick={() => setVariants((current) => current.filter((_, i) => i !== index))}
                        disabled={variants.length === 1}
                      >
                        {t("action.remove")}
                      </button>
                      <details className="advanced">
                        <summary>{t("experiments.advanced")}</summary>
                        <div className="advanced-grid">
                          <label>
                            {t("experiments.system")}
                            <textarea
                              value={variant.system}
                              placeholder={t("experiments.systemPlaceholder")}
                              onChange={(event) => setVariants((current) => current.map((item, i) => (
                                i === index ? { ...item, system: event.target.value } : item
                              )))}
                            />
                          </label>
                          <label>
                            {t("experiments.params")}
                            <input
                              value={variant.params}
                              placeholder='{"temperature": 0.2}'
                              onChange={(event) => setVariants((current) => current.map((item, i) => (
                                i === index ? { ...item, params: event.target.value } : item
                              )))}
                            />
                          </label>
                        </div>
                      </details>
                    </div>
                  ))}
                </div>
                <button className="ghost add-row" onClick={() => setVariants((current) => [...current, emptyVariant(variants[0]?.provider ?? "")])}>
                  + {t("experiments.addModel")}
                </button>
              </section>

              <section className="form-step">
                <div className="step-head">
                  <span className="step-num">3</span>
                  <div>
                    <h3>{t("experiments.stepPrompts")}</h3>
                    <p>{t("experiments.stepPromptsHint")}</p>
                  </div>
                </div>
                <div className="case-rows">
                  {cases.map((item, index) => (
                    <div className="case-row" key={index}>
                      <label>
                        {t("experiments.promptName")}
                        <input
                          value={item.name}
                          placeholder={t("experiments.promptNamePlaceholder")}
                          onChange={(event) => setCases((current) => current.map((value, i) => (
                            i === index ? { ...value, name: event.target.value } : value
                          )))}
                        />
                      </label>
                      <label>
                        {t("experiments.prompt")}
                        <textarea
                          value={item.prompt}
                          placeholder={t("experiments.promptPlaceholder")}
                          onChange={(event) => setCases((current) => current.map((value, i) => (
                            i === index ? { ...value, prompt: event.target.value, messages: null } : value
                          )))}
                        />
                      </label>
                      <button
                        className="ghost small remove"
                        onClick={() => setCases((current) => current.filter((_, i) => i !== index))}
                        disabled={cases.length === 1}
                      >
                        {t("action.remove")}
                      </button>
                    </div>
                  ))}
                </div>
                <button className="ghost add-row" onClick={() => setCases((current) => [...current, emptyCase()])}>
                  + {t("experiments.addPrompt")}
                </button>
              </section>

              <div className="form-actions">
                <button className="ghost" onClick={closeForm}>{t("action.cancel")}</button>
                <button onClick={() => void create()}>{t("action.create")}</button>
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
                  <button onClick={() => void startRun()} disabled={active}>{t("action.run")}</button>
                  {active ? <button onClick={() => void stopRun()}>{t("action.stop")}</button> : null}
                  <a className="button" href={experimentExportURL(experiment.id, "json")} download>{t("action.json")}</a>
                  <a className="button" href={experimentExportURL(experiment.id, "csv")} download>{t("action.csv")}</a>
                  <button className="danger" onClick={() => void removeExperiment()}>{t("action.delete")}</button>
                </div>
              </div>

              <div className="exp-summary">
                <span>{t("common.variants", { n: experiment.variants.length })}</span>
                <span className="times">×</span>
                <span>{t("common.cases", { n: experiment.cases.length })}</span>
                <span className="times">=</span>
                <strong>{t("experiments.runsTotal", { n: totalRuns })}</strong>
                {active ? <span className="chip on">{t("experiments.running")}</span> : null}
              </div>

              {error ? <div className="error">{error}</div> : null}
              {loading ? <div className="subtle">{t("common.loading")}</div> : null}

              {runs.length === 0 ? (
                <div className="run-empty">
                  <h3>{t("experiments.readyTitle")}</h3>
                  <p>{t("experiments.readyBody")}</p>
                </div>
              ) : null}

              <div className="result-list">
                {experiment.cases.map((item, index) => {
                  const caseRuns = runsByCase.get(item.id) ?? [];
                  const prompt = lastUserText(item.messages);
                  return (
                    <section className="result-case" key={item.id}>
                      <header className="result-case-head">
                        <div className="result-case-title">
                          <span className="step-num small">{index + 1}</span>
                          <h3>{item.name}</h3>
                        </div>
                        <p>{prompt || "—"}</p>
                      </header>
                      <div className="run-grid">
                        {caseRuns.length ? caseRuns.map((run) => {
                          const variant = variantByID.get(run.variant_id);
                          const ok = run.status === "succeeded";
                          const warn = run.status === "failed";
                          return (
                            <article className="run-card" key={run.id}>
                              <div className="run-card-head">
                                <div>
                                  <strong>{variant?.model || "—"}</strong>
                                  <span>{variant?.provider}</span>
                                </div>
                                <span className={`status ${ok ? "status-ok" : warn ? "status-error" : "status-muted"}`}>
                                  {t(`experiments.status.${run.status}`)}
                                </span>
                              </div>
                              {run.error ? <p className="run-error">{run.error}</p> : null}
                              <div className="run-answer">
                                <h4>{t("experiments.answer")}</h4>
                                <pre>{run.output || "—"}</pre>
                                {run.thinking ? (
                                  <details className="run-thinking">
                                    <summary>{t("experiments.thinking")}</summary>
                                    <pre>{run.thinking}</pre>
                                  </details>
                                ) : null}
                              </div>
                              <div className="run-card-foot">
                                <span className="mono">{formatDuration(run.duration_ms)}</span>
                                <label className="run-rating">
                                  {t("common.rating")}
                                  <select
                                    value={run.rating ?? 0}
                                    onChange={(event) => void setRating(run, Number(event.target.value))}
                                  >
                                    <option value={0}>—</option>
                                    {[1, 2, 3, 4, 5].map((value) => <option key={value} value={value}>{value}</option>)}
                                  </select>
                                </label>
                                {run.request_id ? (
                                  <button className="ghost small" onClick={() => onOpenRequest(run.request_id!)}>
                                    {t("action.inspect")}
                                  </button>
                                ) : null}
                              </div>
                            </article>
                          );
                        }) : <p className="run-pending">{t("experiments.casePending")}</p>}
                      </div>
                    </section>
                  );
                })}
              </div>
            </>
          ) : (
            <div className="exp-empty exp-welcome">
              <h2>{t("experiments.emptyTitle")}</h2>
              <p>{t("experiments.emptyBody")}</p>
              <div className="welcome-steps">
                <div><span>1</span><p>{t("experiments.welcomeStep1")}</p></div>
                <div><span>2</span><p>{t("experiments.welcomeStep2")}</p></div>
                <div><span>3</span><p>{t("experiments.welcomeStep3")}</p></div>
              </div>
              <button onClick={openForm}>{t("experiments.new")}</button>
            </div>
          )}
          {!formOpen && !experiment && error ? <div className="error">{error}</div> : null}
        </div>
      </section>
    </main>
  );
}
