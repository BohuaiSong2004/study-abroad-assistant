import { APIRequestContext, Page, expect, request as pwRequest } from "@playwright/test";

export const baseURL = process.env.E2E_BASE_URL ?? "http://localhost:8080";
export const counselorEmail = process.env.E2E_COUNSELOR_EMAIL ?? "counselor@example.com";
export const counselorPassword = process.env.E2E_COUNSELOR_PASSWORD ?? "counselor-demo-password";
export const password = "correct horse battery";

let seq = 0;
/** A unique suffix so tests can re-run against the same database. */
export function unique(prefix: string): string {
  seq += 1;
  return `${prefix}-${Date.now().toString(36)}-${process.pid}-${seq}`;
}

/** An API client that behaves like the browser (same Origin, cookie jar). */
export async function apiAs(email: string, pw: string): Promise<APIRequestContext> {
  const ctx = await pwRequest.newContext({ baseURL, extraHTTPHeaders: { Origin: baseURL } });
  const res = await ctx.post("/api/auth/login", { data: { email, password: pw } });
  expect(res.status(), await res.text()).toBe(200);
  return ctx;
}

export async function counselorAPI(): Promise<APIRequestContext> {
  return apiAs(counselorEmail, counselorPassword);
}

/** Dates relative to today, so tests never depend on the calendar. */
export function isoDaysFromNow(days: number): string {
  const d = new Date();
  d.setUTCHours(0, 0, 0, 0);
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
}

export interface ProgramOptions {
  id?: string;
  university?: string;
  round1?: string;
  round2?: string;
  letters?: number;
}

/** Creates a program as the bootstrap counselor and returns it. */
export async function createProgram(opts: ProgramOptions = {}) {
  const api = await counselorAPI();
  const id = opts.id ?? unique("e2e");
  const res = await api.post("/api/programs", {
    data: {
      id,
      university: opts.university ?? `E2E University ${id}`,
      name: "MS in Computer Science",
      degree: "master",
      field: "Computer Science",
      country: "Canada",
      city: "Toronto",
      timezone: "America/Toronto",
      intake: "Fall 2027",
      deadlines: [
        { round: "round_1", date: opts.round1 ?? isoDaysFromNow(60) },
        { round: "round_2", date: opts.round2 ?? isoDaysFromNow(120) },
      ],
      requirements: {
        minToefl: 100, minIelts: 7, greRequired: false,
        recommendationsRequired: opts.letters ?? 2,
        documents: ["statement_of_purpose", "cv", "transcript"],
      },
      applicationFee: { amountCents: 9000, currency: "CAD" },
      tags: ["e2e"],
    },
  });
  expect(res.status(), await res.text()).toBe(201);
  const program = await res.json();
  await api.dispose();
  return program;
}

/** Registers a new student through the UI and lands on the dashboard. */
export async function registerStudent(page: Page, name = "Ana Silva") {
  const email = `${unique("student")}@example.com`;
  await page.goto("/?mode=register");
  await expect(page.locator("body")).toHaveAttribute("data-ready", "true");
  await page.getByLabel("Your name").fill(name);
  await page.locator("#register-email").fill(email);
  await page.locator("#register-password").fill(password);
  await page.getByRole("button", { name: "Create account" }).click();
  await page.waitForURL("**/dashboard.html");
  await expect(page.locator("body")).toHaveAttribute("data-ready", "true");
  return { email, name };
}

/** Starts an application from the programs page and opens it. */
export async function startApplication(page: Page, program: { id: string; university: string }, roundLabel = "Round 1") {
  await page.goto(`/programs.html?q=${encodeURIComponent(program.university)}`);
  const card = page.locator(".program", { hasText: program.university });
  await card.getByRole("button", { name: /Start application/ }).click();
  const dialog = page.getByRole("dialog", { name: "Start application" });
  await dialog.getByLabel(new RegExp(roundLabel)).check();
  await dialog.getByRole("button", { name: "Start application" }).click();
  await page.waitForURL("**/application.html?id=*");
  await expect(page.locator("#timeline .milestone").first()).toBeVisible();
  await expect(page.locator("body")).toHaveAttribute("data-ready", "true");
  return new URL(page.url()).searchParams.get("id")!;
}
