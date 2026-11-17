import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { APIError, errorMessage, request } from "./client";

export const POLL_INTERVAL = 5000;
const MAX_CURSOR_HISTORY = 20;

interface ReadState<T> {
  path: string | null;
  data?: T;
  loading: boolean;
  error?: string;
  updatedAt?: Date;
  stale: boolean;
}

// The effect owns its request, timer and visibility listener. Its disposed flag
// suppresses even fetch implementations that ignore AbortSignal on navigation.
export function usePolling<T>(path: string | null, unauthorized: () => void) {
  const [state, setState] = useState<ReadState<T>>({
    path,
    loading: true,
    stale: false,
  });
  const [generation, setGeneration] = useState(0);
  // biome-ignore lint/correctness/useExhaustiveDependencies: manual refresh restarts this effect and aborts its previous read.
  useEffect(() => {
    if (!path) return;
    let disposed = false;
    let controller: AbortController | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const clearTimer = () => {
      if (timer !== undefined) {
        clearTimeout(timer);
        timer = undefined;
      }
    };
    const run = async () => {
      if (disposed || document.hidden || controller) return;
      const active = new AbortController();
      controller = active;
      setState((previous) =>
        previous.path === path
          ? { ...previous, loading: !previous.data }
          : { path, loading: true, stale: false },
      );
      try {
        const data = await request<T>(path, { signal: active.signal });
        if (!disposed && !active.signal.aborted)
          setState({
            path,
            data,
            loading: false,
            updatedAt: new Date(),
            stale: false,
          });
      } catch (error) {
        if (!disposed && !active.signal.aborted) {
          if (error instanceof APIError && error.status === 401) unauthorized();
          setState((previous) => ({
            ...(previous.path === path ? previous : { path }),
            loading: false,
            error: errorMessage(error),
            stale: true,
          }));
        }
      } finally {
        if (controller === active) controller = null;
        if (!disposed && !document.hidden) {
          clearTimer();
          timer = setTimeout(() => {
            void run();
          }, POLL_INTERVAL);
        }
      }
    };
    const visibility = () => {
      clearTimer();
      if (document.hidden) {
        controller?.abort();
        setState((previous) =>
          previous.path === path
            ? { ...previous, stale: true, loading: false }
            : previous,
        );
      } else if (!controller) {
        void run();
      }
    };
    document.addEventListener("visibilitychange", visibility);
    void run();
    return () => {
      disposed = true;
      clearTimer();
      controller?.abort();
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [path, unauthorized, generation]);
  const refresh = useCallback(() => setGeneration((value) => value + 1), []);
  return {
    ...(state.path === path ? state : { path, loading: true, stale: false }),
    refresh,
  };
}

export function usePager(key: string) {
  const [state, setState] = useState<{
    key: string;
    cursor: string | null;
    history: (string | null)[];
  }>({ key, cursor: null, history: [] });
  const current =
    state.key === key ? state : { key, cursor: null, history: [] };
  return {
    cursor: current.cursor,
    canGoBack: current.history.length > 0,
    retainedCursors: current.history.length,
    next: (cursor: string | null) => {
      if (cursor !== null)
        setState({
          key,
          cursor,
          history: [...current.history, current.cursor].slice(
            -MAX_CURSOR_HISTORY,
          ),
        });
    },
    previous: () => {
      if (current.history.length)
        setState({
          key,
          cursor: current.history.at(-1) ?? null,
          history: current.history.slice(0, -1),
        });
    },
  };
}

const subscribeRoute = (notify: () => void) => {
  window.addEventListener("hashchange", notify);
  return () => window.removeEventListener("hashchange", notify);
};
export function useRoute() {
  const hash = useSyncExternalStore(
    subscribeRoute,
    () => window.location.hash,
    () => "",
  );
  const match =
    /^#\/(events|endpoints)\/([a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})$/i.exec(
      hash,
    );
  return match
    ? { kind: match[1], id: match[2] }
    : { kind: "overview", id: "" };
}
