import json
import os
import time
import uuid
import sqlite3
import hashlib
import asyncio
from pathlib import Path
from fastapi import FastAPI, UploadFile, File, Form, HTTPException
from fastapi.responses import HTMLResponse, FileResponse
from fastapi.middleware.cors import CORSMiddleware
from fastapi.staticfiles import StaticFiles
from urllib.parse import quote

app = FastAPI()
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

# === 数据目录与初始化 ===
# 默认 /data (Docker 卷挂载目录)，本地开发可用环境变量 DATA_DIR 覆盖
DATA_DIR = Path(os.environ.get("DATA_DIR", "/data"))
FILES_DIR = DATA_DIR / "files"
DB_PATH = DATA_DIR / "files.db"
FAVORITES_FILE = DATA_DIR / "favorite.json"

# 新增静态文件夹路径并确保其存在
STATIC_DIR = Path("static")
STATIC_DIR.mkdir(exist_ok=True)

DATA_DIR.mkdir(exist_ok=True)
FILES_DIR.mkdir(exist_ok=True)

# 挂载静态文件夹 (让前端可以通过 /static/xxx 访问到文件)
app.mount("/static", StaticFiles(directory="static"), name="static")


def init_db():
    with sqlite3.connect(DB_PATH) as conn:
        conn.execute('''
            CREATE TABLE IF NOT EXISTS files (
                id TEXT PRIMARY KEY,
                filename TEXT NOT NULL,
                sha256 TEXT NOT NULL,
                update_time INTEGER NOT NULL
            )
        ''')


init_db()

# 收藏数据文件，仅存文件 id 数组
if not FAVORITES_FILE.exists():
    FAVORITES_FILE.write_text("[]", encoding="utf-8")

global_file_lock = asyncio.Lock()


def load_favorites():
    if not FAVORITES_FILE.exists():
        return []
    try:
        with open(FAVORITES_FILE, "r", encoding="utf-8") as f:
            data = json.load(f)
        return data if isinstance(data, list) else []
    except (OSError, json.JSONDecodeError):
        return []


def save_favorites(favorites):
    FAVORITES_FILE.write_text(json.dumps(favorites, ensure_ascii=False, indent=2), encoding="utf-8")


@app.get("/")
async def get_index():
    # 指向 static 文件夹里面的 index.html
    index_path = STATIC_DIR / "index.html"

    if not index_path.exists():
        return HTMLResponse(content=f"找不到文件: {index_path}", status_code=404)

    with open(index_path, "r", encoding="utf-8") as f:
        return HTMLResponse(content=f.read())


# === 新增：文件秒传预检接口 ===
@app.post("/api/check_file")
async def check_file(
    client_sha256: str = Form(...),
    filename: str = Form(...)
):
    final_path = FILES_DIR / client_sha256

    if final_path.exists():
        # 如果物理文件已存在，直接在数据库里新建一条引用记录
        file_id = str(uuid.uuid4())
        async with global_file_lock:
            with sqlite3.connect(DB_PATH) as conn:
                conn.execute(
                    "INSERT INTO files (id, filename, sha256, update_time) VALUES (?, ?, ?, ?)",
                    (file_id, filename, client_sha256, int(time.time()))
                )
        return {"exists": True, "id": file_id, "message": "秒传成功"}

    return {"exists": False}


@app.post("/api/upload")
async def upload_file(
    file: UploadFile = File(...),
    client_sha256: str = Form(...)  # 接收前端计算的 hash
):
    file_id = str(uuid.uuid4())
    final_path = FILES_DIR / client_sha256
    temp_path = FILES_DIR / f"{client_sha256}.temp"

    async with global_file_lock:
        # 1. 物理文件不存在，执行真实上传逻辑
        if not final_path.exists():
            # 流式写入到 .temp 文件 (防止大文件撑爆内存，每次读取 1MB)
            with open(temp_path, "wb") as f:
                while chunk := await file.read(1024 * 1024):
                    f.write(chunk)

            # 2. 分块计算 .temp 文件的 SHA256，进行校验
            hasher = hashlib.sha256()
            with open(temp_path, "rb") as f:
                while chunk := f.read(1024 * 1024):  # 同样分块读取
                    hasher.update(chunk)

            server_sha256 = hasher.hexdigest()

            # 3. 校验比对
            if server_sha256 != client_sha256:
                # 校验失败，删除残次品，拦截入库
                temp_path.unlink()
                raise HTTPException(status_code=400, detail="文件哈希校验失败，上传数据可能已损坏或被篡改")

            # 4. 校验成功，重命名为正式文件
            temp_path.rename(final_path)

        # 5. 写入 SQLite 数据库记录 (不论是真实上传还是秒传，都创建逻辑记录)
        with sqlite3.connect(DB_PATH) as conn:
            conn.execute(
                "INSERT INTO files (id, filename, sha256, update_time) VALUES (?, ?, ?, ?)",
                (file_id, file.filename, client_sha256, int(time.time()))
            )

    return {"message": "上传成功", "id": file_id, "sha256": client_sha256}


@app.get("/api/files")
async def list_files():
    favorite_set = set(load_favorites())
    with sqlite3.connect(DB_PATH) as conn:
        conn.row_factory = sqlite3.Row
        all_files = [dict(row) for row in conn.execute("SELECT * FROM files ORDER BY update_time DESC").fetchall()]

    for f in all_files:
        real_file = FILES_DIR / f["sha256"]
        f["size"] = real_file.stat().st_size if real_file.exists() else 0
        f["is_favorite"] = f["id"] in favorite_set

    return {"files": all_files}


@app.post("/api/favorite/{file_id}")
async def toggle_favorite(file_id: str):
    with sqlite3.connect(DB_PATH) as conn:
        row = conn.execute("SELECT 1 FROM files WHERE id = ?", (file_id,)).fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="文件不存在")

    async with global_file_lock:
        favorites = load_favorites()
        if file_id in favorites:
            favorites.remove(file_id)
            status = "unfavorited"
        else:
            favorites.append(file_id)
            status = "favorited"
        save_favorites(favorites)
    return {"status": status}


@app.get("/api/download/{file_id}")
async def download_file(file_id: str):
    with sqlite3.connect(DB_PATH) as conn:
        row = conn.execute("SELECT filename, sha256 FROM files WHERE id = ?", (file_id,)).fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="文件记录不存在")
    file_path = FILES_DIR / row[1]
    if not file_path.exists():
        raise HTTPException(status_code=404, detail="物理文件已丢失")
    return FileResponse(path=file_path, headers={"Content-Disposition": f"attachment; filename*=utf-8''{quote(row[0])}"})


@app.delete("/api/files/{file_id}")
async def delete_file(file_id: str):
    async with global_file_lock:
        with sqlite3.connect(DB_PATH) as conn:
            cursor = conn.cursor()
            row = cursor.execute("SELECT sha256 FROM files WHERE id = ?", (file_id,)).fetchone()
            if not row:
                raise HTTPException(status_code=404, detail="文件不存在")

            cursor.execute("DELETE FROM files WHERE id = ?", (file_id,))
            ref_count = cursor.execute("SELECT COUNT(*) FROM files WHERE sha256 = ?", (row[0],)).fetchone()[0]

            if ref_count == 0:
                file_path = FILES_DIR / row[0]
                if file_path.exists():
                    file_path.unlink()

            # 删除文件时同步清理收藏
            favorites = load_favorites()
            if file_id in favorites:
                favorites.remove(file_id)
                save_favorites(favorites)
    return {"message": "删除成功"}
