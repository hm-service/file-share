#!/usr/bin/env bash
# 接口语义探针（279dc0e）：
#   A. DATA_DIR 存在但不是目录时的启动错误文案
#   B. 读写权限矩阵：read-only 用户能读、写接口 403；write-only 用户的 /api/me
#   C. 存储后端不可达：/api/storage healthy=false、签下载地址与上传 503，恢复后自愈
#   D. 删掉用户后重启：旧会话立即失效（DESIGN §3.1 / §6.1 的契约）
#
# 只操作临时目录与本地端口，不改仓库文件。
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
PORT="${PORT:-18096}"
BASE="http://127.0.0.1:${PORT}"
WORK="$(mktemp -d /tmp/fs-api-probe.XXXXXX)"
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

json() { python3 -c 'import json,sys;print(json.loads(sys.argv[1]).get(sys.argv[2],""))' "$1" "$2"; }
status() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

start_server() {
  : >"$LOG"
  DATA_DIR="$DATA" HOST=127.0.0.1 PORT="$PORT" \
    SESSION_SIGNING_KEY=probe_session_signing_key_0123456789 \
    URL_SIGNATURE_KEY=probe_url_signature_key_01234567890 \
    READYZ_TOKEN= "$BIN" >>"$LOG" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 100); do
    if curl -sf -o /dev/null "$BASE/livez"; then return 0; fi
    sleep 0.1
  done
  echo "服务没起来" >&2; sed -n '1,20p' "$LOG" >&2; return 1
}

stop_server() { kill -TERM "$SERVER_PID"; wait "$SERVER_PID" 2>/dev/null || true; SERVER_PID=""; }

login() { # $1=user $2=password
  local resp
  resp="$(curl -s -X POST "$BASE/api/auth" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"password\":\"$2\"}")"
  json "$resp" token
}

users_yaml() { # $1=额外用户块(可为空)
  cat <<EOF
users:
  - name: admin
    password: "admin_password_123"
    permissions: [read, write]
  - name: reader
    password: "reader_password_123"
    permissions: [read]
  - name: writer
    password: "writer_password_123"
    permissions: [write]
${1:-}
EOF
}

(cd "$REPO" && go build -o "$BIN" .)

echo "== A. DATA_DIR 指向普通文件 =="
touch "$WORK/afile"
DATA_DIR="$WORK/afile" PORT="$PORT" HOST=127.0.0.1 \
  SESSION_SIGNING_KEY=probe_session_signing_key_0123456789 \
  URL_SIGNATURE_KEY=probe_url_signature_key_01234567890 \
  READYZ_TOKEN= "$BIN" >"$WORK/a.log" 2>&1 || true
python3 - "$WORK/a.log" <<'PY'
import json,sys
for line in open(sys.argv[1]):
    line=line.strip()
    if not line.startswith("{"): continue
    try: rec=json.loads(line)
    except Exception: continue
    if rec.get("msg")=="init failed":
        print("A. 启动错误:", rec.get("error"))
PY

echo
echo "== B. 读写权限矩阵 =="
mkdir -p "$DATA"
users_yaml >"$DATA/users.yaml"
start_server
ADMIN="$(login admin admin_password_123)"
READER="$(login reader reader_password_123)"
WRITER="$(login writer writer_password_123)"
WRONG="$(login admin wrong_password_12345 || true)"
echo "  登录：admin=$([ -n "$ADMIN" ] && echo ok || echo fail) reader=$([ -n "$READER" ] && echo ok || echo fail) writer=$([ -n "$WRITER" ] && echo ok || echo fail) 错误密码=$([ -z "$WRONG" ] && echo 被拒 || echo 不该成功)"

# 用 admin 造一条可读记录
printf 'permission-probe\n' >"$WORK/p.txt"
SHA="$(sha256sum "$WORK/p.txt" | cut -d' ' -f1)"
UP="$(curl -s -X POST "$BASE/api/upload" -H "Authorization: Bearer $ADMIN" \
  -F "file=@$WORK/p.txt" -F "client_sha256=$SHA")"
FILE_ID="$(json "$UP" id)"
[ -n "$FILE_ID" ] || { echo "造数据失败: $UP" >&2; exit 1; }

echo "  reader GET /api/files:        $(status -H "Authorization: Bearer $READER" "$BASE/api/files")"
echo "  reader GET /api/me:           $(curl -s -H "Authorization: Bearer $READER" "$BASE/api/me")"
echo "  reader GET download:          $(status -H "Authorization: Bearer $READER" "$BASE/api/files/$FILE_ID/download")"
echo "  reader POST share:            $(status -X POST -H "Authorization: Bearer $READER" "$BASE/api/files/$FILE_ID/share")"
echo "  reader POST /api/upload:      $(status -X POST -H "Authorization: Bearer $READER" "$BASE/api/upload")"
echo "  reader PATCH file:            $(status -X PATCH -H "Authorization: Bearer $READER" -H 'Content-Type: application/json' -d '{"filename":"x.txt"}' "$BASE/api/files/$FILE_ID")"
echo "  reader DELETE file:           $(status -X DELETE -H "Authorization: Bearer $READER" "$BASE/api/files/$FILE_ID")"
echo "  writer GET /api/me:           $(status -H "Authorization: Bearer $WRITER" "$BASE/api/me")  ← write-only 账号拿不到自己的权限"
echo "  writer GET /api/files:        $(status -H "Authorization: Bearer $WRITER" "$BASE/api/files")"

echo
echo "== C. 存储后端不可达（本地 blob 目录被移走）=="
mv "$DATA/blobs" "$WORK/blobs-away"
echo "  /api/storage:                 $(curl -s -H "Authorization: Bearer $ADMIN" "$BASE/api/storage")"
echo "  签下载地址:                   $(status -H "Authorization: Bearer $ADMIN" "$BASE/api/files/$FILE_ID/download-url")"
SSH="$(sha256sum "$WORK/p.txt" | cut -d' ' -f1)"
echo "  上传:                         $(status -X POST -H "Authorization: Bearer $ADMIN" -F "file=@$WORK/p.txt" -F "client_sha256=$SSH" "$BASE/api/upload")"
echo "  秒传预检:                     $(status -X POST -H "Authorization: Bearer $ADMIN" -F "client_sha256=$SSH" -F "filename=p.txt" "$BASE/api/check_file")"
mv "$WORK/blobs-away" "$DATA/blobs"
echo "  恢复后 /api/storage:          $(curl -s -H "Authorization: Bearer $ADMIN" "$BASE/api/storage")"
echo "  恢复后签下载地址:             $(status -H "Authorization: Bearer $ADMIN" "$BASE/api/files/$FILE_ID/download-url")"

echo
echo "== D. 从用户表删掉 reader 后重启，旧会话应失效 =="
stop_server
cat >"$DATA/users.yaml" <<'EOF'
users:
  - name: admin
    password: "admin_password_123"
    permissions: [read, write]
EOF
start_server
echo "  reader 旧 token GET /api/files: $(status -H "Authorization: Bearer $READER" "$BASE/api/files")  ← 期望 401"
echo "  admin 旧 token GET /api/files:  $(status -H "Authorization: Bearer $ADMIN" "$BASE/api/files")  ← 期望 200"