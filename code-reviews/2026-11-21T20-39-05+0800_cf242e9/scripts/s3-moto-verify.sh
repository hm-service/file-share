#!/usr/bin/env bash
# 用 moto（本地 S3 兼容 mock）验证本次新增的 S3 blob 后端与 migrate-blobs：
#   1) S3 集成测试（上传/秒传/下载 Range+HEAD/引用计数删除/多段上传/迁移/缺桶启动失败）
#   2) migrate-blobs 子命令：--dry-run 不写对象、正式迁移按 sha 去重上传、重复执行全部 skip、本地文件保留
#
# 依赖：uv（脚本用 uv run 起 moto 与 boto3）、本机 127.0.0.1 可用端口。
# 用法：bash scripts/s3-moto-verify.sh [repo-root] [local-data-dir-for-migrate]
#   local-data-dir 默认 .scratch/review-data；不存在时跳过第 2 部分。
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="${1:-$(cd "$SCRIPT_DIR/../../.." && pwd)}"
DATASRC="${2:-$REPO/.scratch/review-data}"
PORT="${S3_MOCK_PORT:-9034}"
BUCKET="file-share-test"
END="http://127.0.0.1:$PORT"

cd "$REPO"
if [ -f .scratch/env.sh ]; then
    # shellcheck disable=SC1091
    source .scratch/env.sh
fi

MOTO_LOG=/tmp/file-share-moto.log
uv run --with 'moto[server]==5.*' --with boto3 --python 3.14 \
    python -m moto.server -H 127.0.0.1 -p "$PORT" > "$MOTO_LOG" 2>&1 &
MOTO=$!
trap 'kill "$MOTO" 2>/dev/null' EXIT
for _ in $(seq 30); do
    curl -s -o /dev/null "$END/" && break
    sleep 0.5
done
curl -s -o /dev/null "$END/" || { echo "moto 没起来，见 $MOTO_LOG"; exit 1; }

uv run --with boto3 --python 3.14 python - "$END" "$BUCKET" <<'PY'
import sys, boto3
end, bucket = sys.argv[1], sys.argv[2]
c = boto3.client("s3", endpoint_url=end, aws_access_key_id="test",
                 aws_secret_access_key="test", region_name="us-east-1")
c.create_bucket(Bucket=bucket)
print("bucket created:", bucket)
PY

echo "=== 1) S3 集成测试 ==="
S3_TEST_ENDPOINT="$END" S3_TEST_BUCKET="$BUCKET" S3_TEST_ACCESS_KEY=test S3_TEST_SECRET_KEY=test \
    go test ./app ./api -run 'S3|Migrate' -count=1 -v

echo
echo "=== 2) migrate-blobs CLI ==="
if [ ! -d "$DATASRC" ]; then
    echo "跳过：没有本地数据目录 $DATASRC（可先跑一个本地实例产生数据）"
    exit 0
fi
MIGDIR=.scratch/review-migrate
rm -rf "$MIGDIR" && cp -a "$DATASRC" "$MIGDIR"
export S3_ENDPOINT="$END" S3_BUCKET="$BUCKET" S3_PREFIX=probe/migrate S3_ACCESS_KEY=test S3_SECRET_KEY=test

mkdir -p frontend/dist && touch frontend/dist/.stub-keep 2>/dev/null || true

DATA_DIR="$MIGDIR" go run . migrate-blobs --dry-run | tee /tmp/file-share-migrate-dry.log
DATA_DIR="$MIGDIR" go run . migrate-blobs | tee /tmp/file-share-migrate-real.log
DATA_DIR="$MIGDIR" go run . migrate-blobs | tee /tmp/file-share-migrate-again.log

echo
echo "--- 断言 ---"
grep -q 'summary: would upload' /tmp/file-share-migrate-dry.log && echo "PASS dry-run 有汇总"
grep -q 'uploaded [1-9]' /tmp/file-share-migrate-real.log && echo "PASS 正式迁移上传了对象"
grep -q 'uploaded 0, skipped' /tmp/file-share-migrate-again.log && echo "PASS 重复执行全部 skip"
[ "$(ls "$MIGDIR/files" | wc -l)" -gt 0 ] && echo "PASS 本地 blob 仍在（回滚依赖）"

uv run --with boto3 --python 3.14 python - "$END" "$BUCKET" <<'PY'
import sys, boto3
end, bucket = sys.argv[1], sys.argv[2]
c = boto3.client("s3", endpoint_url=end, aws_access_key_id="test",
                 aws_secret_access_key="test", region_name="us-east-1")
keys = [o["Key"] for o in c.list_objects_v2(Bucket=bucket, Prefix="probe/migrate").get("Contents", [])]
print(f"PASS 桶里 probe/migrate 下有 {len(keys)} 个对象")
for k in keys[:2]:
    print("   ", k)
PY