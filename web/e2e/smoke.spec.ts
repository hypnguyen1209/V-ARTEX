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

test("tasks table fits its tablet container without horizontal scrolling", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "tablet-chromium");
  await page.goto("/function/tasks");
  await expect(page.getByText("4/4 records", { exact: true })).toBeVisible();
  const table = page.locator('[data-slot="table-container"]').first();
  await expect(table).toBeVisible();
  await expect.poll(() => table.evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
  for (const name of ["Name", "Goal", "Findings C/H/M/L", "Actions"]) {
    await expect(table.getByRole("columnheader", { name, exact: true })).toBeVisible();
  }
  await expect(table.getByRole("checkbox", { name: "Select all tasks on this page" })).toBeVisible();
  await expect(table.getByRole("button", { name: "ID sorted descending; click to reverse sort order" })).toBeVisible();
  await expect(table.getByRole("button", { name: "Sort Status descending" })).toBeVisible();
  await expectNoHorizontalOverflow(page);
});

test("sidebar collapses on entering tablet while allowing manual expansion", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "tablet-chromium");
  await page.goto("/function/tasks");
  const sidebar = page.locator('[data-slot="sidebar"][data-state]');
  await expect(sidebar).toHaveAttribute("data-state", "collapsed");
  await page.getByRole("button", { name: "Toggle Sidebar" }).click();
  await expect(sidebar).toHaveAttribute("data-state", "expanded");
  await page.setViewportSize({ width: 1100, height: 768 });
  await expect(sidebar).toHaveAttribute("data-state", "expanded");
  await page.setViewportSize({ width: 1280, height: 768 });
  await expect(sidebar).toHaveAttribute("data-state", "expanded");
  await page.setViewportSize({ width: 767, height: 768 });
  await expect(sidebar).toHaveCount(0);
  await page.setViewportSize({ width: 768, height: 768 });
  await expect(sidebar).toHaveAttribute("data-state", "collapsed");
  await page.setViewportSize({ width: 1280, height: 768 });
  await expect(sidebar).toHaveAttribute("data-state", "expanded");
  await page.setViewportSize({ width: 1279, height: 768 });
  await expect(sidebar).toHaveAttribute("data-state", "collapsed");
});
