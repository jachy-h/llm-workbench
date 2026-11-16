import { useEffect, useState } from "react";
import { getHealth } from "./api";
import ExperimentsPage from "./ExperimentsPage";
import { I18nProvider, useI18n } from "./i18n";
import PlaygroundPage from "./PlaygroundPage";
import RequestsPage from "./RequestsPage";
import type { Health, RequestDetail } from "./types";

type View = "requests" | "playground" | "experiments";

const views: View[] = ["requests", "playground", "experiments"];

function formatBytes(bytes?: number): string {
  if (!bytes) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function viewFromHash(): View {
  const raw = window.location.hash.replace(/^#/, "");
  return views.includes(raw as View) ? (raw as View) : "requests";
}

function Shell() {
  const { lang, toggle, t } = useI18n();
  const [view, setView] = useState<View>(viewFromHash);
  const [selected, setSelected] = useState<string | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [seed, setSeed] = useState<RequestDetail | null>(null);

  useEffect(() => {
    const onHash = () => setView(viewFromHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      getHealth()
        .then((value) => {
          if (!cancelled) setHealth(value);
        })
        .catch(() => {
          // the navigation still works when health is temporarily unavailable
        });
    };
    load();
    const timer = window.setInterval(load, 5000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, []);

  function openRequest(id: string) {
    setSelected(id);
    setView("requests");
    window.location.hash = "requests";
  }

  function createExperimentFromRequest(detail: RequestDetail) {
    setSeed(detail);
    setView("experiments");
    window.location.hash = "experiments";
  }

  return (
    <div className="app">
      <header className="bar">
        <a className="brand" href="#requests" aria-label="LLM Workbench">
          <span className="mark" aria-hidden="true">LW</span>
          <div>
            <h1>LLM Workbench</h1>
            <p>{t("app.tagline")}</p>
          </div>
        </a>
        <nav className="nav" aria-label="LLM Workbench">
          <a href="/_/llm-proxy">{t("nav.proxy")}</a>
          {views.map((name) => (
            <a key={name} href={`#${name}`} aria-current={view === name ? "page" : undefined}>
              {t(`nav.${name}`)}
            </a>
          ))}
        </nav>
        <div className="chips">
          <span className="chip">{health ? `v${health.version}` : "v…"}</span>
          {health ? (
            <>
              <span className="chip">{t("chip.requests", { n: health.storage.count })}</span>
              <span className="chip">{formatBytes(health.storage.size_bytes)}</span>
              <span className={health.inspector.capture_bodies ? "chip on" : "chip"}>
                {health.inspector.capture_bodies ? t("chip.bodiesOn") : t("chip.bodiesOff")}
              </span>
            </>
          ) : null}
        </div>
        <div className="actions">
          <span className={health ? "status" : "status stopped"}>
            <i className="dot" />
            <span>{health ? t("status.connected") : t("status.connecting")}</span>
          </span>
          <button className="ghost small" onClick={toggle} title={t("action.lang")}>
            {lang === "en" ? "中文" : "EN"}
          </button>
        </div>
      </header>

      {view === "requests" ? (
        <RequestsPage
          selected={selected}
          onSelect={setSelected}
          health={health}
          onCreateExperiment={createExperimentFromRequest}
        />
      ) : view === "playground" ? (
        <PlaygroundPage onOpenRequest={openRequest} />
      ) : (
        <ExperimentsPage
          seed={seed}
          onSeedConsumed={() => setSeed(null)}
          onOpenRequest={openRequest}
        />
      )}
    </div>
  );
}

export default function App() {
  return (
    <I18nProvider>
      <Shell />
    </I18nProvider>
  );
}
