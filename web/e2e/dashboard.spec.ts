import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";
import { expect, test } from "@playwright/test";

const token = "m4-browser-synthetic-token";
async function login(page: Page) {
  await page.goto("/dashboard/");
  await page.getByLabel("Management token").fill(token);
  await page.getByRole("button", { name: "Open console" }).click();
  await expect(
    page.getByRole("heading", { name: "Relay overview." }),
  ).toBeVisible();
}
async function fixture(page: Page) {
  return (await (await page.request.get("/__test/fixture")).json()) as Record<
    string,
    string
  >;
}

test("authenticated overview, pagination, keyboard access and real screenshot", async ({
  page,
}) => {
  await page.goto("/dashboard/");
  await expect(page.getByLabel("Management token")).toBeVisible();
  await page.getByLabel("Management token").fill("wrong");
  await page.getByRole("button", { name: "Open console" }).click();
  await expect(page.getByRole("alert")).toContainText("not accepted");
  await expect(page.getByLabel("Management token")).toHaveValue("");
  await page.getByLabel("Management token").fill(token);
  await page.getByRole("button", { name: "Open console" }).click();
  await expect(
    page.getByRole("heading", { name: "Endpoints", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("link", { name: /Billing hooks/ })).toBeVisible();
  await expect(page.getByRole("table").getByRole("row")).toHaveCount(26);
  expect(
    await page.evaluate(() => ({
      local: localStorage.length,
      session: sessionStorage.length,
      cookies: document.cookie,
    })),
  ).toEqual({ local: 0, session: 0, cookies: "" });
  const cookies = await page.context().cookies();
  expect(cookies).toHaveLength(1);
  expect(cookies[0].httpOnly).toBe(true);
  expect(cookies[0].sameSite).toBe("Strict");
  expect(cookies[0].value).not.toContain(token);
  await page.keyboard.press("Tab");
  expect(
    await page.evaluate(() => document.activeElement !== document.body),
  ).toBe(true);
  const axe = await new AxeBuilder({ page }).analyze();
  expect(axe.violations).toEqual([]);
  await page.getByRole("heading", { name: "Relay overview." }).click();
  await page.setViewportSize({ width: 1440, height: 1700 });
  await page.screenshot({
    path: "../docs/images/dashboard-m4.png",
  });
  await page.setViewportSize({ width: 1440, height: 1100 });
  const events = page.getByRole("region", { name: "Recent events" });
  const first = await events.getByRole("link").allTextContents();
  await events.getByRole("button", { name: "Next", exact: true }).click();
  await expect(
    events.getByRole("button", { name: "Previous", exact: true }),
  ).toBeEnabled();
  await expect(events.getByRole("row")).toHaveCount(10);
  const second = await events.getByRole("link").allTextContents();
  expect(second.filter((item) => first.includes(item))).toHaveLength(0);
  await page.getByRole("button", { name: "Log out" }).click();
  await expect(page.getByLabel("Management token")).toBeVisible();
  expect(await page.context().cookies()).toHaveLength(0);
});

test("endpoint inspection, zero statistics and guarded re-enable", async ({
  page,
}) => {
  await login(page);
  const ids = await fixture(page);
  await page.goto(`/dashboard/#/endpoints/${ids.empty_endpoint}`);
  await expect(
    page.getByText("No finished HTTP-response attempts in this window.", {
      exact: false,
    }),
  ).toBeVisible();
  await page.goto(`/dashboard/#/endpoints/${ids.disabled_endpoint}`);
  await expect(
    page.getByRole("heading", { name: "Paused sandbox" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Edit destination" }).click();
  await page.getByRole("checkbox", { name: "Enabled", exact: true }).check();
  await page.getByRole("button", { name: "Save endpoint" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Endpoint updated" }),
  ).toBeVisible();
  await expect(page.getByText("enabled", { exact: true })).toBeVisible();
  const axe = await new AxeBuilder({ page }).analyze();
  expect(axe.violations).toEqual([]);
});

test("attempt and cycle pages, retained replay history and duplicate-effect warning", async ({
  page,
}) => {
  await login(page);
  const ids = await fixture(page);
  await page.goto(`/dashboard/#/events/${ids.history}`);
  const attempts = page.getByRole("region", { name: "Attempt timeline" });
  await expect(
    attempts.getByRole("heading", { name: /^Attempt \d/, level: 3 }),
  ).toHaveCount(25);
  await attempts.getByRole("button", { name: "Next", exact: true }).click();
  await expect(
    attempts.getByRole("heading", { name: /^Attempt \d/, level: 3 }),
  ).toHaveCount(2);
  await attempts.getByRole("button", { name: "Previous", exact: true }).click();
  await expect(
    attempts.getByRole("heading", { name: /^Attempt \d/, level: 3 }),
  ).toHaveCount(25);
  const cycles = page.getByRole("region", { name: "Previous delivery cycles" });
  await cycles.getByRole("button", { name: "Next", exact: true }).click();
  await expect(cycles.getByRole("row")).toHaveCount(2);
  await page.getByRole("button", { name: "Replay event", exact: true }).click();
  await expect(
    page.getByRole("group", { name: "Confirm replay" }),
  ).toContainText("repeat an effect");
  await page
    .getByRole("button", { name: "Confirm replay", exact: true })
    .click();
  await expect(page.getByRole("status")).toContainText("Replay accepted");
  await expect(
    page
      .getByText("Lifetime attempts", { exact: true })
      .locator("..")
      .locator("dd"),
  ).toHaveText("28", { timeout: 10000 });
});

test("dead-letter replay creates a new cycle and renders payload/diagnostics as text", async ({
  page,
}) => {
  await login(page);
  const ids = await fixture(page);
  await page.goto(`/dashboard/#/events/${ids.dead}`);
  await expect(
    page.getByText("non_retryable_response", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Replay event", exact: true }).click();
  await page
    .getByRole("button", { name: "Confirm replay", exact: true })
    .click();
  await expect(page.getByRole("status")).toContainText("Replay accepted");
  await page.goto(`/dashboard/#/events/${ids.hostile}`);
  await expect(page.getByRole("heading", { level: 1 })).toContainText("<img");
  await page.getByRole("button", { name: "Show payload" }).click();
  await expect(
    page.getByRole("region", { name: "Stored event payload" }),
  ).toContainText("<script>");
  expect(
    await page.evaluate(
      () => (window as Window & { __injected?: number }).__injected,
    ),
  ).toBeUndefined();
  expect(await page.locator("main img, main svg, main script").count()).toBe(0);
  const axe = await new AxeBuilder({ page }).analyze();
  expect(axe.violations).toEqual([]);
});

test("navigation aborts reads, suppresses stale content and mobile layout fits", async ({
  page,
}) => {
  await login(page);
  const ids = await fixture(page);
  let blocked = false;
  await page.route(
    `**/dashboard/api/events/${ids.delivered}`,
    async (route) => {
      blocked = true;
      await new Promise((resolve) => setTimeout(resolve, 1000));
      await route.continue().catch(() => {});
    },
  );
  await page.goto(`/dashboard/#/events/${ids.delivered}`);
  await expect.poll(() => blocked).toBe(true);
  await page.goto(`/dashboard/#/endpoints/${ids.endpoint}`);
  await expect(
    page.getByRole("heading", { name: "Billing hooks" }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Current delivery cycle" }),
  ).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/dashboard/#/");
  await expect(
    page.getByRole("heading", { name: "Relay overview." }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: "test-results/dashboard-mobile.png",
    fullPage: true,
  });
  const axe = await new AxeBuilder({ page }).analyze();
  expect(axe.violations).toEqual([]);
});
