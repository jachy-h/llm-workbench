import { useCallback, useEffect, useState } from "react";
import { errorMessage, getPortalState, shutdownPortal, type PortalState } from "./portalApi";

export type PortalController = {
  state: PortalState | null;
  error: string;
  stopped: boolean;
  refresh: () => Promise<void>;
  setState: (next: PortalState) => void;
  shutdown: () => void;
};

// usePortal loads the settings state only while the proxy page is visible.
// The React shell is served at both /_/llm-proxy and /_/workbench/, so the
// page owns its own copy of this state.
export function usePortal(enabled: boolean): PortalController {
  const [state, setState] = useState<PortalState | null>(null);
  const [error, setError] = useState("");
  const [stopped, setStopped] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const next = await getPortalState();
      setState(next);
      setError("");
    } catch (err) {
      setError(errorMessage(err));
    }
  }, []);

  useEffect(() => {
    if (enabled) void refresh();
  }, [enabled, refresh]);

  const shutdown = useCallback(() => {
    // Show "stopping" while the request is in flight; the process may exit
    // before the response arrives.
    setState((current) => (current ? { ...current, stopping: true } : current));
    shutdownPortal()
      .catch(() => {
        // expected when the server closes the connection first
      })
      .finally(() => {
        setStopped(true);
      });
  }, []);

  return { state, error, stopped, refresh, setState, shutdown };
}
