import { expect, test } from "@playwright/test";
import { password, registerStudent } from "./helpers";

test("register, sign out and sign back in", async ({ page }) => {
  const { email } = await registerStudent(page, "Lena Park");
  await expect(page.getByRole("heading", { name: "Your applications" })).toBeVisible();
  await expect(page.getByText("Welcome back, Lena Park.")).toBeVisible();
  await expect(page.getByText("No applications yet")).toBeVisible();

  await page.getByRole("button", { name: "Sign out" }).click();
  await page.waitForURL((u) => u.pathname === "/");

  // Protected pages redirect to sign in and come back afterwards.
  await page.goto("/dashboard.html");
  await page.waitForURL(/\/\?mode=login&next=/);
  await page.locator("#login-email").fill(email);
  await page.locator("#login-password").fill("wrong password");
  await page.getByRole("button", { name: "Sign in" }).last().click();
  await expect(page.locator("#login-error")).toHaveText("email or password is incorrect");

  await page.locator("#login-password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).last().click();
  await page.waitForURL("**/dashboard.html");
});

test("duplicate registration and weak passwords are explained", async ({ page }) => {
  const { email } = await registerStudent(page);
  await page.getByRole("button", { name: "Sign out" }).click();
  await page.waitForURL((u) => u.pathname === "/");
  await page.goto("/?mode=register");
  await expect(page.locator("body")).toHaveAttribute("data-ready", "true");
  await page.getByLabel("Your name").fill("Someone");
  await page.locator("#register-email").fill(email);
  await page.locator("#register-password").fill("short");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.locator("#register-error")).toHaveText("password must be at least 10 characters");
  await page.locator("#register-password").fill(password);
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.locator("#register-error")).toHaveText("an account with this email already exists");
});

test.describe("before JavaScript runs", () => {
  test.use({ javaScriptEnabled: false });

  test("the sign-in form posts instead of leaking the password into the URL", async ({ page, browser }) => {
    // Create the account with JavaScript enabled.
    const ctx = await browser.newContext({ javaScriptEnabled: true });
    const { email } = await registerStudent(await ctx.newPage());
    await ctx.close();

    await page.goto("/");
    await page.locator("#login-email").fill(email);
    await page.locator("#login-password").fill(password);
    const [req] = await Promise.all([
      page.waitForRequest((r) => r.url().includes("/api/auth/login")),
      page.locator("#login-form button[type=submit]").click(),
    ]);
    expect(req.method()).toBe("POST");
    expect(req.url()).not.toContain(encodeURIComponent(password));
    await page.waitForURL("**/dashboard.html");
    expect(page.url()).not.toContain("password");
  });
});
