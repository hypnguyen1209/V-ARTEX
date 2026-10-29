import { expect, type Page, test } from "@playwright/test";

const browserErrors = new WeakMap<Page, string[]>();

test.beforeEach(async ({ page }) => {
  const failures: string[] = [];
  page.on("pageerror", (error) => failures.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") failures.push(message.text());
  });
  browserErrors.set(page, failures);
});

test.afterEach(async ({ page }) => {
  const failures = browserErrors.get(page) ?? [];
  expect(failures, "browser errors").toEqual([]);
});

async function expectNoHorizontalOverflow(page: Page) {
  const fits = await page.evaluate(
    () => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1,
  );
  expect(fits).toBe(true);
}

test("tasks and global run-budget settings work in mock mode", async ({ page }) => {
  await page.goto("/function/tasks");
  await expect(page.getByRole("tab", { name: "Current Tasks" })).toBeVisible();
  await expect(page.getByText("4/4 records", { exact: true })).toBeVisible();
  await expectNoHorizontalOverflow(page);

  await page.goto("/system/settings");
  await expect(page.getByRole("heading", { name: "System Settings" })).toBeVisible();
  const card = page.locator('[aria-labelledby="agent-concurrency-heading"]');
  await expect(card).toBeVisible();
  await card.getByRole("switch").click();
  await card.getByRole("spinbutton").fill("3");
  await card.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Global agent concurrency settings saved")).toBeVisible();
  await expectNoHorizontalOverflow(page);
});
