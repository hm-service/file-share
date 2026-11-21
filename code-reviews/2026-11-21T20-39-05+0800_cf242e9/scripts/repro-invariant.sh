#!/usr/bin/env bash
# 正向验证：sha 分片锁重构后「记录还在 => blob 必须还在」的不变量在并发上传 + 删除下成立。
#
# 用法：bash scripts/repro-invariant.sh [repo-root]
# 退出码：0 = 通过（不变量成立）；非 0 = 失败。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="${1:-$(cd "$SCRIPT_DIR/../../.." && pwd)}"
WORK="$(mktemp -d /tmp/file-share-invariant.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

echo "repo    = $REPO"
echo "workdir = $WORK"

( cd "$REPO" && tar -cf - \
    --exclude='./.git' --exclude='./.scratch' --exclude='./.data*' \
    --exclude='./frontend/node_modules' --exclude='./.shots' \
    --exclude='./code-reviews' . ) | tar -xf - -C "$WORK"

cp "$SCRIPT_DIR/invariant-probe_test.go.txt" "$WORK/app/invariant_probe_test.go"
mkdir -p "$WORK/frontend/dist" && touch "$WORK/frontend/dist/.stub-keep"

if [ -f "$REPO/.scratch/env.sh" ]; then
    # shellcheck disable=SC1091
    source "$REPO/.scratch/env.sh"
fi

cd "$WORK"
CGO_ENABLED=1 go test ./app -run TestProbeUploadDeleteInvariant -count=1 -race -v