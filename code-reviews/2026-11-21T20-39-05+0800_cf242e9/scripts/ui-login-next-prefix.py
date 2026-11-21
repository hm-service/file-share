"""复现 F5：一级文件夹名以 auth 开头（如 /author）时，未登录深链接在登录后丢失目标。

步骤：建「author」文件夹 → 清 localStorage → 打开 /author（跳 /auth?next=%2Fauthor）→ 登录 →
观察最终地址。预期（正确行为）：回到 /author；实际（本次审查）：落到 /，原因见 LoginPage.vue 的
next.startsWith("/auth") 前缀判断。

前置同 ui-smoke.py，运行：
  source .scratch/env.sh && uv run --with playwright==1.65.2 --python 3.14 python <本文件>
"""

import asyncio
import json
import sys

from playwright.async_api import async_playwright

BASE = "http://127.0.0.1:18081"
TOKEN = "review_token_2026"
CHROME = "/opt/workbench/app/chrome/chrome-headless-shell"


async def session_token(page):
    return json.loads(await page.evaluate("localStorage.getItem('file-share.session')"))["token"]


async def main():
    async with async_playwright() as pw:
        browser = await pw.chromium.launch(executable_path=CHROME, args=["--no-sandbox", "--disable-dev-shm-usage"])
        context = await browser.new_context(viewport={"width": 1440, "height": 900}, locale="zh-CN")
        page = await context.new_page()

        await page.goto(BASE + "/auth", wait_until="load")
        await page.fill('input[name="token"]', TOKEN)
        await page.click('button[type="submit"]')
        await page.wait_for_selector(".app", timeout=15000)

        await page.evaluate(
            """async (token) => {
                await fetch("/api/groups", {method: "POST",
                    headers: {Authorization: "Bearer " + token, "Content-Type": "application/json"},
                    body: JSON.stringify({path: "author"})});
            }""",
            await session_token(page),
        )

        await page.evaluate("localStorage.clear()")
        await page.goto(BASE + "/author", wait_until="load")
        await page.wait_for_timeout(600)
        print("login url :", page.url)
        await page.fill('input[name="token"]', TOKEN)
        await page.click('button[type="submit"]')
        await page.wait_for_timeout(2500)
        final = page.url
        ok = final.rstrip("/").endswith("/author")
        print("final url :", final)
        print("PASS 回到 /author" if ok else "FAIL 目标丢失（落到了 " + final + "）")
        await browser.close()
        return 0 if ok else 1


sys.exit(asyncio.run(main()))