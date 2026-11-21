#!/usr/bin/env bash
# 接口探针：在「审查专用实例」上核对本次改动涉及的接口契约（会写数据，别对正式实例跑）。
# 覆盖：/api/storage、三级视图路由、分享链接 ttl 两档与非法值、签名下载（GET/HEAD/Range/篡改）、
# 文件与文件夹排序、文件夹改名/删除、保留一级路径。
#
# 用法：bash scripts/api-probe.sh [base-url] [auth-token]
# 要求：干净实例（除本脚本创建的内容外，没有其它顶级文件夹，否则「文件夹排序」一项会 409）
# 预期：除标出的 400/404 外全部 200/201/204；脚本结束会打印 PASS/FAIL 汇总。
set -uo pipefail

BASE="${1:-http://127.0.0.1:18081}"
AUTH="${2:-review_token_2026}"
pass=0
fail=0

check() { # check <desc> <expected> <actual>
    if [ "$2" = "$3" ]; then pass=$((pass + 1)); echo "PASS $1 ($2)";
    else fail=$((fail + 1)); echo "FAIL $1: want $2, got $3"; fi
}

TOK=$(curl -s -X POST -H 'Content-Type: application/json' -d "{\"token\":\"$AUTH\"}" "$BASE/api/auth" |
    sed -E 's/.*"token":"([^"]+)".*/\1/')
A="Authorization: Bearer $TOK"

check "auth 拿到会话" 88 "${#TOK}"
check "/api/storage 未登录" 401 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/storage")"
check "/api/storage 本地模式" '{"backend":"local"}' "$(curl -s -H "$A" "$BASE/api/storage")"

check "建三级文件夹" 201 "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$A" -H 'Content-Type: application/json' \
    -d '{"path":"探针一级/探针二级/探针三级"}' "$BASE/api/groups")"
check "建四级文件夹被拒" 400 "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$A" -H 'Content-Type: application/json' \
    -d '{"path":"a/b/c/d"}' "$BASE/api/groups")"
check "保留一级路径 s 被拒" 400 "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$A" -H 'Content-Type: application/json' \
    -d '{"path":"s"}' "$BASE/api/groups")"

ENC="%E6%8E%A2%E9%92%88%E4%B8%80%E7%BA%A7/%E6%8E%A2%E9%92%88%E4%BA%8C%E7%BA%A7/%E6%8E%A2%E9%92%88%E4%B8%89%E7%BA%A7"
check "三级视图路由" 200 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/$ENC")"
check "四级路径 404" 404 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/$ENC/%E5%9B%9B")"
check "index.html 不直出" 404 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/index.html")"

printf 'probe-content-%s' "$RANDOM" > /tmp/file-share-api-probe.bin
SHA=$(sha256sum /tmp/file-share-api-probe.bin | cut -d' ' -f1)
UP=$(curl -s -H "$A" -F "file=@/tmp/file-share-api-probe.bin;filename=probe.bin" -F "client_sha256=$SHA" "$BASE/api/upload")
ID=$(echo "$UP" | sed -E 's/.*"id":"([^"]+)".*/\1/')
check "上传返回 id" 32 "${#ID}"
check "秒传命中" true "$(curl -s -H "$A" -F "client_sha256=$SHA" -F "filename=copy-probe.bin" "$BASE/api/check_file" | sed -E 's/.*"exists":(true|false).*/\1/')"

for ttl in 600 86400; do
    code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$A" "$BASE/api/files/$ID/share?ttl=$ttl")
    check "share ttl=$ttl" 200 "$code"
done
for ttl in 0 60 3600 604800 abc -600; do
    code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$A" "$BASE/api/files/$ID/share?ttl=$ttl")
    check "share 非法 ttl=$ttl" 400 "$code"
done

# 注意：JSON 里 "&" 会被编码成 \u0026，必须按 JSON 解析而不是 sed
URL=$(curl -s -X POST -H "$A" "$BASE/api/files/$ID/share?ttl=86400" |
    python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])')
check "匿名签名下载" 200 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE$URL")"
check "签名下载 HEAD" 200 "$(curl -s -o /dev/null -w '%{http_code}' -I "$BASE$URL")"
check "签名下载 Range" 206 "$(curl -s -o /dev/null -w '%{http_code}' -H 'Range: bytes=0-9' "$BASE$URL")"
TAMPERED=$(echo "$URL" | sed -E 's/exp=[0-9]+/exp=9999999999/')
check "篡改 exp 拒绝" 404 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE$TAMPERED")"

check "文件排序" 204 "$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H "$A" -H 'Content-Type: application/json' \
    -d "{\"items\":[{\"id\":\"$ID\",\"sort_order\":0}]}" "$BASE/api/files/order")"
check "文件夹排序集合不符" 409 "$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H "$A" -H 'Content-Type: application/json' \
    -d '{"parent":"","names":["探针一级","不存在的"]}' "$BASE/api/groups/order")"
check "文件夹排序" 204 "$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H "$A" -H 'Content-Type: application/json' \
    -d '{"parent":"","names":["探针一级"]}' "$BASE/api/groups/order")"
check "文件夹改名" 204 "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$A" -H 'Content-Type: application/json' \
    -d '{"path":"探针一级/探针二级","new_name":"探针二级改"}' "$BASE/api/groups/rename")"
check "删除文件夹（保留文件）" 204 "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$A" -H 'Content-Type: application/json' \
    -d '{"path":"探针一级","delete_files":false}' "$BASE/api/groups/delete")"
check "删除记录" 204 "$(curl -s -o /dev/null -w '%{http_code}' -X DELETE -H "$A" "$BASE/api/files/$ID")"

echo
echo "summary: pass=$pass fail=$fail"
rm -f /tmp/file-share-api-probe.bin
[ "$fail" -eq 0 ]