import { expect, test } from "@playwright/test";
import { apiAs, createProgram, lastEmailTo, mailpitURL, password, registerStudent, startApplication, tokenLink, unique } from "./helpers";

// These run when the stack sends email to Mailpit (CI, docker compose) and
// E2E_MAILPIT_URL points at it.
test.skip(!mailpitURL, "set E2E_MAILPIT_URL to run the email tests");

test("a recommendation invitation is emailed to the recommender", async ({ page, browser }) => {
  const program = await createProgram();
  await registerStudent(page, "Noor Haddad");
  await startApplication(page, program);
  const recommender = `${unique("prof")}@example.edu`;
  await page.getByText("Ask someone for a letter").click();
  await page.getByLabel("Name", { exact: true }).fill("Prof. Omar Diaz");
  await page.getByLabel("Email", { exact: true }).fill(recommender);
  await page.getByRole("button", { name: "Create invitation link" }).click();
  await expect(page.getByRole("status").filter({ hasText: `We emailed the invitation to ${recommender}` })).toBeVisible();
  const shown = await page.getByLabel(/Invitation link for Prof. Omar Diaz/).inputValue();

  const mail = await lastEmailTo(recommender);
  expect(mail.subject).toBe("Recommendation request from Noor Haddad");
  expect(mail.text).toContain(program.university);
  const link = tokenLink(mail.text, "recommend");
  expect(link).toBe(shown);

  const rec = await (await browser.newContext()).newPage();
  await rec.goto(link.replace(/^https?:\/\/[^/]+/, ""));
  await expect(rec.locator("#invite-intro")).toContainText("Noor Haddad has asked you to write a recommendation letter");
});

test("a forgotten password is reset by email and other sessions end", async ({ page }) => {
  const { email } = await registerStudent(page, "Ines Duarte");
  const otherDevice = await apiAs(email, password);
  expect((await otherDevice.get("/api/applications")).status()).toBe(200);
  await page.getByRole("button", { name: "Sign out" }).click();
  await page.waitForURL((u) => u.pathname === "/");

  await page.goto("/?mode=login");
  await page.getByRole("link", { name: "Forgot your password?" }).click();
  await page.waitForURL("**/reset.html");
  await expect(page.locator("body")).toHaveAttribute("data-ready", "true");
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Email me a link" }).click();
  await expect(page.getByRole("status")).toContainText(`If ${email} has a Waypoint account, we've emailed it a link`);

  const mail = await lastEmailTo(email);
  expect(mail.subject).toBe("Reset your Waypoint password");
  await page.goto(tokenLink(mail.text, "reset").replace(/^https?:\/\/[^/]+/, ""));
  await expect(page.getByRole("heading", { name: "Choose a new password" })).toBeVisible();
  await page.getByLabel("New password").fill("a brand new passphrase");
  await page.getByRole("button", { name: "Set new password" }).click();
  await expect(page.getByRole("status")).toContainText("Your password was changed");
  expect(new URL(page.url()).hash, "spent token removed from the address bar").toBe("");

  // The session on the other device ends (within the gateway's 5 s cache).
  await expect.poll(async () => (await otherDevice.get("/api/applications")).status(), { timeout: 10_000 }).toBe(401);
  await page.goto("/?mode=login");
  await expect(page.locator("body")).toHaveAttribute("data-ready", "true");
  await page.locator("#login-email").fill(email);
  await page.locator("#login-password").fill("a brand new passphrase");
  await page.getByRole("button", { name: "Sign in" }).last().click();
  await page.waitForURL("**/dashboard.html");
});
