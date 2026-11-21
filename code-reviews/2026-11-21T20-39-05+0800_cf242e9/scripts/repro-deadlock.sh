#!/usr/bin/env bash
# 复现 F1：app.DeleteFile 在记录 sha256 长度 < 2 时永久自死锁。
#
# 做法：把当前工作区复制到临时目录，把 deadlock-probe_test.go.txt 放进副本的 app/，
# 只对副本跑 go test，不动正式工作区。
#
# 用法：bash scripts/repro-deadlock.sh [repo-root]
# 退出码：0 = 死锁已复现（预期）；1 = 未复现。
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="${1:-$(cd "$SCRIPT_DIR/../../.." && pwd)}"
WORK="$(mktemp -d /tmp/file-share-deadlock.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

echo "repo    = $REPO"
echo "workdir = $WORK"

# 只复制构建所需的内容（frontend/dist 被 go:embed 用；.scratch/.git/node_modules 太大且无关）
( cd "$REPO" && tar -cf - \
    --exclude='./.git' --exclude='./.scratch' --exclude='./.data*' \
    --exclude='./frontend/node_modules' --exclude='./.shots' \
    --exclude='./code-reviews' . ) | tar -xf - -C "$WORK"

cp "$SCRIPT_DIR/deadlock-probe_test.go.txt" "$WORK/app/deadlock_probe_test.go"

if [ -f "$REPO/.scratch/env.sh" ]; then
    # shellcheck disable=SC1091
    source "$REPO/.scratch/env.sh"
fi

cd "$WORK"
# go:embed frontend/dist 需要至少有文件；副本里通常已经有真实产物，缺失时补占位
mkdir -p frontend/dist && touch frontend/dist/.stub-keep

set +e
timeout 120 go test ./app -run TestProbeDeleteFileWithMalformedSHA -count=1 -v 2>&1 | tee /tmp/file-share-deadlock.log
status=${PIPESTATUS[0]}
set -e

if grep -q "永久自死锁" /tmp/file-share-deadlock.log; then
    echo
    echo "结果：死锁已复现（F1）。日志见 /tmp/file-share-deadlock.log"
    exit 0
fi
echo
echo "结果：未复现（go test 退出码 $status）"
exit 1