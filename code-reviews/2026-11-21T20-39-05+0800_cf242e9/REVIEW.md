# Cross review：file-share

- **被审查提交**：`cf242e92be7029623fa2bf7674a75bc067d4fa47`（分支 `main`，工作区 dirty，未提交改动即待审查内容）
- **审查开始时间**：2026-11-21T20:39:05+08:00（Asia/Shanghai）
- **审查者**：code-reviewer（本文件即正式审查记录，修复由 fixer 按本文逐项处理并写 RESPONSE.md）
- **审查范围**：
  - A 部分（上一轮改动）：本项目**第一次** cross review，`code-reviews/` 目录此前不存在（`git ls-files code-reviews` 为空），因此把工作区相对 HEAD `cf242e9` 的**全部未提交改动**当作待审查提交：34 个已跟踪文件改动（2160 插入 / 3763 删除）+ 11 个新文件。
  - B 部分（全项目）：Go 后端 `api/ app/ core/ db/ httpx/ i18n/ main.go migrate.go web.go internal/` 与前端 `frontend/src/` 全量扫描；契约以 `DESIGN.md`、`README.md`（本次改动同步更新后的版本）为准。
- **审查方式**：静态阅读 + 构建/测试 + 独立实例上的接口探针 + 无头 Chromium（Playwright）UI 探针 + moto 模拟 S3 的集成验证。所有复现脚本在 `scripts/` 下，生产代码与测试代码均未修改。

## 1. 工作区状态

### 1.1 审查开始时的 `git status --short`（45 项）

```text
 M DESIGN.md
 M README.md
 M api/api_test.go
 M api/files.go
 M api/router.go
 M app/app.go
 M app/app_test.go
 M app/files.go
 M app/upload.go
 M core/signurl.go
 M core/signurl_test.go
 M core/validate.go
 M docs/screenshot-main-en.png
 M docs/screenshot-main-zh.png
 M docs/screenshot-mobile-en.png
 M docs/screenshot-mobile-zh.png
 M frontend/src/App.vue
 M frontend/src/api.ts
 D frontend/src/components/FileList.vue
 M frontend/src/components/FileRow.vue
 M frontend/src/i18n.ts
 M frontend/src/icons.ts
 M frontend/src/routes.ts
 M frontend/src/style.css
 M frontend/src/types.ts
 M go.mod
 M go.sum
 M i18n/i18n.go
 M internal/testenv/testenv.go
 M justfile
 M k8s.file-share.yaml
 M main.go
 M main_test.go
 M web.go
?? api/s3_test.go
?? api/storage.go
?? app/blobstore.go
?? app/blobstore_local.go
?? app/blobstore_s3.go
?? app/migrate.go
?? app/s3_test.go
?? frontend/src/components/ContentTable.vue
?? frontend/src/components/DirTree.vue
?? internal/testenv/s3.go
?? migrate.go
```

### 1.2 审查期间工作区是否变化

- 已跟踪文件的 diff 在审查开始与结束时逐字节一致（`git diff` 比对通过），审查结论覆盖上述 34 个改动文件与 11 个新文件。
- 审查期间新增、**不属于被审查内容**的部分：本目录 `code-reviews/`（本次审查产物，未跟踪）、`.scratch/review-*`（审查用临时实例数据，已清理）、`frontend/dist/`（gitignored，跑 `pnpm build` 重新生成；文件名是内容哈希，与原有产物同名）。
- `.scratch/app-ui` 按验收命令 `go build -o .scratch/app-ui .` 被重新构建（gitignored 的调试二进制，未删除；fixer 在 18080 的演示进程仍在运行，未受影响）。此外只读访问了 `.scratch/data-live` 与既有环境变量，未读取/打印任何真实密钥。
- 未对任何生产代码 / 测试代码 / 文档做修改；审查期间跑过的本地实例（18081/18082/18083 与 moto 9033）已全部停止，独立 DATA_DIR 已清理。

## 2. 发现

严重度：P0 数据丢失/安全/不可恢复；P1 正常使用功能错误；P2 特定输入、边界或时序错误；P3 健壮性/可维护性/体验。下文每个发现都附了可复现步骤或代码依据。

