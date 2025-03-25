import { expect, test, request } from "@playwright/test";
import { baseURL, createProgram, registerStudent, startApplication } from "./helpers";

test("security headers are set on every page", async ({ page }) => {
  const res = await page.goto("/");
  const h = res!.headers();
  expect(h["content-security-policy"]).toContain("script-src 'self'");
  expect(h["x-content-type-options"]).toBe("nosniff");
  expect(h["x-frame-options"]).toBe("DENY");
  expect(h["referrer-policy"]).toBe("same-origin");
});

test("cross-site requests with the session cookie are rejected", async ({ page }) => {
  const program = await createProgram();
  await registerStudent(page);
  const cookies = await page.context().cookies();
  const session = cookies.find((c) => c.name === "wp_session")!;
  expect(session.httpOnly).toBe(true);
  expect(session.sameSite).toBe("Lax");

  const evil = await request.newContext({ baseURL, extraHTTPHeaders: { Origin: "https://evil.example", Cookie: `wp_session=${session.value}` } });
  const res = await evil.post("/api/applications", { data: { programId: program.id } });
  expect(res.status()).toBe(403);
  await evil.dispose();
});

test("a broken or replaced invitation link explains itself", async ({ page }) => {
  await page.goto("/recommend.html");
  await expect(page.locator("#invite-error")).toContainText("This link is incomplete");
  await page.goto("/recommend.html#token=ABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFGHIJKLMNOPQRST");
  await expect(page.locator("#invite-error")).toContainText("invalid or was replaced");
});

test("students cannot open someone else's application", async ({ page, browser }) => {
  const program = await createProgram();
  await registerStudent(page);
  const id = await startApplication(page, program);
  const other = await browser.newContext();
  const p2 = await other.newPage();
  await registerStudent(p2, "Mallory");
  await p2.goto(`/application.html?id=${id}`);
  await expect(p2.getByRole("heading", { name: "Application not found" })).toBeVisible();
  await other.close();
});
