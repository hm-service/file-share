"""只读账号 UI、侧栏树默认折叠、侧栏拖拽调宽、存储状态红点的界面探针。

前置：一份监听 BASE 的实例（由 run-ui-probe.sh 起），DATA_DIR 里用户表有
admin（read+write）与 reader（read）两个账号；视口 1440x900。

跑法：bash code-reviews/2026-11-22T00-32-17+0800_279dc0/scripts/run-ui-probe.sh
"""

import asyncio
import json
import os
import sys

from playwright.async_api import async_playwright

BASE = os.environ.get("BASE", "http://127.0.0.1:18097")
DATA_DIR = os.environ["DATA_DIR"]
CHROME = "/opt/workbench/app/chrome/chrome-headless-shell"

ADMIN = ("admin", "admin_password_123")
READER = ("reader", "reader_password_123")

results: list[tuple[str, bool, str]] = []
page_errors: list[str] = []
console_noise: list[str] = []


def report(name, ok, detail=""):
    results.append((name, ok, str(detail)))
    print(("PASS " if ok else "FAIL ") + name + ((" :: " + str(detail)) if detail else ""))


async def login(page, user):
    await page.goto(BASE + "/", wait_until="domcontentloaded")
    await page.wait_for_url("**/auth**", timeout=10000)
    await page.fill("input[name=username]", user[0])
    await page.fill("input[name=password]", user[1])
    await page.click("button[type=submit]")
    await page.wait_for_url(lambda url: "/auth" not in url, timeout=15000)
    await page.wait_for_selector(".topbar", timeout=10000)
    # 等列表与 /api/me 都落定
    await page.wait_for_timeout(600)


async def token(page):
    raw = await page.evaluate("localStorage.getItem('file-share.session')")
    return json.loads(raw)["token"]


async def api(page, method, path, body=None):
    payload = None if body is None else body
    status, text = await page.evaluate(
        """async ([method, path, body, token]) => {
            const headers = {Authorization: "Bearer " + token};
            if (body) headers["Content-Type"] = "application/json";
            const r = await fetch(path, {method, headers, body: body ? JSON.stringify(body) : undefined});
            return [r.status, await r.text()];
        }""",
        [method, path, payload, await token(page)],
    )
    return status, text


