import { useEffect, useState } from "react";
import type { MouseEvent } from "react";
import { getHealth } from "./api";
import ExperimentsPage from "./ExperimentsPage";
import { I18nProvider, useI18n } from "./i18n";
import PlaygroundPage from "./PlaygroundPage";
import ProxyPage from "./ProxyPage";
import RequestsPage from "./RequestsPage";
import SettingsPage from "./SettingsPage";
import { getPortalPID, savePortalLang } from "./portalApi";
import type { Health, RequestDetail } from "./types";
import { usePortal } from "./usePortal";

type View = "proxy" | "requests" | "playground" | "experiments" | "settings";

const views: View[] = ["requests", "playground", "experiments", "settings"];

function formatBytes(bytes?: number): string {
  if (!bytes) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function viewFromLocation(): View {
  const path = window.location.pathname.replace(/\/+$/, "");
  if (path === "/_/llm-proxy") return "proxy";
  const raw = window.location.hash.replace(/^#/, "");
  return views.includes(raw as View) ? (raw as View) : "requests";
}

function stripScheme(url: string): string {
  return String(url || "").replace(/^https?:\/\//, "");
}

function Shell() {
  const { lang, toggle, t } = useI18n();
  const [view, setView] = useState<View>(viewFromLocation);
  const [selected, setSelected] = useState<string | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [seed, setSeed] = useState<RequestDetail | null>(null);
  const portal = usePortal(view === "proxy" || view === "settings");

  useEffect(() => {
    const update = () => setView(viewFromLocation());
    window.addEventListener("hashchange", update);
    window.addEventListener("popstate", update);
    return () => {
      window.removeEventListener("hashchange", update);
      window.removeEventListener("popstate", update);
    };
  }, []);

  useEffect(() => {
    document.title = t(
      view === "proxy"
        ? "proxy.title"
        : view === "settings"
          ? "settings.title"
          : "app.title",
    );
  }, [view, t]);

  // A rebuilt binary keeps the same URL but gets a new pid. Reload the shell
  // so the inline assets and the settings components match the new process.
  useEffect(() => {
    let baseline: number | null = null;
    let inflight = false;
    let leaving = false;
    const tick = async () => {
      if (inflight || leaving) return;
      inflight = true;
      try {
        const { pid } = await getPortalPID();
        if (baseline === null) {
          baseline = pid;
        } else if (pid !== baseline) {
          leaving = true;
          window.location.reload();
        }
      } catch {
        // The old process may be gone and the new one still starting.
      } finally {
        inflight = false;
      }
    };
    void tick();
    const timer = window.setInterval(() => void tick(), 2000);
    return () => window.clearInterval(timer);
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

  function navigate(next: View, event?: MouseEvent<HTMLAnchorElement>) {
    if (event && (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey)) {
      return;
    }
    event?.preventDefault();
    const path = next === "proxy" ? "/_/llm-proxy" : `/_/workbench/#${next}`;
    if (window.location.pathname + window.location.hash !== path) {
      window.history.pushState(null, "", path);
    }
    setView(next);
  }

  function toggleLang() {
    const next = lang === "en" ? "zh" : "en";
    toggle();
    // The server uses its language for log lines and the LaunchAgent comment.
    void savePortalLang(next).catch(() => {
      // The client-side switch still works if the config cannot be written.
    });
  }

  function openRequest(id: string) {
    setSelected(id);
    navigate("requests");
  }

  function createExperimentFromRequest(detail: RequestDetail) {
    setSeed(detail);
    navigate("experiments");
  }

  function shutdown() {
    if (!window.confirm(t("proxy.confirmShutdown"))) return;
    portal.shutdown();
  }

  const proxyStopped = portal.stopped || !!portal.state?.stopping;
  const proxyStatus = !portal.state && !portal.stopped
    ? t("status.connecting")
    : portal.stopped
      ? t("proxy.statusStopped")
      : portal.state?.stopping
        ? t("proxy.statusStopping")
        : t("proxy.statusRunning");

  return (
    <div className="app">
      <header className="bar">
        <a
          className="brand"
          href="/_/workbench/#requests"
          aria-label="LLM Workbench"
          onClick={(event) => navigate("requests", event)}
        >
          <span className="mark" aria-hidden="true">LW</span>
          <div>
            <h1>LLM Workbench</h1>
            <p>{t("app.tagline")}</p>
          </div>
        </a>
        <nav className="nav" aria-label="LLM Workbench">
          <a
            href="/_/llm-proxy"
            aria-current={view === "proxy" ? "page" : undefined}
            onClick={(event) => navigate("proxy", event)}
          >
            {t("nav.proxy")}
          </a>
          {views.map((name) => (
            <a
              key={name}
              href={`/_/workbench/#${name}`}
              aria-current={view === name ? "page" : undefined}
              onClick={(event) => navigate(name, event)}
            >
              {t(`nav.${name}`)}
            </a>
          ))}
        </nav>

        {view === "proxy" ? (
          <>
            <div className="chips">
              <span className="chip">{portal.state ? `v${portal.state.version}` : "v…"}</span>
              <span className="chip">
                {portal.state ? stripScheme(portal.state.listen) : "—"}
              </span>
              <span className="chip">pid {portal.state?.pid ?? "—"}</span>
            </div>
            <div className="actions">
              <span className={proxyStopped ? "status stopped" : "status"}>
                <i className="dot" />
                <span>{proxyStatus}</span>
              </span>
              <button className="ghost small" onClick={toggleLang} title={t("action.lang")}>
                {lang === "en" ? "中文" : "EN"}
              </button>
              <button
                className="danger"
                onClick={shutdown}
                disabled={!portal.state || proxyStopped}
              >
                {t("proxy.shutdown")}
              </button>
            </div>
          </>
        ) : (
          <>
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
              <button className="ghost small" onClick={toggleLang} title={t("action.lang")}>
                {lang === "en" ? "中文" : "EN"}
              </button>
            </div>
          </>
        )}
      </header>

      {view === "proxy" ? (
        <ProxyPage portal={portal} onOpenSettings={() => navigate("settings")} />
      ) : view === "requests" ? (
        <RequestsPage
          selected={selected}
          onSelect={setSelected}
          health={health}
          onCreateExperiment={createExperimentFromRequest}
        />
      ) : view === "playground" ? (
        <PlaygroundPage onOpenRequest={openRequest} />
      ) : view === "experiments" ? (
        <ExperimentsPage
          seed={seed}
          onSeedConsumed={() => setSeed(null)}
          onOpenRequest={openRequest}
        />
      ) : (
        <SettingsPage portal={portal} health={health} />
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
