import { useEffect, useState } from "react";
import { useI18n } from "./i18n";
import { errorMessage, savePortalConfig, type PortalState } from "./portalApi";
import type { Health } from "./types";
import type { PortalController } from "./usePortal";

type Form = {
  enabled: boolean;
  captureBodies: boolean;
  retentionDays: string;
  maxRows: string;
  maxBodyBytes: string;
  dbPath: string;
};

type Message = {
  text: string;
  kind: "" | "ok" | "err";
};

function formFromState(state: PortalState): Form {
  return {
    enabled: !!state.inspector?.enabled,
    captureBodies: !!state.inspector?.capture_bodies,
    retentionDays: String(state.inspector?.retention_days ?? 0),
    maxRows: String(state.inspector?.max_rows ?? 0),
    maxBodyBytes: String(state.inspector?.max_body_bytes ?? 0),
    dbPath: state.storage?.path || "",
  };
}

function FitPath({ value }: { value: string }) {
  return (
    <span className="proxy-fit" title={value}>
      {value || "—"}
    </span>
  );
}

export default function SettingsPage({
  portal,
  health,
}: {
  portal: PortalController;
  health: Health | null;
}) {
  const { t } = useI18n();
  const [form, setForm] = useState<Form | null>(null);
  const [message, setMessage] = useState<Message>({ text: "", kind: "" });
  const [saving, setSaving] = useState(false);

  const disabled = portal.stopped || !!portal.state?.stopping;

  useEffect(() => {
    if (portal.state && !form) setForm(formFromState(portal.state));
  }, [portal.state, form]);

  if (!portal.state && portal.error) {
    return (
      <main className="settings-shell">
        <p className="proxy-error">
          {t("settings.loadFailed")}: {portal.error}
        </p>
      </main>
    );
  }
  if (!portal.state || !form) {
    return (
      <main className="settings-shell">
        <p className="proxy-loading">{t("settings.loading")}</p>
      </main>
    );
  }

  const state = portal.state;
  // The inspector reads its settings once at startup, so the saved config and
  // the running process can differ until the next restart.
  const pendingRestart =
    !!health &&
    !!health.storage.path &&
    (health.inspector.enabled !== !!state.inspector?.enabled ||
      health.inspector.capture_bodies !== !!state.inspector?.capture_bodies);

  function update<K extends keyof Form>(key: K, value: Form[K]) {
    setForm((current) => (current ? { ...current, [key]: value } : current));
  }

  async function save() {
    if (!form) return;
    setMessage({ text: "", kind: "" });
    setSaving(true);
    try {
      const next = await savePortalConfig({
        inspector: {
          enabled: form.enabled,
          capture_bodies: form.captureBodies,
          retention_days: Number.parseInt(form.retentionDays, 10) || 0,
          max_rows: Number.parseInt(form.maxRows, 10) || 0,
          max_body_bytes: Number.parseInt(form.maxBodyBytes, 10) || 0,
        },
        storage: { path: form.dbPath.trim() },
      });
      portal.setState(next);
      setForm(formFromState(next));
      setMessage({ text: t("settings.saved"), kind: "ok" });
    } catch (err) {
      setMessage({ text: errorMessage(err), kind: "err" });
    } finally {
      setSaving(false);
    }
  }

  function applyDefaults() {
    const defaults = state.defaults;
    if (!defaults) {
      setMessage({ text: t("settings.noDefaults"), kind: "err" });
      return;
    }
    setForm((current) =>
      current
        ? {
            ...current,
            enabled: !!defaults.inspector?.enabled,
            captureBodies: !!defaults.inspector?.capture_bodies,
            retentionDays: String(defaults.inspector?.retention_days ?? 0),
            maxRows: String(defaults.inspector?.max_rows ?? 0),
            maxBodyBytes: String(defaults.inspector?.max_body_bytes ?? 0),
            dbPath: defaults.storage?.path || "",
          }
        : current,
    );
    setMessage({ text: t("settings.defaultsFilled"), kind: "ok" });
  }

  const notice = portal.stopped
    ? { text: t("settings.stopped"), kind: "ok" as const }
    : message.text
      ? message
      : portal.error
        ? { text: portal.error, kind: "err" as const }
        : { text: "", kind: "" as const };

  return (
    <main className="settings-shell">
      <div className="settings-page">
        <header className="settings-head">
          <div className="settings-intro">
            <h2>{t("settings.introTitle")}</h2>
            <p>{t("settings.introBody")}</p>
          </div>
          <div className="settings-actions">
            {notice.text ? (
              <span className={`proxy-msg ${notice.kind}`} role="status">
                {notice.text}
              </span>
            ) : pendingRestart ? (
              <span className="proxy-msg warn">{t("settings.pendingRestartShort")}</span>
            ) : null}
            <button className="ghost" onClick={applyDefaults} disabled={disabled}>
              {t("settings.defaults")}
            </button>
            <button onClick={() => void save()} disabled={saving || disabled}>
              {t("settings.save")}
            </button>
          </div>
        </header>

        {pendingRestart ? (
          <div className="settings-banner" role="status">
            <strong>{t("settings.pendingRestartTitle")}</strong>
            <span>{t("settings.pendingRestartBody")}</span>
          </div>
        ) : null}

        <div className="settings-grid">
          <section className="panel proxy-settings settings-section">
            <div className="panel-head">
              <h2>{t("settings.panelHistory")}</h2>
              <span className="aside-note">{t("settings.restartNote")}</span>
            </div>
            <div className="panel-body">
              <div className="proxy-toggle-grid">
                <label className="proxy-switch proxy-toggle">
                  <input
                    type="checkbox"
                    checked={form.enabled}
                    onChange={(event) => update("enabled", event.target.checked)}
                  />
                  <span className="proxy-track">
                    <span className="proxy-knob" />
                  </span>
                  <span className="proxy-switch-label">
                    {t("settings.fieldInspectorEnabled")}
                  </span>
                </label>
                <label className="proxy-switch proxy-toggle">
                  <input
                    type="checkbox"
                    checked={form.captureBodies}
                    onChange={(event) => update("captureBodies", event.target.checked)}
                  />
                  <span className="proxy-track">
                    <span className="proxy-knob" />
                  </span>
                  <span className="proxy-switch-label">
                    {t("settings.fieldCaptureBodies")}
                  </span>
                </label>
              </div>
              <p className="proxy-hint">{t("settings.captureHint")}</p>
              <div className="proxy-grid proxy-grid-3">
                <label>
                  <span>{t("settings.fieldRetentionDays")}</span>
                  <input
                    type="number"
                    min={0}
                    step={1}
                    value={form.retentionDays}
                    onChange={(event) => update("retentionDays", event.target.value)}
                  />
                </label>
                <label>
                  <span>{t("settings.fieldMaxRows")}</span>
                  <input
                    type="number"
                    min={0}
                    step={1}
                    value={form.maxRows}
                    onChange={(event) => update("maxRows", event.target.value)}
                  />
                </label>
                <label>
                  <span>{t("settings.fieldMaxBodyBytes")}</span>
                  <input
                    type="number"
                    min={0}
                    step={1}
                    value={form.maxBodyBytes}
                    onChange={(event) => update("maxBodyBytes", event.target.value)}
                  />
                </label>
              </div>
            </div>
          </section>

          <section className="panel proxy-settings settings-section">
            <div className="panel-head">
              <h2>{t("settings.panelStorage")}</h2>
              <span className="aside-note">{t("settings.restartNote")}</span>
            </div>
            <div className="panel-body">
              <label>
                <span>{t("settings.fieldDb")}</span>
                <input
                  type="text"
                  spellCheck={false}
                  autoComplete="off"
                  value={form.dbPath}
                  onChange={(event) => update("dbPath", event.target.value)}
                />
              </label>
              <dl className="proxy-facts">
                <div>
                  <dt>{t("settings.fieldConfig")}</dt>
                  <dd>
                    <FitPath value={state.config_path} />
                  </dd>
                </div>
                <div>
                  <dt>{t("settings.fieldLog")}</dt>
                  <dd>
                    <FitPath value={state.log_path} />
                  </dd>
                </div>
              </dl>
            </div>
          </section>
        </div>
      </div>
    </main>
  );
}