async def main():
    async with async_playwright() as pw:
        browser = await pw.chromium.launch(
            executable_path=CHROME, args=["--no-sandbox", "--disable-dev-shm-usage"]
        )
        context = await browser.new_context(viewport={"width": 1440, "height": 900}, locale="zh-CN")
        page = await context.new_page()
        page.on("pageerror", lambda err: page_errors.append(str(err)))
        page.on("console", lambda msg: console_noise.append(msg.text) if msg.type == "error" else None)

        # ---------- 写账号：造数据 ----------
        await login(page, ADMIN)
        for path in ["组A", "组A/子1", "组A/子1/孙1"]:
            status, body = await api(page, "POST", "/api/groups", {"path": path})
            report(f"建文件夹 {path}", status == 201, f"{status} {body[:80]}")

        # 上传一个文件到 组A（供拖拽把手 / 行操作检查）
        content = "probe-content"
        sha = "d0a2b8a1ab5a8bb5ef0ac1c1e8d1cd18f0f68b9a60a2b3f9f1c2d3e4f5a6b7c8"
        # 用页面内的 hashFile 算真实 sha，避免上传校验失败
        sha = await page.evaluate(
            "async (text) => {const enc = new TextEncoder().encode(text);"
            "return window.__app.hashFile(new Blob([enc]));}",
            content,
        )
        fd_js = """
        async ([sha, text, token]) => {
          const form = new FormData();
          form.append("file", new Blob([new TextEncoder().encode(text)]), "探针.txt");
          form.append("client_sha256", sha);
          const r = await fetch("/api/upload", {method: "POST", headers: {Authorization: "Bearer " + token}, body: form});
          return [r.status, await r.text()];
        }
        """
        status, body = await page.evaluate(fd_js, [sha, content, await token(page)])
        report("上传探针文件", status == 200, f"{status} {body[:80]}")

        # ---------- 写账号：顶栏与用户胶囊 ----------
        upload_btn = page.locator(".top-actions .btn.primary")
        report("写账号顶栏有上传按钮", await upload_btn.count() == 1)
        top_btns = await page.locator(".top-actions .btn").all_inner_texts()
        report("写账号顶栏有新建文件夹", any("新建文件夹" in t for t in top_btns), top_btns)
        chip = await page.locator(".user-chip").inner_text()
        report("写账号用户胶囊显示名字且无只读徽标", "admin" in chip and "只读" not in chip, chip)

        # ---------- 侧栏树默认只展开到第二级 ----------
        await page.evaluate("localStorage.removeItem('file-share.folded-folders')")
        await page.reload(wait_until="domcontentloaded")
        await page.wait_for_selector(".nav-tree .node.is-group", timeout=10000)
        labels = await page.locator(".nav-tree .node-label").all_inner_texts()
        # 只应有 组A 与 子1；孙1 被折叠在 子1 下面
        report(
            "树默认展开到第二级（孙1 折叠）",
            "组A" in labels and "子1" in labels and "孙1" not in labels,
            labels,
        )

        # ---------- 侧栏拖拽调宽：上限半屏、刷新回默认 ----------
        before = await page.evaluate(
            "document.querySelector('.sidebar-wrap').getBoundingClientRect().width"
        )
        grip = page.locator(".sidebar-grip")
        box = await grip.bounding_box()
        await page.mouse.move(box["x"] + box["width"] / 2, box["y"] + 100)
        await page.mouse.down()
        await page.mouse.move(box["x"] + box["width"] / 2 + 700, box["y"] + 100, steps=8)
        await page.mouse.up()
        after = await page.evaluate(
            "document.querySelector('.sidebar-wrap').getBoundingClientRect().width"
        )
        report("拖拽后侧栏变宽", after > before, f"{before:.0f} -> {after:.0f}")
        report("侧栏宽度不超过半个视口", after <= 720 + 1, f"{after:.0f} <= 720")
        await page.reload(wait_until="domcontentloaded")
        await page.wait_for_selector(".nav-tree", timeout=10000)
        reset = await page.evaluate(
            "document.querySelector('.sidebar-wrap').getBoundingClientRect().width"
        )
        report("刷新后侧栏宽度回默认（不持久化）", abs(reset - 248) < 2, f"{reset:.0f}")

        # ---------- 只读账号：写入口全部隐藏 ----------
        await page.evaluate("localStorage.clear()")
        await login(page, READER)
        report("只读账号顶栏无上传按钮", await page.locator(".top-actions .btn.primary").count() == 0)
        report("只读账号顶栏无新建文件夹", await page.locator(".top-actions .btn").count() == 0)
        chip = await page.locator(".user-chip").inner_text()
        report("只读账号用户胶囊挂只读徽标", "reader" in chip and "只读" in chip, chip)
        report("只读账号侧栏无新建入口", await page.locator(".section-add, .new-folder").count() == 0)

        # 进入 组A：有子文件夹与文件
        await page.locator(".nav-tree .node-main", has_text="组A").first.click()
        await page.wait_for_timeout(500)
        report("只读账号文件夹行没有拖拽把手", await page.locator(".drag-handle").count() == 0)
        # 探针文件在「未分组」里：到那里检查文件行的写操作入口
        await page.goto(BASE + "/ungrouped", wait_until="domcontentloaded")
        await page.wait_for_selector(".file-row", timeout=10000)
        report("只读账号文件行没有收藏按钮", await page.locator(".icon-btn.star").count() == 0)
        report("只读账号文件行没有移动按钮", await page.locator(".icon-btn.group").count() == 0)
        await page.locator(".icon-btn.more").first.click()
        await page.wait_for_timeout(200)
        menu = await page.locator(".row-menu").inner_text()
        report(
            "只读账号更多菜单没有重命名/删除",
            "重命名" not in menu and "删除" not in menu and "移动到文件夹" not in menu,
            menu.replace("\n", "|"),
        )
        report("只读账号更多菜单仍可签分享链接", "分享链接" in menu, menu.replace("\n", "|"))
        await page.keyboard.press("Escape")

        # ---------- 存储状态红点：把 blob 目录移走后重新加载 ----------
        blobs = os.path.join(DATA_DIR, "blobs")
        moved = blobs + ".away"
        os.rename(blobs, moved)
        try:
            await page.reload(wait_until="domcontentloaded")
            await page.wait_for_selector(".storage", timeout=10000)
            await page.wait_for_timeout(400)
            dot_class = await page.locator(".storage-dot").get_attribute("class")
            text = await page.locator(".storage-text").inner_text()
            report("存储不可达时侧栏红点", "is-bad" in (dot_class or ""), dot_class)
            report("存储不可达时侧栏文案", "无法连接" in text, text)
        finally:
            os.rename(moved, blobs)

        await browser.close()

    print()
    passed = sum(1 for _, ok, _ in results if ok)
    print(f"{passed}/{len(results)} PASS")
    if page_errors:
        print("PAGE ERRORS:", page_errors)
    if console_noise:
        print("CONSOLE ERRORS:", console_noise)
    ok = passed == len(results) and not page_errors and not console_noise
    print("CONSOLE_CLEAN" if not page_errors and not console_noise else "CONSOLE_DIRTY")
    sys.exit(0 if ok else 1)


asyncio.run(main())