import { useEffect, useState } from "react";
import { getHealth } from "./api";
import ExperimentsPage from "./ExperimentsPage";
import PlaygroundPage from "./PlaygroundPage";
import RequestsPage from "./RequestsPage";
import type { Health, RequestDetail } from "./types";

type View = "requests" | "playground" | "experiments";

function formatBytes(bytes?: number): string {
  if (!bytes) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export default function App() {
  const [view, setView] = useState<View>("requests");
  const [selected, setSelected] = useState<string | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [seed, setSeed] = useState<RequestDetail | null>(null);

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
  }

  function createExperimentFromRequest(detail: RequestDetail) {
    setSeed(detail);
    setView("experiments");
  }

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <span className="brand-mark">LW</span>
          <div>
            <h1>LLM Workbench</h1>
            <p>Local-first proxy &amp; experiment workbench</p>
          </div>
        </div>
        <nav className="nav">
          <button
            className={`nav-item ${view === "requests" ? "active" : ""}`}
            onClick={() => setView("requests")}
          >
            Requests
          </button>
          <button
            className={`nav-item ${view === "playground" ? "active" : ""}`}
            onClick={() => setView("playground")}
          >
            Playground
          </button>
          <button
            className={`nav-item ${view === "experiments" ? "active" : ""}`}
            onClick={() => setView("experiments")}
          >
            Experiments
          </button>
        </nav>
        <div className="health">
          {health ? (
            <>
              <span>{health.storage.count} requests</span>
              <span>{formatBytes(health.storage.size_bytes)}</span>
              <span className={health.inspector.capture_bodies ? "pill on" : "pill"}>
                {health.inspector.capture_bodies ? "bodies on" : "bodies off"}
              </span>
            </>
          ) : (
            <span>connecting…</span>
          )}
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
