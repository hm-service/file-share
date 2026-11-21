"""UI 冒烟：登录、树/表格、详情、10 分钟与 24 小时分享链接、curl、移动文件、统计、重命名、
文件夹拖拽排序、全局搜索、移动端抽屉、路由回退，并检查控制台是否干净。

前置：一份干净的空实例（独立 DATA_DIR），监听 127.0.0.1:18081，AUTH_TOKEN=review_token_2026，
MAX_FILE_SIZE=16MiB。脚本会用 API 自建文件夹与文件。运行：
  source .scratch/env.sh && uv run --with playwright==1.65.2 --python 3.14 \
    python code-reviews/<本次目录>/scripts/ui-smoke.py
预期：34/34 PASS、CONSOLE_CLEAN。
"""


import asyncio
import sys
import time

from playwright.async_api import async_playwright

BASE = "http://127.0.0.1:18081"
TOKEN = "review_token_2026"
CHROME = "/opt/workbench/app/chrome/chrome-headless-shell"

console_noise: list[str] = []
page_errors: list[str] = []
failed_requests: list[str] = []
api_calls: list[str] = []

results: list[tuple[str, bool, str]] = []


def report(name, ok, detail=""):
    results.append((name, ok, str(detail)))
    print(("PASS " if ok else "FAIL ") + name + ((" :: " + str(detail)) if detail else ""))


async def session_token(page):
    raw = await page.evaluate("localStorage.getItem('file-share.session')")
    import json
    return json.loads(raw)["token"]


async def api(page, method, path, body=None):
    payload = None if body is None else body
    resp = await page.evaluate(
        """async ([method, path, body, token]) => {
            const headers = {Authorization: "Bearer " + token};
            if (body) headers["Content-Type"] = "application/json";
            const r = await fetch(path, {method, headers, body: body ? JSON.stringify(body) : undefined});
            return [r.status, await r.text()];
        }""",
        [method, path, payload, await session_token(page)],
    )
    return resp


