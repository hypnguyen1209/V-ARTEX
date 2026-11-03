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

for (const scenario of [
  { width: 768, expanded: false },
  { width: 768, expanded: true },
  { width: 1024, expanded: false },
  { width: 1024, expanded: true },
  { width: 1440, expanded: true },
  { width: 1920, expanded: true },
]) {
  test(`populated task controls fit at ${scenario.width}px with sidebar ${scenario.expanded ? "expanded" : "collapsed"}`, async ({
    page,
  }, testInfo) => {
    test.skip(testInfo.project.name !== "tablet-chromium");
    await page.setViewportSize({ width: scenario.width, height: 768 });
    await page.goto("/function/tasks");
    const sidebar = page.locator('[data-slot="sidebar"][data-state]');
    await expect(sidebar).toHaveAttribute("data-state", scenario.width < 1280 ? "collapsed" : "expanded");
    if (scenario.expanded && scenario.width < 1280) {
      await page.getByRole("button", { name: "Toggle Sidebar" }).click();
      await expect(sidebar).toHaveAttribute("data-state", "expanded");
    }
    const table = page.locator('[data-slot="table-container"]').first();
    await table.getByRole("button", { name: "Rename task #t-acme-web", exact: true }).click();
    const name = table.getByRole("textbox", { name: "Task #t-acme-web name", exact: true });
    await name.fill("External attack surface reconnaissance");
    await name.press("Enter");
    await expect(
      table.getByRole("link", { name: "External attack surface reconnaissance", exact: true }),
    ).toBeVisible();
    const measure = () =>
      table.evaluate((container) => {
        const bounds = container.getBoundingClientRect();
        const actions = [...container.querySelectorAll("tbody tr")].map((row) => {
          const cell = row.lastElementChild;
          if (!cell) return false;
          const rect = cell.getBoundingClientRect();
          const controls = [...cell.querySelectorAll("a, button")];
          return (
            rect.left >= bounds.left - 1 &&
            rect.right <= bounds.right + 1 &&
            controls.every((control) => {
              const controlBounds = control.getBoundingClientRect();
              return controlBounds.left >= bounds.left - 1 && controlBounds.right <= bounds.right + 1;
            })
          );
        });
        const coreControlsVisible = [
          ...container.querySelectorAll(
            "tbody td:is(:nth-child(1), :nth-child(2), :nth-child(3), :nth-child(5), :nth-child(6), :nth-child(8), :last-child)",
          ),
        ].every((cell) => {
          const cellBounds = cell.getBoundingClientRect();
          if (cellBounds.width === 0) return false;
          return [...cell.querySelectorAll(":scope > *, a, button, [role=checkbox]")].every((control) => {
            const rect = control.getBoundingClientRect();
            return rect.left >= cellBounds.left - 1 && rect.right <= cellBounds.right + 1;
          });
        });
        return {
          width: container.clientWidth,
          scroll: container.scrollWidth,
          actionsVisible: actions.every(Boolean),
          coreControlsVisible,
        };
      });
    console.log(scenario, await measure());
    await expect
      .poll(async () => {
        const result = await measure();
        return result.scroll <= result.width + 1 && result.actionsVisible && result.coreControlsVisible;
      })
      .toBe(true);
    await expectNoHorizontalOverflow(page);
  });
}

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
