import { mkdirSync, writeFileSync } from "node:fs";
import type { Page } from "@playwright/test";
import { expect, test } from "@playwright/test";

async function login(page: Page) {
  await page.goto("/dashboard/");
  await page.getByLabel("Management token").fill("m4-browser-synthetic-token");
  await page.getByRole("button", { name: "Open console" }).click();
  await expect(
    page.getByRole("heading", { name: "Relay overview." }),
  ).toBeVisible();
  return (await (await page.request.get("/__test/fixture")).json()) as Record<
    string,
    string
  >;
}

test("stale replay is shown once and cross-origin cookie mutations are rejected", async ({
  page,
}) => {
  const ids = await login(page);
  await page.goto(`/dashboard/#/events/${ids.delivered}`);
  await page.getByRole("button", { name: "Replay event", exact: true }).click();
  const session = (await (
    await page.request.get("/dashboard/api/session")
  ).json()) as { csrf_nonce: string };
  const current = (await (
    await page.request.get(`/dashboard/api/events/${ids.delivered}`)
  ).json()) as { replay_count: number };
  const rejected = await page.request.post(
    `/dashboard/api/events/${ids.delivered}/replay`,
    {
      headers: {
        Origin: "http://evil.example",
        "X-EventRelay-CSRF": session.csrf_nonce,
      },
      data: { expected_replay_count: current.replay_count },
    },
  );
  expect(rejected.status()).toBe(403);
  const accepted = await page.request.post(
    `/dashboard/api/events/${ids.delivered}/replay`,
    {
      headers: {
        Origin: "http://127.0.0.1:58084",
        "X-EventRelay-CSRF": session.csrf_nonce,
      },
      data: { expected_replay_count: current.replay_count },
    },
  );
  expect(accepted.status()).toBe(202);
  let uiSubmissions = 0;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      request.url().endsWith(`/events/${ids.delivered}/replay`)
    )
      uiSubmissions++;
  });
  await page
    .getByRole("button", { name: "Confirm replay", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "Refresh before submitting",
  );
  expect(uiSubmissions).toBe(1);
  expect((await page.request.get("/v1/events")).status()).toBe(401);
});

test("repeated navigation releases page data and records retained Chromium heap", async ({
  page,
}) => {
  const ids = await login(page);
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Performance.enable");
  const round = async () => {
    await page.goto(`/dashboard/#/events/${ids.hostile}`);
    await expect(
      page.getByRole("heading", { name: "Attempt timeline" }),
    ).toBeVisible();
    await page.goto(`/dashboard/#/endpoints/${ids.endpoint}`);
    await expect(
      page.getByRole("heading", { name: "Billing hooks" }),
    ).toBeVisible();
    await page.goto("/dashboard/#/");
    await expect(
      page.getByRole("link", { name: /Billing hooks/ }),
    ).toBeVisible();
  };
  for (let i = 0; i < 5; i++) await round();
  const heap = async () => {
    await cdp.send("HeapProfiler.collectGarbage");
    const result = await cdp.send("Performance.getMetrics");
    return (
      result.metrics.find((item) => item.name === "JSHeapUsedSize")?.value ?? 0
    );
  };
  const before = await heap();
  for (let i = 0; i < 20; i++) await round();
  const after = await heap();
  mkdirSync("test-results", { recursive: true });
  writeFileSync(
    "test-results/m4-browser-heap.json",
    JSON.stringify(
      {
        warmupRounds: 5,
        measuredRounds: 20,
        navigations: 60,
        beforeBytes: before,
        afterBytes: after,
        deltaBytes: after - before,
      },
      null,
      2,
    ),
  );
  expect(before).toBeGreaterThan(0);
  expect(after).toBeLessThan(before + 4 * 1024 * 1024);
  await cdp.detach();
});
