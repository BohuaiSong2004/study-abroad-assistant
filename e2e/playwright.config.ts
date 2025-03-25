import { defineConfig, devices } from "@playwright/test";

// Runs against an already running stack (docker compose or ./scripts/dev.sh).
const baseURL = process.env.E2E_BASE_URL ?? "http://localhost:8080";
const executablePath = process.env.CHROME_PATH || undefined;

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
    launchOptions: executablePath ? { executablePath, args: ["--no-sandbox"] } : {},
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 900 } }, testIgnore: /mobile\.spec/ },
    { name: "mobile", use: { ...devices["Pixel 7"] }, testMatch: /mobile\.spec/ },
  ],
});
