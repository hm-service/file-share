"""上一轮 F2 的回归探针：上传成功但「移入目标文件夹」的 PATCH 失败后点「重试」，
不应重新上传、也不应在服务端留下两条同名记录。

跑法：bash code-reviews/2026-11-22T00-32-17+0800_279dc0/scripts/run-ui-upload-retry.sh
"""

import asyncio
import json
import sys
import urllib.parse

from playwright.async_api import async_playwright

BASE = "http://127.0.0.1:18105"
CHROME = "/opt/workbench/app/chrome/chrome-headless-shell"
ADMIN = ("admin", "admin_password_123")
FOLDER = "重试组"
FILENAME = "dup-probe.txt"
CONTENT = b"retry-probe-content\n"

results: list[tuple[str, bool, str]] = []
page_errors: list[str] = []


def report(name, ok, detail=""):
    results.append((name, ok, str(detail)))
    print(("PASS " if ok else "FAIL ") + name + ((" :: " + str(detail)) if detail else ""))


async def main():
    async with async_playwright() as pw:
        browser = await pw.chromium.launch(
            executable_path=CHROME, args=["--no-sandbox", "--disable-dev-shm-usage"]
        )
        context = await browser.new_context(viewport={"width": 1440, "height": 900}, locale="zh-CN")
        page = await context.new_page()
        page.on("pageerror", lambda err: page_errors.append(str(err)))

        await page.goto(BASE + "/", wait_until="domcontentloaded")
        await page.wait_for_url("**/auth**", timeout=15000)
        await page.fill("input[name=username]", ADMIN[0])
        await page.fill("input[name=password]", ADMIN[1])
        await page.click("button[type=submit]")
        await page.wait_for_url(lambda url: "/auth" not in url, timeout=20000)
        await page.wait_for_selector(".topbar", timeout=10000)

        token = json.loads(await page.evaluate("localStorage.getItem('file-share.session')"))["token"]
        status, _ = await page.evaluate(
            """async ([path, token]) => {
                const r = await fetch("/api/groups", {method: "POST",
                    headers: {Authorization: "Bearer " + token, "Content-Type": "application/json"},
                    body: JSON.stringify({path})});
                return [r.status, await r.text()];
            }""",
            [FOLDER, token],
        )
        report(f"建文件夹 {FOLDER}", status in (201, 409), status)

        # 只让第一次「移入目标文件夹」的 PATCH 失败
        patch_calls = {"count": 0}

        async def handle_route(route):
            request = route.request
            if request.method == "PATCH":
                patch_calls["count"] += 1
                if patch_calls["count"] == 1:
                    await route.fulfill(
                        status=500,
                        content_type="application/json",
                        body='{"detail":"探针注入的 PATCH 失败"}',
                    )
                    return
            await route.continue_()

        await page.route("**/api/files/**", handle_route)

        # 进入文件夹视图（API 新建的文件夹要刷新一次列表才出现在侧栏，直接用地址进入）
        await page.goto(BASE + "/" + urllib.parse.quote(FOLDER), wait_until="domcontentloaded")
        await page.wait_for_selector(".crumb.current", timeout=15000)
        await page.wait_for_function(
            "(name) => document.querySelector('.crumb.current')?.textContent?.includes(name)",
            arg=FOLDER,
            timeout=15000,
        )
        await page.wait_for_timeout(400)
        await page.set_input_files(
            "input.hidden-input",
            {"name": FILENAME, "mimeType": "text/plain", "buffer": CONTENT},
        )

        # 等队列进入失败态并出现「重试」
        retry = page.locator(".dock-action.retry").first
        await retry.wait_for(timeout=30000)
        report("PATCH 失败后队列出现可重试状态", True)

        await retry.click()
        # 等队列项变成 done（1.2s 后自动移出）
        await page.wait_for_timeout(2500)

        files = await page.evaluate(
            """async (token) => {
                const r = await fetch("/api/files", {headers: {Authorization: "Bearer " + token}});
                return await r.json();
            }""",
            token,
        )
        same = [f for f in files["files"] if f["filename"] == FILENAME]
        report("同名记录只有一条（没有重新上传）", len(same) == 1, [f["group_path"] for f in same])
        report("记录落在目标文件夹", len(same) == 1 and same[0]["group_path"] == FOLDER,
               [f["group_path"] for f in same])
        report("PATCH 被调用了两次（失败一次 + 重试一次）", patch_calls["count"] == 2, patch_calls["count"])

        # 清掉探针记录，方便重复跑
        for f in same:
            await page.evaluate(
                """async ([id, token]) => {
                    await fetch("/api/files/" + id, {method: "DELETE", headers: {Authorization: "Bearer " + token}});
                }""",
                [f["id"], token],
            )

        await browser.close()

    passed = sum(1 for _, ok, _ in results if ok)
    print(f"{passed}/{len(results)} PASS")
    if page_errors:
        print("PAGE ERRORS:", page_errors)
    sys.exit(0 if passed == len(results) and not page_errors else 1)


asyncio.run(main())