# RESPONSE：cross review 修复记录

- **对应审查记录**：`code-reviews/2026-11-21T20-39-05+0800_cf242e9/REVIEW.md`
- **响应时间**：2026-11-21T21:08:00+08:00（Asia/Shanghai）
- **修复提交**：本目录随修复一起提交（提交信息引用审查目录名）
- **修复范围**：F1–F10 全部修复；无遗留 finding。审查报告的「待确认项」不属于缺陷，逐条说明处理结论（见 §3）。

## 1. 逐项响应

### [P2][F1] `shaMutex` 非法 sha 退回全局锁导致 `DeleteFile` 自死锁

**位置**：`app/app.go`（`shaMutex`）、`app/blobstore.go`（新增 `validBlobSHA`）、`app/blobstore_local.go`、`app/blobstore_s3.go`、`app/files.go`（`DeleteGroup`）。

**做法**：

1. `shaMutex` 不再有「退回全局锁」分支：对任意字符串按前两位十六进制（不足两位就按已有字符）计算分片下标，任何输入都落到 64 个分片之一，不会返回 `&a.mu`，因此 `DeleteFile` 里「先分片锁、再全局锁」不会变成同一把锁重入。
2. 同一根因的两个放大器一起堵上：
   - 新增 `validBlobSHA`（`core.IsSHA256`），本地与 S3 两个 `BlobStore` 实现的 `Head` / `PutFile` / `Open` / `Delete` 入口统一校验。非法 sha 直接返回错误：本地后端不再可能用 `../` 之类的值穿越 `DATA_DIR`，S3 后端也不会因 `sha[:2]` / `sha[2:4]` 越界 panic。
   - `DeleteGroup` 的 blob 清理抽成 `removeBlobIfUnreferenced`（`defer Unlock`），循环体里任何 panic 都不会把分片锁永久留在锁定状态。

**验证**：

- 新增 `app/app_test.go: TestDeleteFileWithMalformedSHA`：向库里插入 `sha256 = "a"` 的记录后调用 `DeleteFile`，goroutine + 3 秒超时断言必定返回（修复前 100% 超时）；同时断言记录确实被删除。
- 新增 `app/app_test.go: TestBlobStoreRejectsMalformedSHA`：`""` / `"a"` / `"abc"` / `"../evil"` / `"a/b"` / 大写 64 位等输入在 `Head`、`Open`、`Delete` 上一律返回错误。
- `go test -race ./...` 通过；审查报告里的复现脚本思路（插入异常 sha 后删记录）已固化成上面的回归测试。

### [P2][F2] 上传成功后移入目标文件夹失败 → 重试造成重复记录

**位置**：`frontend/src/components/UploadDock.vue`。

**做法**：队列项新增 `fileId`，记录「服务端已经建好的记录 id」：

- 秒传命中、上传成功后立刻把 `check.id` / `uploaded.id` 写进 `item.fileId`；只有随后的 `PATCH /api/files/{id}` 成功才清空它。
- `processItem` 开头先判断 `fileId` 非空：这一轮只重发那条 PATCH，成功后直接 `done`，**不再重新上传**（重传会在服务端再插一条记录）。
- PATCH 之前失败（哈希 / 预检 / 上传本身失败）时 `fileId` 仍为空，重试照旧走完整流程。
- 「移除」时一并清掉 `fileId`。

**验证**：Playwright 脚本 `.scratch/verify-f2f5.py`（方法同审查报告 `scripts/ui-retry-duplicate.py`）：在 `/工作` 里上传 `dup-probe.txt`，拦截第一条 PATCH 使其失败 → 队列显示失败 → 点「重试」→ 查询 `/api/files`：

```text
[F2] 队列（PATCH 失败后）: 上传队列 | 1 项 | dup-probe.txt | 23 B | 网络异常 | 重试 | 移除
[F2] 被拦截的 PATCH 次数: 1
[F2] dup-probe.txt 记录: ['工作']
[F2] PASS
```

修复前为 2 条（一条未分组、一条在目标文件夹）；修复后 1 条且分组正确。

### [P3][F3] 列头「修改时间」显示的是 `created_at`

**位置**：`frontend/src/components/ContentTable.vue`、`frontend/src/i18n.ts`、`README.md`、`DESIGN.md`、`frontend/src/components/FileRow.vue`。

**做法**：按最小改法统一语义——列头键 `column.modified` 改名为 `column.uploaded`，文案「上传时间」/「Uploaded」，与详情面板的「上传时间」一致；README 与 DESIGN 的列说明同步改为「上传时间（`created_at`）」；`FileRow` 里的注释一并更新。

