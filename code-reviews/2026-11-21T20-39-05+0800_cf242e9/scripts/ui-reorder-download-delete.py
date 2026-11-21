"""UI 复检：文件行拖拽排序持久化、文件夹「全部下载」逐个签 URL、三级文件夹不再显示「新建子文件夹」、
删除文件夹并勾选「同时删除这些文件」。依赖 ui-smoke.py 跑过之后的状态（工作 / 项目A / 阶段一改）。

运行方式同 ui-smoke.py。预期：11/11 PASS。
"""


import asyncio
import json
import sys

from playwright.async_api import async_playwright

BASE = "http://127.0.0.1:18081"
CHROME = "/opt/workbench/app/chrome/chrome-headless-shell"
results = []
http_errors = []
page_errors = []


def report(name, ok, detail=""):
    results.append((name, ok))
    print(("PASS " if ok else "FAIL ") + name + ((" :: " + str(detail)) if detail else ""))


async def token(page):
    return json.loads(await page.evaluate("localStorage.getItem('file-share.session')"))["token"]


async def api(page, method, path, body=None, headers=None):
    return await page.evaluate(
        """async ([method, path, body, token]) => {
            const h = {Authorization: "Bearer " + token};
            if (body) h["Content-Type"] = "application/json";
            const r = await fetch(path, {method, headers: h, body: body ? JSON.stringify(body) : undefined});
            return [r.status, await r.text()];
        }""",
        [method, path, body, await token(page)],
    )


async def drag_vertical(page, source, target, dy=-30):
    box_s = await source.bounding_box()
    box_t = await target.bounding_box()
    await source.hover()
    await page.wait_for_timeout(150)
    await page.mouse.move(box_s["x"] + box_s["width"] / 2, box_s["y"] + box_s["height"] / 2)
    await page.mouse.down()
    steps = 12
    end_y = box_t["y"] + dy
    for i in range(1, steps + 1):
        await page.mouse.move(
            box_s["x"] + box_s["width"] / 2,
            box_s["y"] + (end_y - box_s["y"]) * i / steps,
        )
        await page.wait_for_timeout(35)
    await page.mouse.up()
    await page.wait_for_timeout(1200)


