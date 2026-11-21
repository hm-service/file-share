#!/usr/bin/env bash
# 复现：登录页 ?next= 的站内限制可被反斜杠绕过，登录后被带到外站（开放重定向）。
#
# 前端 LoginPage.afterLoginURL 用字符串前缀判断（startsWith("/") 且不以 "//" 开头），
# 但 URL 语法里反斜杠在特殊 scheme 下等价于 "/"：next=/\evil.example 会被浏览器
# 解析成 http://evil.example/。脚本用无头 Chromium 实际点一次登录并观察落到哪个地址。
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
PORT="${PORT:-18100}"
WORK="$(mktemp -d /tmp/fs-next-redirect.XXXXXX)"
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

echo "== 构建二进制（含前端产物）=="
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
  python "$REPO/code-reviews/2026-11-22T00-32-17+0800_279dc0/scripts/login-next-probe.py"
CODE=$?
set -e
echo "(探针退出码 $CODE：1 = 复现成功)"
exit 0