**验证**：`vue-tsc` 通过；浏览器实测列头显示「上传时间」/「Uploaded」，数值与详情面板一致。

### [P3][F4] 后端文案表缺 `store.order`

**位置**：`i18n/i18n.go`。

**做法**：补 `"store.order": {"保存文件夹顺序失败", "Failed to save the folder order"}`（键与 `app/files.go` 里 `storeFailure(err, "store.order")` 对齐）。

**验证**：`i18n` 包测试通过；键表无悬空引用（`i18n/i18n_test.go` 的键集合检查通过）。

### [P3][F5] 登录 `?next=` 前缀判断过宽

**位置**：`frontend/src/LoginPage.vue`。

**做法**：`afterLoginURL` 改用 `isAuthPage`：只有 `"/auth"`、`"/auth?…"`、`"/auth/…"` 才算登录页；`/author`、`/authnotes` 这类合法一级文件夹不再被前缀误伤。

**验证**：`scripts/ui-login-next-prefix.py` 的等价脚本 `.scratch/verify-f2f5.py`：未登录打开 `/auth?next=%2Fauthor` → 登录后落到 `/author`。

```text
[F5] 登录后地址: http://127.0.0.1:18081/author
[F5] PASS
```

### [P3][F6] `just check` 不跑 gofmt，`api/storage.go` 非 gofmt-clean

**位置**：`justfile`、`api/storage.go`、`README.md`。

**做法**：

- `check` 改为 `check: fmt-check vet test`；新增 `fmt-check` recipe：`git ls-files --cached --others --exclude-standard -z -- '*.go' | xargs -0 gofmt -l`，输出非空即失败（覆盖未跟踪的新文件，不扫 `node_modules` / `.scratch`）。
- `api/storage.go` 补末尾换行，`gofmt -l` 对本仓库全部 Go 文件无输出。
- README 两处说明统一为「gofmt/vet/go test + 前端 typecheck」。

**验证**：故意放一个格式错误的 `.go` 文件后 `just fmt-check` 输出该文件并 `exit 1`；删除后 `just check` 全绿。

### [P3][F7] `go.mod` 未 tidy

**位置**：`go.mod`、`go.sum`。

**做法**：执行 `go mod tidy`：`github.com/minio/minio-go/v7` 从带 `// indirect` 的块移到直接依赖块；`go.sum` 按 tidy 结果补齐/更新（testify、zeebo/assert 等 indirect 校验和，`golang.org/x/{sync,tools}` 随工具链升版）。

**验证**：`go mod tidy -diff` 无输出；`go build ./...`、`go test ./...`、`go test -race ./...` 均通过。

### [P3][F8] 死代码、失效注释与未使用文案键

**位置**：`frontend/src/format.ts`、`frontend/src/routes.ts`、`frontend/src/session.ts`、`frontend/src/i18n.ts`、`core/validate.go`。

**做法**：

- 删除 `formatDayHeading` 及其专属文案 `date.group-heading`，并把文件头注释改成只描述大小 / 速度 / 时间。
- `routes.ts` 注释「超过两段」→「超过三段」；`session.ts` 注释去掉已删除的 `?section=`。
- 删除 21 个无调用方的文案键（审查列出的 20 个 + `date.group-heading`），union 类型与值表同步删除，i18n 键表 133 → 112。
- 删除 `core/validate.go` 里的 `ReservedGroupRoot`（全仓库无调用方），`gofmt` 重新整理该文件。

**验证**：用脚本核对「`t("…")` 静态引用集合」与 i18n 键表一致后删除，`vue-tsc` 通过；Playwright 打开 `/`、`/favorites`、`/ungrouped`、`/F10` 检查 `body` 文本，未出现 `nav.*` / `action.*` 等原始键名，无 console error。

### [P3][F9] README 保留一级路径清单漏 `s`

**位置**：`README.md`。

**做法**：清单补 `s`（`/s/{id}` 是签名下载入口），与 `core/validate.go` 的 `reservedGroupNames` 一致。

**验证**：逐项比对 `reservedGroupNames` 与 README 清单，元素集合一致。

### [P3][F10] 拖拽状态两处维护、`hasFiles` 三份、`applyGroupOrder` 非全序

**位置**：`frontend/src/dnd.ts`（新增）、`frontend/src/App.vue`、`frontend/src/components/ContentTable.vue`、`frontend/src/components/DirTree.vue`。

**做法**：

