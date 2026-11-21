"""复现 F2：上传成功后「移入目标文件夹」的 PATCH 失败 → 队列记失败 → 点重试 → 服务端多出一条记录。

做法：把第一条 PATCH /api/files/{id} 人为 abort，再点队列的「重试」，最后数同名同 sha 的记录。
运行方式同 ui-smoke.py。预期输出包含 dup-probe.txt record count: 2，且之一在未分组、之一在「工作」。
"""


import asyncio
import json
import sys

from playwright.async_api import async_playwright

BASE = "http://127.0.0.1:18081"
CHROME = "/opt/workbench/app/chrome/chrome-headless-shell"


async def main():
    async with async_playwright() as pw:
        browser = await pw.chromium.launch(executable_path=CHROME, args=["--no-sandbox", "--disable-dev-shm-usage"])
        context = await browser.new_context(viewport={"width": 1440, "height": 900}, locale="zh-CN")
        page = await context.new_page()

        await page.goto(BASE + "/auth", wait_until="load")
        await page.fill('input[name="token"]', "review_token_2026")
        await page.click('button[type="submit"]')
        await page.wait_for_selector(".app", timeout=15000)

        # a folder to upload into (upload target = current folder, so the post-upload PATCH runs)
        await page.goto(BASE + "/工作", wait_until="load")
        await page.wait_for_timeout(800)

        # fail exactly the first PATCH /api/files/{id} (the "move uploaded file into folder" step)
        state = {"failed": 0}

        async def handler(route):
            if route.request.method == "PATCH" and state["failed"] == 0:
                state["failed"] += 1
                await route.abort("failed")
            else:
                await route.continue_()

        await page.route("**/api/files/*", handler)

        await page.set_input_files(
            'input[type="file"]',
            files=[{"name": "dup-probe.txt", "mimeType": "text/plain", "buffer": b"duplicate probe content"}],
        )
        await page.wait_for_timeout(3000)
        queue_text = await page.locator(".upload-dock").inner_text()
        print("queue after forced PATCH failure:", queue_text.replace("\n", " | "))
        print("failed PATCH count:", state["failed"])

        await page.unroute("**/api/files/*")
        # click retry
        retry = page.locator('.dock-action:has-text("重试")')
        print("retry button count:", await retry.count())
        if await retry.count():
            await retry.first.click()
            await page.wait_for_timeout(3500)
        print("upload dock present after retry:", await page.locator(".upload-dock").count())

        records = await page.evaluate(
            """async () => {
                const token = JSON.parse(localStorage.getItem("file-share.session")).token;
                const r = await fetch("/api/files", {headers: {Authorization: "Bearer " + token}});
                const d = await r.json();
                return d.files.filter(f => f.filename === "dup-probe.txt");
            }"""
        )
        print("records named dup-probe.txt:", json.dumps(records, ensure_ascii=False, indent=1))
        await browser.close()


sys.exit(asyncio.run(main()))