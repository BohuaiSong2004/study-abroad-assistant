import { expect, test } from "@playwright/test";
import { createProgram, registerStudent, startApplication } from "./helpers";

test("application pages fit a phone screen", async ({ page }) => {
  const program = await createProgram();
  await registerStudent(page);
  await startApplication(page, program);
  for (const path of [page.url(), "/dashboard.html", "/programs.html", "/"]) {
    await page.goto(path);
    await expect(page.locator("body")).toHaveAttribute("data-ready", "true");
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow, `horizontal scroll on ${path}`).toBeLessThanOrEqual(0);
  }
});