async def main():
    async with async_playwright() as pw:
        browser = await pw.chromium.launch(executable_path=CHROME, args=["--no-sandbox", "--disable-dev-shm-usage"])
        context = await browser.new_context(
            viewport={"width": 1440, "height": 900}, locale="zh-CN", accept_downloads=True
        )
        page = await context.new_page()
        page.on("pageerror", lambda e: page_errors.append(str(e)))
        page.on(
            "response",
            lambda r: http_errors.append(f"{r.status} {r.request.method} {r.url}") if r.status >= 400 else None,
        )
        calls = []
        page.on(
            "request",
            lambda r: calls.append(f"{r.method} {r.url}") if r.url.startswith(BASE + "/api/") and r.method not in ("GET",) else None,
        )

        await page.goto(BASE + "/auth", wait_until="load")
        await page.fill('input[name="token"]', "review_token_2026")
        await page.click('button[type="submit"]')
        await page.wait_for_selector(".app", timeout=15000)

        # setup: 4 files in 工作 via API (upload small contents)
        created = []
        for i, name in enumerate(["aaa.txt", "bbb.txt", "ccc.txt", "ddd.txt"]):
            content = f"content-{i}".encode()
            status, text = await page.evaluate(
                """async ([name, content, token]) => {
                    const bytes = new TextEncoder().encode(content);
                    const digest = await crypto.subtle.digest("SHA-256", bytes);
                    const sha = [...new Uint8Array(digest)].map(b => b.toString(16).padStart(2, "0")).join("");
                    const fd = new FormData();
                    fd.append("file", new File([bytes], name));
                    fd.append("client_sha256", sha);
                    const r = await fetch("/api/upload", {method: "POST", headers: {Authorization: "Bearer " + token}, body: fd});
                    const payload = await r.json();
                    await fetch("/api/files/" + payload.id, {method: "PATCH", headers: {Authorization: "Bearer " + token, "Content-Type": "application/json"}, body: JSON.stringify({group_path: "工作"})});
                    return [r.status, payload.id];
                }""",
                [name, content, await token(page)],
            )
            created.append(text)
            if status != 200:
                print("setup upload failed", name, status)

        await page.goto(BASE + "/工作", wait_until="load")
        await page.wait_for_timeout(900)
        names_before = await page.locator(".file-row .file-name").all_inner_texts()
        report("files listed in creation order", names_before == ["aaa.txt", "bbb.txt", "ccc.txt", "ddd.txt"], names_before)

        # drag first row to the bottom
        await drag_vertical(page, page.locator(".file-row").nth(0).locator(".drag-handle"), page.locator(".file-row").nth(3), dy=20)
        names_after = await page.locator(".file-row .file-name").all_inner_texts()
        order_calls = [c for c in calls if "/api/files/order" in c]
        report("file reorder drag applied locally", names_after != names_before, names_after)
        report("file reorder PUT sent", len(order_calls) == 1, order_calls)
        await page.reload(wait_until="load")
        await page.wait_for_timeout(900)
        names_reload = await page.locator(".file-row .file-name").all_inner_texts()
        report("file order persisted", names_reload == names_after, names_reload)

        # download-all button: triggers one signed-URL request per visible file
        download_urls = []
        page.on("request", lambda r: download_urls.append(r.url) if "/download-url" in r.url else None)
        async with page.expect_download(timeout=15000) as dl:
            await page.click('button:has-text("全部下载")')
        got = await dl.value
        await page.wait_for_timeout(1500)
        report("download-all signs one url per file", len(download_urls) == 4, download_urls)
        report("download-all started a download", got.suggested_filename in ("aaa.txt", "bbb.txt", "ccc.txt", "ddd.txt"), got.suggested_filename)

        # level-3 guard: no "新建子文件夹" in level-3 folder view / menu
        await page.goto(BASE + "/工作/项目A/阶段一改", wait_until="load")
        await page.wait_for_timeout(800)
        toolbar_new = await page.locator('.toolbar button:has-text("新建子文件夹")').count()
        report("level-3 toolbar hides new-subfolder", toolbar_new == 0, toolbar_new)
        await page.click('.crumbs .crumb:last-child', button="right") if False else None
        await page.locator(".toolbar .icon-btn").last.click()
        await page.wait_for_timeout(400)
        menu_new = await page.locator('.folder-menu-pop .row-menu-item:has-text("新建子文件夹")').count()
        report("level-3 folder menu hides new-subfolder", menu_new == 0, menu_new)
        await page.keyboard.press("Escape")
        await page.wait_for_timeout(200)
        await page.mouse.click(700, 850)

        # delete folder with files, checkbox checked -> file records gone
        status, _ = await api(page, "POST", "/api/groups", {"path": "待删"})
        content = b"to be deleted"
        status, text = await page.evaluate(
            """async ([content, token]) => {
                const bytes = new TextEncoder().encode(content);
                const digest = await crypto.subtle.digest("SHA-256", bytes);
                const sha = [...new Uint8Array(digest)].map(b => b.toString(16).padStart(2, "0")).join("");
                const fd = new FormData();
                fd.append("file", new File([bytes], "del-me.txt"));
                fd.append("client_sha256", sha);
                const r = await fetch("/api/upload", {method: "POST", headers: {Authorization: "Bearer " + token}, body: fd});
                const payload = await r.json();
                await fetch("/api/files/" + payload.id, {method: "PATCH", headers: {Authorization: "Bearer " + token, "Content-Type": "application/json"}, body: JSON.stringify({group_path: "待删"})});
                return [r.status, payload.id];
            }""",
            [content, await token(page)],
        )
        deleted_id = text
        await page.goto(BASE + "/", wait_until="load")
        await page.wait_for_timeout(800)
        await page.click('.nav-tree .node.is-group:has(.node-label:text-is("待删"))', button="right")
        await page.wait_for_selector(".folder-menu-pop", timeout=5000)
        await page.click('.folder-menu-pop .row-menu-item:has-text("删除文件夹")')
        await page.wait_for_selector(".dialog", timeout=5000)
        checkbox = page.locator('.dialog input[type="checkbox"]')
        report("delete dialog has checkbox", await checkbox.count() == 1)
        await checkbox.check()
        await page.click(".dialog-btn.is-danger")
        await page.wait_for_timeout(1200)
        status, text = await api(page, "GET", "/api/files")
        report("deleted folder files gone", deleted_id not in text)
        report("deleted folder gone from list", "待删" not in text)

        await browser.close()

    print("\n--- unexpected http errors ---")
    for item in http_errors:
        print(item)
    print("--- page errors ---")
    for item in page_errors:
        print(item)
    failing = [n for n, ok in results if not ok]
    print(f"{len(results) - len(failing)}/{len(results)} passed")
    if failing:
        print("FAILED:", failing)
    return 0 if not failing else 1


sys.exit(asyncio.run(main()))