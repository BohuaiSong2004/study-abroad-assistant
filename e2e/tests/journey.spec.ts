import { expect, test } from "@playwright/test";
import { createProgram, registerStudent, startApplication } from "./helpers";
import { readFileSync } from "fs";

test("a student plans an application end to end", async ({ page, browser }) => {
  const program = await createProgram({ letters: 1 });
  await registerStudent(page, "Ana Silva");
  await startApplication(page, program);

  await expect(page.getByRole("heading", { name: program.university })).toBeVisible();
  const timeline = page.locator("#timeline");
  // SOP, CV, transcript, English scores (implied by the TOEFL minimum),
  // "ask recommender", submission and the decision estimate.
  await expect(timeline.locator(".milestone")).toHaveCount(7);
  await expect(timeline).toContainText("Finalize statement of purpose");
  await expect(timeline).toContainText("Send English test scores");
  await expect(timeline).toContainText("Submit application (Round 1 deadline)");
  await expect(page.locator("#live")).toHaveText("Live updates on");

  // Ticking a document updates the application and, via the event stream,
  // the timeline (the checkbox survives the live re-render).
  const sop = timeline.locator(".milestone", { hasText: "Finalize statement of purpose" });
  await sop.getByRole("checkbox").check();
  await expect(sop).toHaveClass(/done/);
  await expect(page.locator("#tl-progress-label")).toContainText("1 of 6 done");
  await page.reload();
  await expect(timeline.locator(".milestone.done", { hasText: "Finalize statement of purpose" })).toBeVisible();

  // Custom milestone.
  await page.getByText("Add your own milestone").click();
  await page.getByLabel("Title").fill("Book IELTS test");
  await page.getByLabel("Due date").fill("2026-12-15");
  await page.getByRole("button", { name: "Add milestone" }).click();
  await expect(timeline).toContainText("Book IELTS test");

  // Ask for a recommendation letter; the invitation link is shown once.
  await page.getByText("Ask someone for a letter").click();
  await page.getByLabel("Name", { exact: true }).fill("Dr. Mei Lin");
  await page.getByLabel("Email", { exact: true }).fill("mei.lin@example.edu");
  await page.getByLabel("Relationship (optional)").fill("Thesis advisor");
  await page.getByRole("button", { name: "Create invitation link" }).click();
  const link = page.getByLabel(/Invitation link for Dr. Mei Lin/);
  await expect(link).toBeVisible();
  const url = await link.inputValue();
  expect(url).toContain("/recommend.html#token=");
  await expect(timeline).toContainText("Letter from Dr. Mei Lin");

  // The recommender (no account) submits through the link, in another browser.
  const recCtx = await browser.newContext();
  const rec = await recCtx.newPage();
  const seen: string[] = [];
  rec.on("request", (r) => seen.push(r.url()));
  await rec.goto(url.replace(/^https?:\/\/[^/]+/, ""));
  await expect(rec.locator("#invite-intro")).toContainText("Ana Silva has asked you to write a recommendation letter");
  await expect(rec.locator("#invite-privacy")).toContainText("waived their right to read");
  await rec.getByLabel("Letter text").fill("Ana is one of the most capable and generous students I have supervised. ".repeat(3));
  await rec.getByRole("button", { name: "Submit letter" }).click();
  await expect(rec.getByRole("heading", { name: /Thank you/ })).toBeVisible();
  const token = url.split("#token=")[1];
  expect(seen.some((u) => u.includes(token)), "token must never be sent in a URL").toBe(false);
  await recCtx.close();

  // The student's open page updates live: letter milestone done, status pill.
  const letter = timeline.locator(".milestone", { hasText: "Letter from Dr. Mei Lin" });
  await expect(letter).toHaveClass(/done/);
  await expect(page.locator("#recs")).toContainText("Confidential letter received");

  // Status change marks submission done.
  await page.getByLabel("Application status").selectOption("submitted");
  await expect(timeline.locator(".milestone.done", { hasText: "Submit application" })).toBeVisible();

  // XML exports download and carry the right namespaces.
  for (const [button, ns] of [["Timeline (XML)", "urn:waypoint:timeline:v1"], ["Recommendation packet (XML)", "urn:waypoint:packet:v1"]]) {
    const [download] = await Promise.all([page.waitForEvent("download"), page.getByRole("link", { name: button }).click()]);
    const xml = readFileSync((await download.path())!, "utf8");
    expect(xml).toContain(`xmlns="${ns}"`);
    expect(xml).not.toContain("most capable and generous"); // waived letter withheld
  }

  // Dashboard summary.
  await page.getByRole("link", { name: "← All applications" }).click();
  const card = page.locator(".app-card", { hasText: program.university });
  await expect(card).toContainText("Submitted");
  await expect(card.getByRole("progressbar")).toHaveAttribute("aria-valuenow", /\d+/);

  // Delete.
  await card.getByRole("link", { name: program.university }).click();
  page.once("dialog", (d) => d.accept());
  await page.getByRole("button", { name: "Delete application" }).click();
  await page.waitForURL("**/dashboard.html");
  await expect(page.locator(".app-card", { hasText: program.university })).toHaveCount(0);
});

test("starting a second application to the same program shows the existing one", async ({ page }) => {
  const program = await createProgram();
  await registerStudent(page);
  await startApplication(page, program);
  await page.goto(`/programs.html?q=${encodeURIComponent(program.university)}`);
  await expect(page.locator(".program", { hasText: program.university }).getByRole("link", { name: "View application" })).toBeVisible();
});

test("anonymous visitors can browse and are sent to sign up to apply", async ({ page }) => {
  const program = await createProgram();
  await page.goto("/programs.html");
  await page.getByLabel("Search").fill(program.university);
  await page.getByRole("button", { name: "Search" }).click();
  const card = page.locator(".program", { hasText: program.university });
  await expect(card).toContainText("TOEFL 100+ · IELTS 7.0+ · 2 letters · Fee 90.00 CAD");
  await card.getByRole("button", { name: /Start application/ }).click();
  await page.waitForURL(/mode=register/);
});
