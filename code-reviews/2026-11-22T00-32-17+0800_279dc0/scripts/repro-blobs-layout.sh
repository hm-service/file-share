#!/usr/bin/env bash
# 复现：从旧布局（DATA_DIR/files）升级到 279dc0e 后，已有 blob 全部不可见。
#
# 旧版本（cf242e9 及更早）把本地 blob 存在 DATA_DIR/files/<sha256>；255a431 起改为
# DATA_DIR/blobs，并且当时带「旧目录自动改名」；b4d185d 把这个兼容逻辑整段删掉了。
# 本脚本用一份真实上传造出 blobs/，再把目录改名回 files/ 模拟旧部署，然后重启：
#   - 启动成功，日志里没有任何 files/ 相关的提示；
#   - /api/files 仍列出记录；
#   - 下载 / 签下载地址全部 404（blob 在 files/ 里没人看）。
#
# 只操作临时目录与本地端口，不动仓库里的任何文件。
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
PORT="${PORT:-18095}"
BASE="http://127.0.0.1:${PORT}"
WORK="$(mktemp -d /tmp/fs-blobs-layout.XXXXXX)"
DATA="$WORK/data"
BIN="$WORK/app"
LOG="$WORK/server.log"
SERVER_PID=""

cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill -TERM "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

json_field() { # $1=json $2=field
  python3 -c 'import json,sys;print(json.loads(sys.argv[1]).get(sys.argv[2],""))' "$1" "$2"
}

wait_ready() {
  for _ in $(seq 100); do
    if curl -sf -o /dev/null "$BASE/livez"; then return 0; fi
    sleep 0.1
  done
  echo "服务没有在端口 $PORT 上起来" >&2
  sed -n '1,40p' "$LOG" >&2 || true
  return 1
}

start_server() {
  : >"$LOG"
  DATA_DIR="$DATA" HOST=127.0.0.1 PORT="$PORT" \
    SESSION_SIGNING_KEY=probe_session_signing_key_0123456789 \
    URL_SIGNATURE_KEY=probe_url_signature_key_01234567890 \
    READYZ_TOKEN= \
    "$BIN" >>"$LOG" 2>&1 &
  SERVER_PID=$!
  wait_ready
}

stop_server() {
  kill -TERM "$SERVER_PID"
  wait "$SERVER_PID" 2>/dev/null || true
  SERVER_PID=""
}

login() {
  local resp
  resp="$(curl -s -X POST "$BASE/api/auth" -H 'Content-Type: application/json' \
    -d '{"username":"admin","password":"admin_password_123"}')"
  json_field "$resp" token
}

echo "== 0. 构建被测二进制（HEAD）=="
(cd "$REPO" && go build -o "$BIN" .)
echo "ok"

echo "== 1. 全新部署：上传一份文件，确认 blob 落在 DATA_DIR/blobs/ =="
mkdir -p "$DATA"
cat >"$DATA/users.yaml" <<'EOF'
users:
  - name: admin
    password: "admin_password_123"
    permissions: [read, write]
EOF
start_server
TOKEN="$(login)"
[ -n "$TOKEN" ] || { echo "登录失败" >&2; exit 1; }
printf 'upgrade-probe-content\n' >"$WORK/probe.txt"
SHA="$(sha256sum "$WORK/probe.txt" | cut -d' ' -f1)"
UPLOAD="$(curl -s -X POST "$BASE/api/upload" -H "Authorization: Bearer $TOKEN" \
  -F "file=@$WORK/probe.txt" -F "client_sha256=$SHA")"
FILE_ID="$(json_field "$UPLOAD" id)"
[ -n "$FILE_ID" ] || { echo "上传失败: $UPLOAD" >&2; exit 1; }
echo "  记录 id=$FILE_ID sha=$SHA"
echo "  blobs/<sha> 存在: $([ -f "$DATA/blobs/$SHA" ] && echo yes || echo no)"
BEFORE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/files/$FILE_ID/download")"
echo "  升级前下载状态: $BEFORE"
[ "$BEFORE" = "200" ] || { echo "基线不成立" >&2; exit 1; }
stop_server

echo "== 2. 模拟旧部署：blobs/ 改回旧布局 files/，重启 =="
mv "$DATA/blobs" "$DATA/files"
start_server
TOKEN="$(login)"
LIST="$(curl -s -H "Authorization: Bearer $TOKEN" "$BASE/api/files")"
COUNT="$(python3 -c 'import json,sys;print(len(json.loads(sys.argv[1])["files"]))' "$LIST")"
echo "  /api/files 记录数: $COUNT"
DL="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/files/$FILE_ID/download")"
URL_STATUS="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/files/$FILE_ID/download-url")"
STORAGE="$(curl -s -H "Authorization: Bearer $TOKEN" "$BASE/api/storage")"
echo "  升级后下载状态: $DL（期望 200，实际会 404）"
echo "  升级后 download-url 状态: $URL_STATUS"
echo "  升级后 /api/storage: $STORAGE"
echo "  启动日志里 legacy / files/ 提示: $(grep -Eci 'legacy|DATA_DIR/files' "$LOG" || true)"
echo "  旧路径文件还在磁盘: $([ -f "$DATA/files/$SHA" ] && echo yes || echo no)；新目录: $(ls -A "$DATA/blobs" 2>/dev/null | wc -l) 个文件"

if [ "$DL" = "200" ]; then
  echo "RESULT: FAIL（旧布局仍可用，未复现）"
  exit 1
fi
echo "RESULT: 复现成功 —— 升级后已有记录仍在列表里，但下载 404，且启动不报警、文档未提迁移"