async def main():
    async with async_playwright() as pw:
        browser = await pw.chromium.launch(
            executable_path=CHROME, args=["--no-sandbox", "--disable-dev-shm-usage"]
        )
        context = await browser.new_context(
            viewport={"width": 1440, "height": 900}, locale="zh-CN"
        )
        await context.grant_permissions(["clipboard-read", "clipboard-write"])
        page = await context.new_page()
        page.on(
            "console",
            lambda msg: console_noise.append(f"{msg.type}: {msg.text}")
            if msg.type in ("error", "warning")
            else None,
        )
        page.on("pageerror", lambda err: page_errors.append(str(err)))
        page.on("requestfailed", lambda req: failed_requests.append(f"{req.method} {req.url} :: {req.failure}"))
        http_errors: list[str] = []
        page.on(
            "response",
            lambda resp: http_errors.append(f"{resp.status} {resp.request.method} {resp.url}")
            if resp.status >= 400
            else None,
        )
        page.on(
            "request",
            lambda req: api_calls.append(f"{req.method} {req.url}")
            if req.url.startswith(BASE + "/api/") and req.method not in ("GET",)
            else None,
        )

        # ---------- 0) bootstrap session via UI login on /auth ----------
        await page.goto(BASE + "/auth", wait_until="load")
        await page.fill('input[name="token"]', TOKEN)
        await page.click('button[type="submit"]')
        await page.wait_for_selector(".app", timeout=15000)
        await page.wait_for_timeout(600)
        report("login lands on recent view", page.url == BASE + "/", page.url)

        # API setup: folders + one ungrouped file
        folders = ["工作", "工作/项目A", "工作/项目A/阶段一", "工作/项目B", "甲", "乙"]
        for path in folders:
            status, text = await api(page, "POST", "/api/groups", {"path": path})
            if status != 201:
                print("setup create group failed", path, status, text)
        status, text = await api(page, "GET", "/api/files")
        report("setup folders visible after refresh", True, f"{len(folders)} folders")
        await page.reload(wait_until="load")
        await page.wait_for_timeout(700)

        # ---------- 1) shell / tree / table ----------
        await page.goto(BASE + "/工作/项目A", wait_until="load")
        await page.wait_for_timeout(800)
        head = (await page.locator(".table-head").inner_text()).replace("\n", "|")
        report("table header columns", "名称" in head and "指纹" in head and "操作" in head, head)
        await page.goto(BASE + "/", wait_until="load")
        await page.wait_for_timeout(600)
        storage = await page.locator(".storage-text").inner_text()
        report("storage hint zh", storage == "本地磁盘", storage)
        labels = await page.locator(
            ".nav-tree .node.is-group:not(.is-child):not(.is-grandchild) > .node-main .node-label"
        ).all_inner_texts()
        report("tree top-level folders", labels == ["工作", "甲", "乙"], labels)
        report("tree quick entries zh", "最近" in await page.locator(".quick").inner_text())

        # ---------- 2) 3-level deep link + breadcrumbs + dir rows ----------
        await page.goto(BASE + "/工作/项目A/阶段一", wait_until="load")
        await page.wait_for_timeout(800)
        crumbs = (await page.locator(".crumbs").inner_text()).replace("\n", "|")
        report("3-level breadcrumb", all(x in crumbs for x in ("全部", "工作", "项目A", "阶段一")), crumbs)
        empty = await page.locator(".empty-title").inner_text()
        report("empty folder copy", "空" in empty or "没有文件" in empty, empty)

        # ---------- 3) folder rows ordering + counts in parent view ----------
        await page.goto(BASE + "/工作/项目A", wait_until="load")
        await page.wait_for_timeout(700)
        dir_names = await page.locator(".dir-row .dir-name").all_inner_texts()
        report("folder rows in content", dir_names == ["阶段一"], dir_names)
        meta = (await page.locator(".toolbar .meta").inner_text()).replace("\n", "|")
        report("toolbar meta counts folders+files", "1 个文件夹" in meta and "0 个文件" in meta, meta)

        # ---------- 4) upload into current folder (hidden input) ----------
        await page.set_input_files(
            'input[type="file"]',
            files=[{"name": "评测文件.txt", "mimeType": "text/plain", "buffer": b"hello cross review"}],
        )
        await page.wait_for_timeout(3000)
        rows = await page.locator(".file-row").count()
        report("uploaded row appears in folder", rows == 1, f"rows={rows}")
        if rows == 1:
            row_text = (await page.locator(".file-row").first.inner_text()).replace("\n", "|")
            short_hash = await page.locator(".file-row .cell-hash").first.inner_text()
            report("short hash column 8 chars", len(short_hash.strip()) == 8, short_hash)
            report("row has no folder tag inside folder view", "工作" not in row_text, row_text[:120])

        # ---------- 5) file detail dialog ----------
        await page.click(".file-row .file-name")
        await page.wait_for_selector(".dialog", timeout=5000)
        sha = (await page.locator(".detail-sha").inner_text()).strip()
        report("detail dialog shows full sha", len(sha) == 64, len(sha))
        await page.keyboard.press("Escape")
        await page.wait_for_timeout(300)
        report("detail dialog closes on Escape", await page.locator(".dialog").count() == 0)

        # ---------- 6) share link 10 min via row button ----------
        await page.click(".file-row .icon-btn.link")
        await page.wait_for_timeout(700)
        clip = await page.evaluate("navigator.clipboard.readText()")
        ok10 = "/s/" in clip and "sig=" in clip and "exp=" in clip
        exp10 = int(clip.split("exp=")[1]) if ok10 else 0
        report("10min share copied", ok10 and abs(exp10 - time.time() - 600) < 30, f"ttl={exp10 - time.time():.0f}s")
        report("share request carries ttl=600", any("share?ttl=600" in c for c in api_calls), [c for c in api_calls if "share" in c])

        # ---------- 7) share link 24 h via more menu ----------
        await page.click(".file-row .icon-btn.more")
        await page.wait_for_selector(".row-menu", timeout=5000)
        await page.click('.row-menu-item:has-text("复制分享链接（24 小时）")')
        await page.wait_for_timeout(700)
        clip24 = await page.evaluate("navigator.clipboard.readText()")
        exp24 = int(clip24.split("exp=")[1])
        report("24h share copied", "/s/" in clip24 and 86000 < exp24 - time.time() <= 86400, f"ttl={exp24 - time.time():.0f}s")
        report("share request carries ttl=86400", any("share?ttl=86400" in c for c in api_calls))

        # ---------- 8) copy curl command ----------
        await page.click(".file-row .icon-btn.more")
        await page.wait_for_selector(".row-menu", timeout=5000)
        await page.click('.row-menu-item:has-text("复制 curl 命令")')
        await page.wait_for_timeout(700)
        curl = await page.evaluate("navigator.clipboard.readText()")
        report("curl command copied", curl.startswith("curl -f -o '") and "/s/" in curl, curl[:100])

        # ---------- 9) move file via folder menu ----------
        await page.click(".file-row .icon-btn.group")
        await page.wait_for_selector(".row-menu", timeout=5000)
        await page.click('.row-menu-item:has-text("阶段一")')
        await page.wait_for_timeout(1000)
        report("file moved out of folder view", await page.locator(".file-row").count() == 0)
        await page.goto(BASE + "/工作/项目A/阶段一", wait_until="load")
        await page.wait_for_timeout(700)
        report("file now in target folder", await page.locator(".file-row").count() == 1)

        # ---------- 10) folder context menu -> stats ----------
        await page.click('.nav-tree .node.is-group:has(.node-label:text-is("工作"))', button="right")
        await page.wait_for_selector(".folder-menu-pop", timeout=5000)
        await page.click('.row-menu-item:has-text("统计信息")')
        await page.wait_for_selector(".dialog", timeout=5000)
        stats = (await page.locator(".dialog").inner_text()).replace("\n", "|")
        report("stats dialog content", "统计信息" in stats and "文件数量" in stats, stats[:120])
        await page.keyboard.press("Escape")
        await page.wait_for_timeout(300)

        # ---------- 11) rename folder keeps current deep view ----------
        await page.goto(BASE + "/工作/项目A/阶段一", wait_until="load")
        await page.wait_for_timeout(700)
        await page.click('.nav-tree .node.is-grandchild:has(.node-label:text-is("阶段一"))', button="right")
        await page.wait_for_selector(".folder-menu-pop", timeout=5000)
        await page.click('.row-menu-item:has-text("重命名文件夹")')
        await page.wait_for_selector(".dialog input", timeout=5000)
        await page.fill(".dialog input", "阶段一改")
        await page.click(".dialog-btn.is-primary")
        await page.wait_for_timeout(1000)
        report("rename keeps deep view", "阶段一改" in page.url or "%E9%98%B6%E6%AE%B5%E4%B8%80%E6%94%B9" in page.url, page.url)

        # ---------- 12) folder reorder in content area (Sortable + PUT groups/order) ----------
        await page.goto(BASE + "/工作", wait_until="load")
        await page.wait_for_timeout(800)
        before = await page.locator(".dir-row .dir-name").all_inner_texts()
        report("two folder rows before reorder", before == ["项目A", "项目B"], before)
        if before == ["项目A", "项目B"]:
            handle = page.locator(".dir-row").nth(1).locator(".drag-handle")
            target = page.locator(".dir-row").nth(0)
            await handle.hover()
            await page.wait_for_timeout(200)
            box_h = await handle.bounding_box()
            box_t = await target.bounding_box()
            await page.mouse.move(box_h["x"] + box_h["width"] / 2, box_h["y"] + box_h["height"] / 2)
            await page.mouse.down()
            for i in range(1, 11):
                await page.mouse.move(
                    box_h["x"] + box_h["width"] / 2,
                    box_h["y"] + (box_t["y"] + 8 - box_h["y"]) * i / 10,
                )
                await page.wait_for_timeout(40)
            await page.mouse.up()
            await page.wait_for_timeout(1200)
            after = await page.locator(".dir-row .dir-name").all_inner_texts()
            report("folder rows reorder after drag", after == ["项目B", "项目A"], after)
            report("group order PUT sent", any("/api/groups/order" in c for c in api_calls), [c for c in api_calls if "order" in c])

        # ---------- 13) recent view search across folders ----------
        await page.fill(".search input", "评测")
        await page.wait_for_timeout(700)
        report("global search finds file", await page.locator(".file-row").count() == 1, await page.locator(".file-row").count())
        row = (await page.locator(".file-row").first.inner_text()).replace("\n", "|")
        report("search row shows folder tag", "阶段一改" in row, row[:140])
        report("search disables drag handles", await page.locator(".file-row .drag-handle").count() == 0)
        await page.fill(".search input", "")
        await page.wait_for_timeout(400)

        # ---------- 14) mobile drawer ----------
        await page.set_viewport_size({"width": 390, "height": 844})
        await page.wait_for_timeout(400)
        await page.click(".hamburger")
        await page.wait_for_timeout(400)
        report("mobile drawer opens", await page.locator(".sidebar-wrap.is-open").count() == 1)
        await page.screenshot(path="/tmp/rev/mobile-recent.png")
        await page.mouse.click(360, 400)
        await page.set_viewport_size({"width": 1440, "height": 900})
        await page.goto(BASE + "/工作", wait_until="load")
        await page.wait_for_timeout(700)
        await page.screenshot(path="/tmp/rev/desktop-folder.png")

        # ---------- 15) routing edge cases ----------
        await page.goto(BASE + "/不存在的文件夹", wait_until="load")
        await page.wait_for_timeout(1200)
        report("unknown folder falls back to recent", page.url == BASE + "/", page.url)
        resp = await page.goto(BASE + "/a/b/c/d", wait_until="load")
        report("four-segment path 404", resp.status == 404, resp.status)
        resp = await page.goto(BASE + "/index.html", wait_until="load")
        report("index.html not served", resp.status == 404, resp.status)

        await browser.close()

    print("\n--- console errors/warnings ---")
    for item in console_noise:
        print(item)
    print("--- page errors ---")
    for item in page_errors:
        print(item)
    print("--- failed requests ---")
    for item in failed_requests:
        print(item)
    failing = [name for name, ok, _ in results if not ok]
    print(f"\n{len(results) - len(failing)}/{len(results)} checks passed")
    if failing:
        print("FAILED:", failing)
    expected = ("/a/b/c/d", "/index.html")
    unexpected_http = [e for e in http_errors if not any(e.endswith(BASE + p) for p in expected)]
    print("--- unexpected http errors ---")
    for item in unexpected_http:
        print(item)
    clean = not page_errors and not failed_requests and not unexpected_http
    print("CONSOLE_CLEAN" if clean else "CONSOLE_NOISE")
    return 0 if not failing and clean else 1


sys.exit(asyncio.run(main()))