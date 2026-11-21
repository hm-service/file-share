#!/usr/bin/env bash
# 起一份独立实例（端口 18105）后跑 ui-upload-retry-probe.py：
# 验证上一轮 F2 的修复（PATCH 失败后重试只补 PATCH、不重新上传）在当前 HEAD 上仍然成立。
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
PORT="${PORT:-18105}"
WORK="$(mktemp -d /tmp/fs-ui-retry.XXXXXX)"
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

(cd "$REPO" && go build -o "$BIN" .)
mkdir -p "$DATA"
cat >"$DATA/users.yaml" <<'EOF'
users:
  - name: admin
    password: "admin_password_123"
    permissions: [read, write]
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

set +e
uv run --with playwright==1.65.2 --python 3.14 --no-project \
  python "$REPO/code-reviews/2026-11-22T00-32-17+0800_279dc0/scripts/ui-upload-retry-probe.py"
CODE=$?
set -e
grep -E 'level":"ERROR"' "$LOG" | head -5 || true
exit "$CODE"