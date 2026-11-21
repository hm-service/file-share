"""登录后跳转（?next=）的站内限制探针：验证 LoginPage.afterLoginURL 能否被绕过。

跑法：bash code-reviews/2026-11-22T00-32-17+0800_279dc0/scripts/repro-login-open-redirect.sh
"""

import asyncio
import sys

from playwright.async_api import async_playwright

BASE = "http://127.0.0.1:18100"
CHROME = "/opt/workbench/app/chrome/chrome-headless-shell"
ADMIN = ("admin", "admin_password_123")

results: list[tuple[str, bool, str]] = []


def report(name, ok, detail=""):
    results.append((name, ok, str(detail)))
    print(("PASS " if ok else "FAIL ") + name + ((" :: " + str(detail)) if detail else ""))


async def login_to(page, next_value):
    await page.goto(f"{BASE}/auth?next={next_value}", wait_until="domcontentloaded")
    await page.fill("input[name=username]", ADMIN[0])
    await page.fill("input[name=password]", ADMIN[1])
    await page.click("button[type=submit]")
    await page.wait_for_timeout(1500)
    return page.url


async def main():
    async with async_playwright() as pw:
        browser = await pw.chromium.launch(
            executable_path=CHROME, args=["--no-sandbox", "--disable-dev-shm-usage"]
        )
        page = await browser.new_page()

        # 正常深链接：登录后应回到目标站内地址
        url = await login_to(page, "%2Fungrouped")
        report("合法 next 回到站内", url.rstrip("/") == BASE + "/ungrouped", url)

        await page.evaluate("localStorage.clear()")
        # 反斜杠绕过：URL 语义里 /\evil.example 等价于 //evil.example
        href = await page.evaluate(
            "(c) => new URL(c, location.origin).href", "/\\evil.example"
        )
        report("浏览器把 /\\\\evil.example 解析成外站", href.startswith("http://evil.example"), href)
        url = await login_to(page, "%2F%5Cevil.example")
        report("登录后跳到了外站（应当被拦成 /）", not url.startswith(BASE), url)

        await browser.close()

    print()
    passed = sum(1 for _, ok, _ in results if ok)
    print(f"{passed}/{len(results)} PASS")
    # 这个探针是复现用：只要有外站跳转就返回 1（复现成功）
    escaped = any(name.startswith("登录后跳到了外站") and ok for name, ok, _ in results)
    print("RESULT:", "复现成功：?next= 可把登录后的用户带到外站" if escaped else "未复现")
    sys.exit(1 if escaped else 0)


asyncio.run(main())