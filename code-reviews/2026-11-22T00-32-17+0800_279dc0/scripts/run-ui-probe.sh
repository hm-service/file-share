#!/usr/bin/env bash
# 起一份独立实例（端口 18097）后跑 ui-permission-probe.py：
#   - 只读账号界面是否真的隐藏全部写入口
#   - 文件夹树是否默认只展开到第二级
#   - 侧栏拖拽调宽的上限与「刷新回默认」
#   - 存储后端不可达时侧栏红点 / 文案
#
# 需要前端产物已构建（frontend/dist）：脚本会先 pnpm build，再 go build。
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
PORT="${PORT:-18097}"
WORK="$(mktemp -d /tmp/fs-ui-probe.XXXXXX)"
DATA="$WORK/data"
BIN="$WORK/app"
LOG="$WORK/server.log"
SERVER_PID=""

cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill -TERM "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  # 探针可能把 blobs 目录改名后失败退出，先恢复再删
  if [ -d "$DATA/blobs.away" ] && [ ! -d "$DATA/blobs" ]; then
    mv "$DATA/blobs.away" "$DATA/blobs"
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "== 构建前端与二进制 =="
(cd "$REPO/frontend" && pnpm build >/dev/null)
(cd "$REPO" && go build -o "$BIN" .)

mkdir -p "$DATA"
cat >"$DATA/users.yaml" <<'EOF'
users:
  - name: admin
    password: "admin_password_123"
    permissions: [read, write]
  - name: reader
    password: "reader_password_123"
    permissions: [read]
EOF

DATA_DIR="$DATA" HOST=127.0.0.1 PORT="$PORT" \
  SESSION_SIGNING_KEY=probe_session_signing_key_0123456789 \
  URL_SIGNATURE_KEY=probe_url_signature_key_01234567890 \
  READYZ_TOKEN= "$BIN" >>"$LOG" 2>&1 &
SERVER_PID=$!
for _ in $(seq 100); do
  if curl -sf -o /dev/null "http://127.0.0.1:$PORT/livez"; then break; fi
  sleep 0.1
done

echo "== UI 探针 =="
set +e
DATA_DIR="$DATA" BASE="http://127.0.0.1:$PORT" \
  uv run --with playwright==1.65.2 --python 3.14 --no-project \
  python "$REPO/code-reviews/2026-11-22T00-32-17+0800_279dc0/scripts/ui-permission-probe.py"
CODE=$?
set -e

echo "== 服务端错误日志 =="
grep -E 'level":"ERROR"' "$LOG" | head -10 || true
exit "$CODE"