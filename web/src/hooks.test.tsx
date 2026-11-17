import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { POLL_INTERVAL, usePager, usePolling } from "./hooks";

const json = (data: unknown, status = 200) =>
  ({
    status,
    ok: status >= 200 && status < 300,
    text: async () => JSON.stringify(data),
  }) as Response;
const visible = (hidden: boolean) => {
  Object.defineProperty(document, "hidden", {
    configurable: true,
    value: hidden,
  });
  document.dispatchEvent(new Event("visibilitychange"));
};
const unauthorized = vi.fn();

beforeEach(() => {
  vi.useFakeTimers();
  unauthorized.mockReset();
  visible(false);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("bounded polling lifecycle", () => {
  it("waits five seconds after completion and keeps only one request active", async () => {
    let resolve: ((response: Response) => void) | undefined;
    const fetch = vi.fn(
      () =>
        new Promise<Response>((value) => {
          resolve = value;
        }),
    );
    vi.stubGlobal("fetch", fetch);
    const hook = renderHook(() =>
      usePolling<{ value: string }>("/slow", unauthorized),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    await act(async () => {
      resolve?.(json({ value: "complete" }));
    });
    expect(hook.result.current.data?.value).toBe("complete");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_INTERVAL - 1);
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(fetch).toHaveBeenCalledTimes(2);
    await act(async () => {
      resolve?.(json({ value: "second" }));
    });
    hook.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("aborts navigation and rejects stale responses even when transport ignores abort", async () => {
    const pending: {
      signal: AbortSignal;
      resolve: (response: Response) => void;
    }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string, options: RequestInit) =>
          new Promise<Response>((resolve) => {
            pending.push({ signal: options.signal as AbortSignal, resolve });
          }),
      ),
    );
    const hook = renderHook(
      ({ path }: { path: string }) =>
        usePolling<{ name: string }>(path, unauthorized),
      { initialProps: { path: "/old" } },
    );
    hook.rerender({ path: "/new" });
    expect(pending[0].signal.aborted).toBe(true);
    expect(hook.result.current.data).toBeUndefined();
    await act(async () => {
      pending[1].resolve(json({ name: "new" }));
    });
    await act(async () => {
      pending[0].resolve(json({ name: "old" }));
    });
    expect(hook.result.current.data?.name).toBe("new");
    hook.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("pauses hidden tabs, resumes them and removes listeners/timers on unmount", async () => {
    const fetch = vi.fn(async () => json({ value: "visible" }));
    vi.stubGlobal("fetch", fetch);
    const remove = vi.spyOn(document, "removeEventListener");
    const hook = renderHook(() => usePolling("/visible", unauthorized));
    await act(async () => {});
    act(() => visible(true));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_INTERVAL * 4);
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    await act(async () => visible(false));
    expect(fetch).toHaveBeenCalledTimes(2);
    hook.unmount();
    expect(vi.getTimerCount()).toBe(0);
    expect(remove).toHaveBeenCalledWith(
      "visibilitychange",
      expect.any(Function),
    );
    remove.mockRestore();
  });

  it("retains a last page as stale on failure and clears auth on 401", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(json({ value: "saved" }))
      .mockResolvedValueOnce(
        json({ title: "Unavailable", detail: "Deadline elapsed" }, 503),
      )
      .mockResolvedValueOnce(json({ title: "Unauthorized" }, 401));
    vi.stubGlobal("fetch", fetch);
    const hook = renderHook(() =>
      usePolling<{ value: string }>("/failure", unauthorized),
    );
    await act(async () => {});
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_INTERVAL);
    });
    expect(hook.result.current.stale).toBe(true);
    expect(hook.result.current.error).toBe("Deadline elapsed");
    expect(hook.result.current.data?.value).toBe("saved");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_INTERVAL);
    });
    expect(unauthorized).toHaveBeenCalledTimes(1);
  });

  it("manual refresh aborts an active read before starting the replacement", async () => {
    const signals: AbortSignal[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string, options: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            const signal = options.signal as AbortSignal;
            signals.push(signal);
            signal.addEventListener(
              "abort",
              () => reject(new DOMException("Aborted", "AbortError")),
              { once: true },
            );
          }),
      ),
    );
    const hook = renderHook(() => usePolling("/refresh", unauthorized));
    await act(async () => hook.result.current.refresh());
    expect(signals).toHaveLength(2);
    expect(signals[0].aborted).toBe(true);
    await act(async () => hook.unmount());
    expect(signals[1].aborted).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });
});

it("bounds cursor history to 20 and resets it when filters change", () => {
  const hook = renderHook(({ key }: { key: string }) => usePager(key), {
    initialProps: { key: "all" },
  });
  for (let i = 0; i < 100; i++)
    act(() => hook.result.current.next(`page-${i}`));
  expect(hook.result.current.retainedCursors).toBe(20);
  for (let i = 0; i < 20; i++) act(() => hook.result.current.previous());
  expect(hook.result.current.canGoBack).toBe(false);
  hook.rerender({ key: "filtered" });
  expect(hook.result.current.cursor).toBeNull();
  expect(hook.result.current.retainedCursors).toBe(0);
});