### [P2][F1] `shaMutex` 的「非法 sha 退回全局锁」兜底会让 `DeleteFile` 永久自死锁

**位置**：[`app/app.go:290-299`](app/app.go#L290-L299)（`shaMutex`，兜底分支在 292-296）、[`app/files.go:315-333`](app/files.go#L315-L333)（`DeleteFile`，322-326 行连续加两把同名锁）、[`app/blobstore_s3.go:84-90`](app/blobstore_s3.go#L84-L90)（`key()`）、[`app/files.go:190-209`](app/files.go#L190-L209)（`DeleteGroup` 的解锁循环）。

**触发条件**：文件表里出现 `sha256` 长度 < 2 的记录（手工修复/恢复库、外部工具写库、或将来新增的写库路径绕过 `core.IsSHA256`）。`shaMutex` 的兜底分支返回 `&a.mu`，而 `DeleteFile` 在拿到这把「sha 锁」之后还会再加一次 `a.mu`：

```go
lock := a.shaMutex(record.SHA256) // len(sha) < 2 → 返回 &a.mu
lock.Lock()
defer lock.Unlock()
a.mu.Lock()                        // 同一个 sync.Mutex 第二次加锁 → 永久阻塞
```

**实际结果**：`sync.Mutex` 不可重入，该请求永久卡死且 `a.mu` 再也不释放；此后所有写操作（上传落库、秒传补记录、建/改名/删除文件夹、批量排序）全部阻塞，进程只能重启。

**复现**：`bash scripts/repro-deadlock.sh`（把仓库复制到临时目录，插入 `scripts/deadlock-probe_test.go.txt` 后只对副本跑测试）：

```text
=== RUN   TestProbeDeleteFileWithMalformedSHA
    deadlock_probe_test.go:39: DeleteFile 3 秒未返回：a.mu 被同一个 goroutine 锁了两次，永久自死锁
--- FAIL: TestProbeDeleteFileWithMalformedSHA (3.04s)
```

同一根因还有两处静态可证的放大器：

- `s3BlobStore.key()` 直接 `sha[:2]` / `sha[2:4]`，sha 长度 < 4 时 panic（本地实现 `filepath.Join` 不会）。
- `DeleteGroup` 的循环（`app/files.go:192-209`）用「手动 lock + 手动 unlock」而不是 `defer`；若循环体里 `Blobs.Delete`（S3 模式）panic，`a.mu` 会永久保持锁定（`net/http` 只恢复 goroutine，不会解锁）。

**修复建议**：兜底分支不要复用 `a.mu`——要么固定映射到一个独立 fallback 分片，要么让 `shaMutex` 按 `for i := 0; i < 2 && i < len(sha); i++` 计算下标（短 sha 也落到 64 个分片之一）；同时给 S3 的 `key()` 加长度校验，`DeleteGroup` 循环改成对每个 sha 调用一个带 `defer Unlock` 的小函数，避免任何 panic 泄漏锁。

### [P2][F2] 上传成功后「移入目标文件夹」失败会被当成整项失败，重试造成重复记录

**位置**：[`frontend/src/components/UploadDock.vue:186-192`](frontend/src/components/UploadDock.vue#L186-L192)（秒传分支）、[`frontend/src/components/UploadDock.vue:214-227`](frontend/src/components/UploadDock.vue#L214-L227)（上传成功后 PATCH，PATCH 抛错落到 `failed`）、[`frontend/src/components/UploadDock.vue:257-291`](frontend/src/components/UploadDock.vue#L257-L291)（`canRetry` / `retryItem`）。

**触发条件**：`/api/upload` 或 `/api/check_file` 已经成功（服务端已插入记录），紧接着的 `PATCH /api/files/{id} {"group_path": …}` 失败（网络抖动、服务重启、代理断连）。在文件夹视图里上传时 `item.group` 恒非空，所以这条 PATCH 每次都会发。

**实际结果**：队列把整项记成「网络异常」并允许重试；重试会把文件完整再走一遍上传流程，服务端 `InsertFile` 再插一条记录（blob 被内容寻址去重，但记录不去重）。最终同一内容出现两条同名记录，一条留在未分组、一条在目标文件夹，列表刷新前用户看到的是「失败」。

**复现**：`python scripts/ui-retry-duplicate.py`（拦截第一条 PATCH 让它失败，然后点队列的「重试」）：

```text
queue after forced PATCH failure: 上传队列 | 1 项 | dup-probe.txt | 23 B | 网络异常 | 重试 | 移除
dup-probe.txt record count: 2 ['', '工作']
```

**修复建议**：把「已创建记录的 id」与「是否已上传成功」记进队列项；上传成功后的 PATCH 失败只允许重试 PATCH，不再重新上传（或在 `/api/upload` 增加可选的 `group_path` 字段，一次请求完成）。

### [P3][F3] 表格列「修改时间」显示的其实是 `created_at`（上传时间）

**位置**：[`frontend/src/components/ContentTable.vue:186`](frontend/src/components/ContentTable.vue#L186)（列头用 `column.modified`）、[`frontend/src/i18n.ts:329`](frontend/src/i18n.ts#L329)（`column.modified` = 「修改时间」/「Modified」）、[`frontend/src/components/FileRow.vue:405`](frontend/src/components/FileRow.vue#L405)（`formatTime(file.created_at)`）；详情面板把同一个字段叫「上传时间」（`frontend/src/i18n.ts:269`、`frontend/src/components/FileDetailDialog.vue:39`）。`DESIGN.md:114` 也把这个列写成「修改时间」。

**触发条件**：任何文件被重命名或移动文件夹后（`created_at` 不变，schema 也没有 `updated_at`）。

**实际结果**：用户看到列头写「修改时间」，但数值永远不会随修改变化；同一个值在详情面板里叫「上传时间」。属于契约与界面语义不一致（要么列头/文档错，要么数据模型缺字段）。

**修复建议**：把列头与 `DESIGN.md` 一并改成「上传时间」（最小改动），或给表加 `updated_at` 并在改名/移动时更新。

### [P3][F4] 后端文案表缺 `store.order`，文件夹排序的 500 会把原始键名回给前端

**位置**：[`app/files.go:80`](app/files.go#L80)（`storeFailure(err, "store.order")`）、[`i18n/i18n.go:44-54`](i18n/i18n.go#L44-L54)（`store.*` 文案块里没有该键）。

**触发条件**：`PUT /api/groups/order` 时数据库写入失败（磁盘满、库损坏等非 409 错误）。

**实际结果**：`handleSetGroupOrder` → `storeFailure` → `core.Fail(500, "store.order")` → `i18n.T` 找不到键会原样返回键名，响应体是 `{"detail":"store.order"}`，前端 toast 直接显示这个英文键名。

**修复建议**：在 `i18n/i18n.go` 补 `"store.order"` 的中英文案（例如「保存文件夹顺序失败」/「Failed to save folder order」）。

### [P3][F5] 登录 `?next=` 的前缀判断过宽：名字以 `auth` 开头的一级文件夹登录后丢目标

**位置**：[`frontend/src/LoginPage.vue:24`](frontend/src/LoginPage.vue#L24)（`next.startsWith("/auth")`）。

**触发条件**：未登录打开 `/author`、`/authnotes` 之类的一级文件夹深链接（`core.reservedGroupNames` 只拒绝精确的 `auth`，这些名字是合法文件夹），登录成功后。

**实际结果**：`afterLoginURL()` 把 `/author` 当成登录页地址丢弃，回退到 `/`，深链接目标丢失（`?next=` 的用途被破坏；仅名字以 auth 开头时才发生，故列为 P3）。

**复现**：`python scripts/ui-login-next-prefix.py`：

```text
login url : http://127.0.0.1:18081/auth?next=%2Fauthor
final url : http://127.0.0.1:18081/
FAIL 目标丢失（落到了 http://127.0.0.1:18081/）
```

**修复建议**：只拦真正的登录页：`next === "/auth" || next.startsWith("/auth?") || next.startsWith("/auth/")`。

### [P3][F6] `just check` 实际不跑 gofmt，与 DESIGN/README 的描述不符；本次新增 `api/storage.go` 不是 gofmt-clean

**位置**：[`justfile:40`](justfile#L40)（`check: vet test`，只跑 vet/test/前端 typecheck；`fmt` 是独立 recipe）、[`DESIGN.md:314`](DESIGN.md#L314)（「`just check`（gofmt / vet / test + 前端 typecheck）」）、[`README.md:83`](README.md#L83)（注释同样写 gofmt/vet/test）、[`api/storage.go`](api/storage.go)（新文件，结尾缺换行）。

**触发条件**：任何格式不合 gofmt 的改动。

**实际结果**：文档承诺的检查不会执行；本次新增的 `api/storage.go` 就漏了过去——`gofmt -l api/storage.go` 会列出该文件（文件末尾缺换行）。注意 `README.md:328` 又写成 `just check # vet/test + 前端 typecheck`，与第 83 行自相矛盾。

**修复建议**：在 `check` recipe 里加 `gofmt -l`（非空即失败）并修好新文件；把 README 两处说法统一。

### [P3][F7] `go.mod` 未 `go mod tidy`：`minio-go/v7` 是直接依赖却标成 indirect

**位置**：[`go.mod:20`](go.mod#L20)。

**触发条件**：执行 `go mod tidy -diff`。

**实际结果**：`go mod tidy` 会把 `github.com/minio/minio-go/v7 v7.4.1` 从第二个 require 块（带 `// indirect`）挪到第一个直接依赖块。属于依赖记录不准确，`go mod tidy` 校验（或 CI 的 tidy 检查）会失败。

**修复建议**：跑一次 `go mod tidy` 并提交 `go.mod` / `go.sum`。

### [P3][F8] UI 重写与历次改动留下的死代码、失效注释与未使用文案键

**位置与事实**（全部可用 grep 复核）：

- [`frontend/src/format.ts:36-37`](frontend/src/format.ts#L36-L37)：`formatDayHeading` 已无任何调用（「最近」视图的日期分段标题在本次重写里删掉了），但函数仍导出；同文件 [`frontend/src/format.ts:4`](frontend/src/format.ts#L4) 的注释仍在讲「年份交给『最近』视图的日期分段标题展示」。
- [`frontend/src/routes.ts:7`](frontend/src/routes.ts#L7)：注释仍写「超过两段的路径返回 null」，代码已是 `> 3`（`MaxViewSegments` 2→3 只改了代码没改注释）。
- [`frontend/src/session.ts:57`](frontend/src/session.ts#L57)：注释仍提 `?section=` 参数，该功能（分组段筛选）本次已整体删除。
- [`frontend/src/i18n.ts:346-349`](frontend/src/i18n.ts#L346-L349)：本次**新增**但没有使用者的键 `action.open`、`action.detail`、`upload.drop-folder`；另有 17 个旧键在重写后也不再使用（`nav.views`、`nav.groups`、`nav.group-search-placeholder`、`nav.no-group-results`、`nav.resize-sidebar`、`nav.recent-count`、`nav.empty-direct`、`nav.empty-direct-hint`、`upload.hint`、`upload.hint-mobile`、`upload.group-hint`、`action.download-group`、`action.refresh`、`group.all`、`group.direct-files`、`group.empty-child`、`group.upload-target`）。
- [`core/validate.go:137-141`](core/validate.go#L137-L141)：`ReservedGroupRoot` 全仓库无调用方（历史遗留）。

**实际结果**：不影响运行，但每次改动都要多判断一遍这些是否还在用；新增的 3 个未用键说明文案表已经开始积累死数据。

**修复建议**：删除 `formatDayHeading` 与相关注释/键；确认 `ReservedGroupRoot` 无计划后删除。

### [P3][F9] README 的「保留一级路径」清单漏了 `s`

**位置**：[`README.md:188`](README.md#L188)（列出 `api`、`auth`、`assets`、`readyz`、`livez`、`favorites`、`ungrouped`、`favicon.svg`）对比 [`core/validate.go:69-79`](core/validate.go#L69-L79)（`reservedGroupNames` 还包含 `s`）。

**触发条件**：照 README 的清单建一个名为 `s` 的一级文件夹。

**实际结果**：`POST /api/groups {"path":"s"}` 返回 400「分组名无效」；而 README 没说 `s` 是保留名（`/s/{id}` 是签名下载入口）。`DESIGN.md:307` 用「等」概括不受影响。

**验证**：`bash scripts/api-probe.sh` 中「保留一级路径 s 被拒」一项实际返回 400。

**修复建议**：README 清单补上 `s`。

### [P3][F10] 拖拽实现：同一状态两处维护、三份重复 helper、一个非全序比较器

**位置**：

- 同一个「正在拖哪个文件」事实有两份状态：[`frontend/src/App.vue:689`](frontend/src/App.vue#L689)（`draggingFileId`，用于整页拖放与树/文件夹落点判定）与 [`frontend/src/components/ContentTable.vue:57`](frontend/src/components/ContentTable.vue#L57)（`draggingId`，用于文件夹行 dropEffect），两者分别由各自的 Sortable 回调维护。
- `hasFiles(event)` 在 [`App.vue:691`](frontend/src/App.vue#L691)、[`ContentTable.vue:115`](frontend/src/components/ContentTable.vue#L115)、[`DirTree.vue:74`](frontend/src/components/DirTree.vue#L74) 各写一遍。
- [`App.vue:386-391`](frontend/src/App.vue#L386-L391) 的 `applyGroupOrder` 比较器对不同 `parent` 的两个元素返回 0（不是全序）：当前之所以正确，只是依赖服务端 `Groups()` 把同一父级排成连续块这一隐含前提。

**实际结果**：本次实测文件夹拖拽排序、文件拖拽排序、拖到树/文件夹行移动文件都正常（见 §4），所以不是功能缺陷；但重复状态符合项目 AGENTS「单一事实来源」的反例，比较器一旦遇上不连续输入就会打乱其它层级的顺序。

**修复建议**：把拖拽状态收敛到一处（例如 App 持有、通过 provide/inject 或 props 下发），`hasFiles` 抽成 `src/dnd.ts`；`applyGroupOrder` 改成先按 parent 分组再在组内重排，或直接只重建目标父级的连续块。

## 3. 待确认项（未能定性为缺陷，需产品/运维确认）

1. **S3 上传信号量不响应取消**：[`app/blobstore_s3.go:123`](app/blobstore_s3.go#L123) 的 `s.uploadSem <- struct{}{}` 不监听 `ctx`。minio-go 默认 transport 有 30s 连接 / 60s 响应头超时，因此不是无限阻塞；但客户端已断开时请求仍会排队并完整上传（`FinishUpload` 故意用不可取消的 `context.WithoutCancel`）。是否需要「取消即放弃」待确认。
2. **`PUT /api/groups/order` 的边界语义**：`parent` 不存在且 `names` 为空时返回 204（空集合被视为「覆盖全部」），见 [`app/files.go:55-81`](app/files.go#L55-L81)、[`db/file.go:489-541`](db/file.go#L489-L541)。是否应 404 / 409 待确认。
3. **前端列表计算复杂度**：[`frontend/src/App.vue:86-97`](frontend/src/App.vue#L86-L97) 的 `counts` 与 [`App.vue:126-132`](frontend/src/App.vue#L126-L132) 的 `dirs` 每次刷新都会对每个文件夹做一次全量 `files` 过滤，复杂度 O(分组数 × 文件数)；`subtreeFiles` 在统计弹窗和模板里还会再扫。万级文件时是否明显变慢未实测。
4. **`migrate-blobs --help` 退出码**：`flag.ErrHelp` 未特判，[`migrate.go:20-22`](migrate.go#L20-L22) 返回 2（惯例 0）。是否算缺陷（自动化脚本可能在意）待确认。
5. **旧 `?section=` 深链接被静默忽略**：上一版「分组段筛选」已整体移除，带 `?section=` 的旧书签现在静默显示全部（不报错也不清理地址）。是否需要提示或重定向待确认。
6. **S3_ENDPOINT 尾部斜杠**：[`main.go:420`](main.go#L420) 接受 `http://host:port/`（仅拒绝其它 path/query），与 `DESIGN.md:42`「不带路径 / 查询」的表述略有出入。

## 4. 构建、测试与验证记录

### 4.1 环境与构建（全部通过）

| 命令 | 结果 |
| --- | --- |
| `source .scratch/env.sh && just check` | 通过：`go vet ./...`、`go test ./... -count=1`（7 个包全 ok）、`pnpm install --frozen-lockfile`、`pnpm typecheck`（`vue-tsc -b`） |
| `go test ./... -count=1` | 通过（root/api/app/core/db/httpx/i18n 全 ok，internal/testenv 无测试文件） |
| `CGO_ENABLED=1 go test -race ./... -count=1` | 通过（无 data race 报告） |
| `cd frontend && pnpm build` | 通过（vite build，产物写回 gitignored `frontend/dist`） |
| `go build -o .scratch/app-ui .` | 通过（单二进制，含新前端产物） |
| `go test ./... -cover -count=1` | 通过，覆盖率 71.2 / 87.7 / 54.9 / 93.9 / 74.1 / 90.6 / 100.0，与 `README.md:331-345` 表格**完全一致** |

### 4.2 S3（用 moto 模拟 S3 网关实测）

- `bash scripts/s3-moto-verify.sh` 通过：
  - `TestS3StartupFailsOnMissingBucket`、`TestMigrateToS3`、`TestMigrateToS3FailsOnMissingLocalBlob`（app 包）全 PASS；
  - `TestS3UploadDownload`（对象 key 布局、上传后 `files/` 为空、完整下载、RFC 2231 Content-Disposition、Range、HEAD、秒传、引用计数删除、对象丢失 404）、`TestS3MultipartUpload`（20 MiB 多段上传 + 跨 16 MiB 分片 Range）全 PASS。
- 另起 `S3_ENDPOINT=http://127.0.0.1:9033`（手工实例）/ `:9034`（脚本默认端口）、`S3_PREFIX=probe/s3run` 的实例（端口 18082）实测：启动日志 `blob_store=s3://file-share-test/probe/s3run`；`GET /api/storage` = `{"backend":"s3","bucket":"file-share-test","prefix":"probe/s3run"}`；上传后桶里 key = `probe/s3run/2d/8c/<sha256>`；下载内容与原文一致、Range 206、`/s/{id}?sig&exp` 24 小时档可匿名下载；`/readyz` 200。
- `migrate-blobs` 子命令实测（副本数据目录，`scripts/s3-moto-verify.sh` 第 2 段）：`--dry-run` 输出 `summary: would upload N, skipped 0` 且桶里无对象；正式迁移 `uploaded N, skipped 0, failed 0`；再跑一次 `uploaded 0, skipped N, failed 0`；本地 `files/` 仍保留；桶内对象与记录 sha 一一对应。`migrate-blobs` 在未配 `S3_*` 时退出码 1 并给出明确错误。

### 4.3 接口契约（审查专用实例 + 本地模式）

`bash scripts/api-probe.sh`：29/29 通过，覆盖 `/api/storage`（含未登录 401）、三级视图路由 200 / 四级 404 / `index.html` 直出不给、建三级文件夹、四级被拒、保留一级路径 `api`/`s` 等被拒、上传与秒传、`share?ttl=600|86400` 与 6 种非法 ttl 400、签名下载 GET/HEAD/Range/篡改 exp 404、文件与文件夹排序（含集合不符 409）、文件夹改名、删除文件夹（保留文件）、删除记录。

### 4.4 UI（无头 Chromium，Playwright，zh-CN 视口 1440×900 / 390×844）

- `python scripts/ui-smoke.py`：**34/34 PASS，控制台干净（CONSOLE_CLEAN）**。覆盖：未登录深链跳 `/auth?next=`、登录、树/表格/存储提示、三级面包屑与 3 级深链、空文件夹文案、上传后短指纹列与文件夹标签、详情面板完整 SHA、行内 10 分钟分享（实际 exp ≈ now+600）与「更多」里的 24 小时分享（exp ≈ now+86400）、复制 curl、移动到文件夹、统计信息弹窗、文件夹改名后地址同步、内容区文件夹行拖拽排序（SortableJS → `PUT /api/groups/order`，刷新后保持）、全局搜索（跨文件夹 + 行内文件夹标签 + 搜索期间隐藏拖拽把手）、移动端抽屉、未知文件夹回退「最近」、四级 404、`/index.html` 404。
- `python scripts/ui-reorder-download-delete.py`：**11/11 PASS**。文件行拖拽排序本地生效并发出 `PUT /api/files/order`、刷新后顺序保持；「全部下载」按可见顺序为 4 个文件各签一个 `/download-url` 并触发下载；三级文件夹视图与文件夹菜单都不再出现「新建子文件夹」；删除文件夹并勾选「同时删除这 N 个文件」后记录被删。
- `python scripts/ui-retry-duplicate.py`、`python scripts/ui-login-next-prefix.py`：分别复现 F2、F5（输出见对应 finding）。

### 4.5 并发正确性（正向验证，不是 finding）

`bash scripts/repro-invariant.sh`：40 个并发上传方 × 10 次同内容上传 + 8 个并发删除方（删 2/3 的记录），`-race` 下全部通过，结束时不变量「还有记录引用 ⇒ blob 必须存在」成立：

```text
invariant ok: 128 records kept, uploadErrs=0 deleteErrs=0
--- PASS: TestProbeUploadDeleteInvariant (1.30s)
```

即本次把 `App.mu` 全局锁拆成「DB 写锁 + 64 路 sha 分片锁」的核心改动在当前实现下没有观察到引用计数竞态（F1 是同一机制在非法输入下的兜底分支问题）。

### 4.6 未能验证 / 未覆盖的项

1. **真实 S3 网关兼容性**：只用 moto 模拟过 S3（协议层足够，但不是 MinIO/Ceph/R2/OSS 的真实实现）；真实网关上的 multipart、Range、签名区域、版本化/生命周期行为仍需按 README 的 `just test-s3` 在测试桶上跑一遍。
2. **多实例共用一个桶 + 不同 `S3_PREFIX`** 的引用计数场景（DESIGN §9 的已知取舍），未部署验证。
3. **k8s / Podman 部署**：readOnlyRootFilesystem、hostNetwork、Quadlet 模板、arm64 二进制运行均未验证。
4. **超大文件链路**：只验证到 20 MiB 多段上传；2 GiB 上限、5 GiB 以上分片边界、以及本地磁盘满时上传错误路径未压测。
5. **真实浏览器**（Safari/Firefox/iOS）的多文件下载拦截、`navigator.clipboard` 回退到 `execCommand`、触屏长按 Sortable 拖拽未验证（只用了 Chromium）。
6. **大数据量前端性能**（见待确认 3）与 `migrate-blobs` 在数十 GB 库上的耗时/中断恢复未实测。
7. S3 集成测试默认 skip，`just test-s3` 在无 `S3_TEST_*` 时不会真正执行（本次用 moto 手工补上，CI 默认仍不跑）。

## 5. 结论

本次改动（S3/BlobStore 后端 + 存储提示接口 + 文件夹树/表格 UI 全量重写 + 文件夹行拖拽排序 + 短指纹列 + 分享链接 24 小时档 + 视图路由放宽到三级）在功能与文档契约上整体自洽：**未发现 P0/P1**；除 F1、F2 两个 P2 外，其余 8 项为 P3（语义/文案/死代码/依赖与检查脚本卫生）。S3 后端、`migrate-blobs`、24 小时分享链接、三级目录路由、文件夹/文件拖拽排序均已用独立实例或 moto 实际跑通；F1、F2、F5 有可重复的复现脚本，F3、F4、F6-F10 均可从代码/命令输出直接证实。