import { afterEach, expect, it, vi } from "vitest";
import { APIError, request } from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
});

it("uses a same-origin cookie and a CSRF nonce without bearer credentials", async () => {
  const fetch = vi.fn(async () => ({ status: 204, ok: true }));
  vi.stubGlobal("fetch", fetch);
  await request("/events/id/replay", {
    method: "POST",
    csrf: "synthetic-nonce",
    body: { expected_replay_count: 3 },
  });
  const [url, options] = fetch.mock.calls[0] as unknown as [
    string,
    RequestInit,
  ];
  expect(url).toBe("/dashboard/api/events/id/replay");
  expect(options.credentials).toBe("same-origin");
  expect(options.redirect).toBe("error");
  expect(options.headers).not.toHaveProperty("Authorization");
  expect(options.headers).toHaveProperty(
    "X-EventRelay-CSRF",
    "synthetic-nonce",
  );
  expect(options.body).toBe('{"expected_replay_count":3}');
});

it("reports conflicts once and does not retry a replay mutation", async () => {
  const fetch = vi.fn(async () => ({
    status: 409,
    ok: false,
    text: async () => '{"title":"Conflict","detail":"Refresh state"}',
  }));
  vi.stubGlobal("fetch", fetch);
  await expect(
    request("/events/id/replay", {
      method: "POST",
      body: { expected_replay_count: 0 },
    }),
  ).rejects.toBeInstanceOf(APIError);
  expect(fetch).toHaveBeenCalledTimes(1);
});
