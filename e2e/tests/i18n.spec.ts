import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { createProgram, password, unique } from "./helpers";

// A browser that prefers Chinese gets the Chinese UI, including the text the
// planner generates; the header switch changes language and remembers it.
test.use({ locale: "zh-CN" });

test("the UI follows a Chinese browser, including generated timeline text", async ({ page, context }) => {
  const program = await createProgram();
  await page.goto("/");
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("所有申请、所有截止日期，一份计划搞定。");
  await expect(page).toHaveTitle("Waypoint · 留学申请规划");
  // Mixed content keeps its link: "无需注册即可{浏览项目}。"
  await expect(page.locator("main").getByRole("link", { name: "浏览项目" })).toBeVisible();

  // Sign up in Chinese.
  await page.goto("/?mode=register");
  await expect(page.locator("body")).toHaveAttribute("data-ready", "true");
  await page.getByLabel("你的姓名").fill("李华");
  await page.locator("#register-email").fill(`${unique("zh")}@example.com`);
  await page.locator("#register-password").fill("short");
  await page.getByRole("button", { name: "注册账户" }).click();
  await expect(page.locator("#register-error")).toHaveText("密码至少需要 10 个字符"); // API message translated
  await page.locator("#register-password").fill(password);
  await page.getByRole("button", { name: "注册账户" }).click();
  await page.waitForURL("**/dashboard.html");
  await expect(page.getByText("欢迎回来，李华。")).toBeVisible();

  // Start an application; the planner's milestones appear in Chinese.
  await page.goto(`/programs.html?q=${encodeURIComponent(program.university)}`);
  const card = page.locator(".program", { hasText: program.university });
  await card.getByRole("button", { name: /开始申请/ }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel(/第一轮/).check();
  await dialog.getByRole("button", { name: "开始申请" }).click();
  await page.waitForURL("**/application.html?id=*");
  const timeline = page.locator("#timeline");
  await expect(timeline).toContainText("提交申请（第一轮截止）");
  await expect(timeline).toContainText("完成个人陈述");
  await expect(timeline).toContainText("预计出结果");
  await expect(timeline.locator("time").first()).toHaveText(/\d{4}年\d{1,2}月\d{1,2}日/);

  // Fees are hundredths of the unit for every currency; yen and won show no decimals.
  expect(await page.evaluate(() => [(window as any).WP.money(3000000, "JPY"), (window as any).WP.money(9000, "USD")])).toEqual(["JP¥30,000", "US$90.00"]);

  const axe = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).analyze();
  expect(axe.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);

  // Switch to English: remembered in a cookie, over the browser preference.
  await page.getByRole("button", { name: "English" }).click();
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(timeline).toContainText("Submit application (Round 1 deadline)");
  expect((await context.cookies()).find((c) => c.name === "wp_lang")?.value).toBe("en");
  await page.goto("/dashboard.html");
  await expect(page.getByRole("heading", { name: "Your applications" })).toBeVisible();
  await page.getByRole("button", { name: "中文" }).click();
  await expect(page.getByRole("heading", { name: "我的申请", level: 1 })).toBeVisible();
});
