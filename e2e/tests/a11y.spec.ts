import AxeBuilder from "@axe-core/playwright";
import { expect, test, Page } from "@playwright/test";
import { counselorEmail, counselorPassword, createProgram, registerStudent, startApplication } from "./helpers";

async function audit(page: Page, name: string) {
  await expect(page.locator("body")).toHaveAttribute("data-ready", "true");
  const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  const serious = result.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(serious.map((v) => `${name}: ${v.id} ${v.help} (${v.nodes.map((n) => n.target.join(" ")).join(", ")})`)).toEqual([]);
}

test("public pages have no serious accessibility violations", async ({ page }) => {
  await page.goto("/");
  await audit(page, "landing");
  await page.goto("/programs.html");
  await expect(page.locator("#results .program").first()).toBeVisible();
  await audit(page, "programs");
  await page.goto("/recommend.html#token=not-a-real-token-but-long-enough");
  await expect(page.locator("#invite-error")).not.toBeEmpty();
  await audit(page, "recommend");
});

test("student pages have no serious accessibility violations", async ({ page }) => {
  const program = await createProgram();
  await registerStudent(page);
  await audit(page, "dashboard (empty)");
  await startApplication(page, program);
  await audit(page, "application");
  await page.goto("/dashboard.html");
  await expect(page.locator(".app-card")).toHaveCount(1);
  await audit(page, "dashboard");
});

test("counselor page has no serious accessibility violations", async ({ page }) => {
  await page.goto("/?mode=login");
  await page.locator("#login-email").fill(counselorEmail);
  await page.locator("#login-password").fill(counselorPassword);
  await page.locator("#login-form button[type=submit]").click();
  await page.waitForURL("**/counselor.html");
  await expect(page.locator("#program-rows tr").first()).toBeVisible();
  await audit(page, "counselor");
});