- 新增 `src/dnd.ts` 导出 `hasFiles`，App / ContentTable / DirTree 三处改为引用同一实现。
- 拖拽状态收敛到 App 的 `draggingFileId`（单一事实来源）：ContentTable 删除内部 `draggingId`，改由 prop `draggingFileId` 接收，`onDirDragOver` / `onDirDrop` 都读这个 prop；SortableJS 的 `onStart` / `onEnd` 仍只发 `drag-start` / `drag-end` 事件，由 App 维护状态。
- `applyGroupOrder` 改为「先过滤出目标父级的兄弟组、组内按提交顺序排序、再原位替换」，比较器在所有元素上都有全序，不再依赖「同父级在数组里连续」的隐含前提。

**验证**：`.scratch/verify-f10.py`（Playwright，独立实例 18081）：

```text
[F10] 文件夹顺序: ['子A', '子B'] -> ['子B', '子A'] PUT: 1   PASS
[F10] 文件顺序: ['甲.txt', '乙.txt'] -> ['乙.txt', '甲.txt'] PUT: 1   PASS
[F10] 移动到文件夹 PATCH: 1   PASS
[F10] pageerror: []
```

## 2. 未修复项

无。

## 3. 待确认项的结论（未改动，说明理由）

1. **S3 上传信号量不响应取消**：保持现状。`FinishUpload` 用不可取消的 context 是有意为之（客户端断开后仍要落库，否则会留下已写入对象但无记录的孤儿）；排队时间受 minio transport 超时约束，不构成无限阻塞。记录在 DESIGN「已知取舍」外，暂不引入取消即放弃的语义。
2. **`PUT /api/groups/order` 空 `names` 对不存在 `parent` 返回 204**：保持现状。语义是「用提交的集合覆盖该父级」，空集合等于「该父级下没有文件夹」；前端不会对不存在的父级提交空集合。若后续要区分可改成 404，属产品决策。
3. **前端 O(分组数 × 文件数) 的列表计算**：保持现状。当前数据规模（百级文件、十几级文件夹）实测无感；上万文件的性能优化留到有真实需求时再做，避免提前引入索引缓存。
4. **`migrate-blobs --help` 退出码 2**：保持现状（Go `flag` 默认行为）。作为运维子命令，`--help` 手动执行时退出码不影响使用；若纳入自动化脚本再改为 0。
5. **旧 `?section=` 深链接被静默忽略**：保持现状。该参数已整体删除，前端不认识即忽略；旧书签打开仍是有效列表页，不产生错误。
6. **`S3_ENDPOINT` 尾部斜杠**：保持现状。解析逻辑已把 path/query 的非空情况拒绝，尾部 `/` 被接受并规范化；DESIGN 表述略宽但行为安全，不改代码。

## 4. 构建与测试

| 命令 | 结果 |
| --- | --- |
| `just check`（新增 gofmt 检查 + vet + test + 前端 typecheck） | 通过 |
| `just fmt-check`（故意放入格式错误文件） | 按预期失败并列出文件 |
| `go test ./... -count=1` | 通过（7 个包全 ok） |
| `go test -race ./... -count=1` | 通过 |
| `go test ./... -cover -count=1` | 通过；`app` 54.9% → 55.1%、`core` 93.9% → 95.0%，README 表格已同步 |
| `cd frontend && pnpm build` | 通过 |
| `go build ./...` / `go build -o .scratch/app-ui .` | 通过 |
| `just build`（linux/amd64 + linux/arm64 交叉编译） | 通过 |

**测试数量变化**：`app` 包新增 2 个测试（`TestDeleteFileWithMalformedSHA`、`TestBlobStoreRejectsMalformedSHA`），其余包测试数量不变。

**UI 验证**：`.scratch/verify-f2f5.py`（F2、F5）、`.scratch/verify-f10.py`（F10）、i18n 泄漏冒烟，均通过；无 console error。验证结束后独立实例（18081）与其 `DATA_DIR` 已清理，`18080` 演示服务已用新构建重启。

## 5. 未自动验证项

- 真实 S3 网关（MinIO / Ceph / R2 / OSS）兼容性：本次修复未触及 S3 传输逻辑，但 `validBlobSHA` 改变了非法 sha 的 S3 行为（现在在入口返回错误）；真实网关未复测，moto 环境下的 S3 集成测试由 `just test-s3` 覆盖（需提供 `S3_TEST_*`，本次未跑）。
- `just test-s3`（默认 skip）：与审查报告一致，本次未配置 S3 测试桶。
- 超大文件、多实例共桶、k8s / arm64 运行、Safari/iOS 等浏览器行为：与审查报告 §4.6 相同，未在本次修复中验证。