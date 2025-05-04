import { defineConfig, devices } from "@playwright/test";

// Runs against an already running stack (docker compose or ./scripts/dev.sh).
const baseURL = process.env.E2E_BASE_URL ?? "http://localhost:8080";
// CHROME_PATH runs the Chromium projects on an installed Chrome instead of
// Playwright's download (Firefox and WebKit always use Playwright's builds).
const executablePath = process.env.CHROME_PATH || undefined;
const chromium = executablePath ? { executablePath, args: ["--no-sandbox"] } : {};

export default defineConfig({
  testDir: "./tests",
  timeout: 45_000,
  expect: { timeout: 10_000 },
  fullyParallel: true,
  workers: process.env.CI ? 2 : 3,
  retries: 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 900 }, launchOptions: chromium }, testIgnore: /mobile\.spec/ },
    { name: "firefox", use: { ...devices["Desktop Firefox"], viewport: { width: 1280, height: 900 } }, testIgnore: /mobile\.spec/ },
    { name: "webkit", use: { ...devices["Desktop Safari"], viewport: { width: 1280, height: 900 } }, testIgnore: /mobile\.spec/ },
    { name: "mobile", use: { ...devices["Pixel 7"], launchOptions: chromium }, testMatch: /mobile\.spec/ },
  ],
});
