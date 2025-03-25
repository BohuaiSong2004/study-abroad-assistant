import { expect, test } from "@playwright/test";
import { counselorEmail, counselorPassword, createProgram, isoDaysFromNow, registerStudent, startApplication } from "./helpers";

function fmt(iso: string): string {
  return new Intl.DateTimeFormat("en-US", { year: "numeric", month: "short", day: "numeric", timeZone: "UTC" }).format(new Date(iso + "T00:00:00Z"));
}

test("a counselor's deadline change reaches the student's open page live", async ({ page, browser }) => {
  const program = await createProgram();
  await registerStudent(page);
  await startApplication(page, program);
  const submit = page.locator("#timeline .milestone", { hasText: "Submit application" });
  await expect(submit).toContainText(fmt(program.deadlines[0].date));

  // Counselor edits the Round 1 deadline in their own browser.
  const cctx = await browser.newContext();
  const c = await cctx.newPage();
  await c.goto("/?mode=login");
  await c.locator("#login-email").fill(counselorEmail);
  await c.locator("#login-password").fill(counselorPassword);
  await c.locator("#login-form button[type=submit]").click();
  await c.waitForURL("**/counselor.html");
  await expect(c.locator("body")).toHaveAttribute("data-ready", "true");
  await c.getByLabel("Filter programs").fill(program.university);
  await c.getByRole("button", { name: "Filter" }).click();
  await c.getByRole("button", { name: `Edit ${program.university} ${program.name}` }).click();
  const newDate = isoDaysFromNow(45);
  await c.getByRole("dialog").getByLabel("Deadline").first().fill(newDate);
  await c.getByRole("button", { name: "Save" }).click();
  await expect(c.getByText("Saved. Affected timelines are being re-planned.")).toBeVisible();
  await expect(c.locator("#sync-rows")).toContainText("catalog");
  await cctx.close();

  // No reload: the change arrives over Server-Sent Events.
  await expect(submit).toContainText(fmt(newDate));
  await expect(page.locator("#tl-progress-label")).toContainText(`deadline ${fmt(newDate)}`);
});

test("removing a round moves the plan to the next round with a warning", async ({ page }) => {
  const program = await createProgram();
  await registerStudent(page);
  await startApplication(page, program);
  const api = await (await import("./helpers")).counselorAPI();
  const res = await api.put(`/api/programs/${program.id}`, { data: { ...program, deadlines: [program.deadlines[1]] } });
  expect(res.status()).toBe(200);
  await api.dispose();
  await expect(page.locator("#warnings")).toContainText("The Round 1 round is no longer offered; planning for Round 2");
  await expect(page.locator("#timeline .milestone", { hasText: "Submit application (Round 2 deadline)" })).toBeVisible();
});
