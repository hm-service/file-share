# Cross review：file-share

- **被审查提交**：`279dc0e5e91096d9114f8ac180e1e63473590964`（分支 `main`，短 SHA `279dc0`）
- **审查开始时间**：2026-11-22T00:32:17+08:00（Asia/Shanghai）
- **审查者**：cross-reviewer（本文件即正式审查记录；修复由 fixer 按本文逐项处理并写 `RESPONSE.md`）
- **审查范围**：
  - **A 部分（上一轮改动）**：`git log --oneline cf242e9..279dc0e` 的 10 个提交，逐条确认上一轮 F1–F10 的修复是否真的成立、有没有引入新问题或缺口：
    `5cf8a2e`（S3 后端/文件夹树 UI/24 小时分享链接，并声称修复 F1–F10）、`839d924`（侧栏拖拽调宽）、
    `a2370b4`（存储状态实时探测/503）、`6eb1443`（树默认只展开到第二级）、`23a4e8c`（文件夹与文件分界线/侧栏宽度只存内存）、
    `255a431`（DATA_DIR 布局拆分）、`b4d185d`（用户表与读写权限）、`fc834a3`（用户表默认路径 + K8s）、
    `db78049`（用户信息移到顶栏、去掉 USERS_FILE）、`279dc0e`（AGENTS/文档同步、405 修正、DATA_DIR 先报目录错误、删 i18n.F、重拍截图）。
    重点核对 `b4d185d`、`fc834a3`、`db78049`、`279dc0e` 这几个认证/权限/文档同步提交。
  - **B 部分（全项目）**：Go 后端 `main.go web.go api/ app/ core/ db/ httpx/ i18n/ internal/testenv`、前端 `frontend/src/**`、
    构建脚本（`justfile` / `Dockerfile` / `.github/workflows`）、部署清单（`k8s.file-share.yaml`）。
    契约依据是 `DESIGN.md` 与 `README.md`；`AGENTS.md` 明列的「与通用规范的有意差异」不作为问题上报。
- **审查方式**：静态阅读 + 构建/测试（含 `-race`、`-cover`、gofmt、`go mod tidy -diff`）+ 独立实例上的接口探针 +
  无头 Chromium（Playwright 1.65.2 / chrome-headless-shell）UI 探针 + 用本仓库真实旧数据（`.data`）做的升级复现。
  所有复现/探针脚本在 `scripts/` 下；**未修改任何生产、测试、文档文件，未提交 git**。

## 1. 工作区状态

### 1.1 审查开始时的 `git status`

```text
（空——工作区干净）
```

`git rev-parse HEAD` = `279dc0e5e91096d9114f8ac180e1e63473590964`，`git branch --show-current` = `main`。

### 1.2 审查期间的工作区变化

- 仅新增本目录 `code-reviews/2026-11-22T00-32-17+0800_279dc0/`（审查产物，未跟踪）。
- 为跑 UI 探针执行了 `pnpm build` / `just build`，重写了 gitignore 的 `frontend/dist/` 与 `.dist/` 构建产物（不属于被审查内容）。
- 探针实例全部监听 127.0.0.1 的 18095–18105 临时端口，`DATA_DIR` 均为 `mktemp -d` 临时目录，结束时已停止进程并删除；
  未触碰 `.data/`、`.scratch/` 与真实服务。用 `.data` 做的升级复现是 `cp -a` 到临时目录后进行的。

## 2. 上一轮修复复核（cf242e9 → 279dc0e）

| 上一轮 | 结论 | 依据 |
| --- | --- | --- |
| F1 `shaMutex` 自死锁 | **已修复** | `app/app.go:356-377` 任何 sha 都落到 64 分片之一，不再退回 `a.mu`；`app/blobstore.go:19-24` 的 `validBlobSHA` 在本地/S3 两个后端的 `Head/PutFile/Open/Delete` 入口统一校验；`app/files.go:198-219` 的 `removeBlobIfUnreferenced` 用 `defer` 解锁。回归测试 `app/app_test.go:930`（`TestDeleteFileWithMalformedSHA`，3 秒超时断言）与 `:957`（`TestBlobStoreRejectsMalformedSHA`）通过 |
| F2 上传后 PATCH 失败→重试重复记录 | **已修复** | `frontend/src/components/UploadDock.vue:164-181` 的 `fileId` 分支：重试只补 PATCH 不重新上传。运行时探针 `scripts/run-ui-upload-retry.sh` **5/5 PASS**：同名记录 1 条、落在目标文件夹、PATCH 调用 2 次 |
| F3 列头「修改时间」 | **已修复** | `frontend/src/i18n.ts:299` `column.uploaded`「上传时间/Uploaded」；`frontend/src/components/ContentTable.vue:182`；`DESIGN.md:139`、`README.md:26` 同步 |
| F4 缺 `store.order` 文案 | **已修复** | `i18n/i18n.go:53` 已有该键；脚本核对后端 35 个文案键与全部 `Fail`/`fail`/`storeFailure` 引用一一对应 |
| F5 `?next=` 前缀过宽 | **部分修复** | `frontend/src/LoginPage.vue:22-25` 的 `isAuthPage` 已能正确处理 `/author`；但同一段「只接受站内路径」的校验仍可被反斜杠绕过 → **本轮新发现 F2（P2）** |
| F6 `just check` 不跑 gofmt | **已修复** | `justfile:40-54`：`check: fmt-check vet test` + `fmt-check` 用 `git ls-files --cached --others` 覆盖新文件；全仓库 `gofmt -l` 无输出；`README.md:91` 与 `:347` 表述一致 |
| F7 `go.mod` 未 tidy | **已修复** | `go mod tidy -diff` 无输出（0 字节）；`minio-go` 已在直接依赖块 |
| F8 死代码 / 失效注释 / 未用文案键 | **已修复** | `formatDayHeading`、`date.group-heading`、`ReservedGroupRoot` 全仓库无引用（`.scratch` 里的历史备份除外）；`routes.ts:7`、`session.ts:57` 注释已同步；前端文案表 118 键与 `t("…")` 静态引用集合完全一致，无未用键/缺失键 |
| F9 README 保留路径漏 `s` | **已修复** | `README.md:215` 已含 `s`，与 `core/validate.go:69-79` 集合一致 |
| F10 拖拽状态三份/比较器非全序 | **已修复** | `frontend/src/dnd.ts:7` 单一 `hasFiles`（App/ContentTable/DirTree 三处 import 同一实现）；拖拽状态收敛为 App 的 `draggingFileId`（`App.vue:806`，ContentTable 只收 prop）；`App.vue:491-505` 的 `applyGroupOrder` 先筛同父级再组内排序，比较器已是全序 |

上一轮的 6 条「待确认项」在本轮 HEAD 上仍维持 RESPONSE 里的结论（S3 上传信号量不响应取消、空 `names` 的 204、
前端 O(分组数×文件数)、`--help` 退出码等已随 `migrate-blobs` 删除而消失、旧 `?section=` 被忽略、`S3_ENDPOINT` 尾斜杠）。
本轮**没有**找到推翻这些结论的新证据。

## 3. 发现

严重度：P0 数据丢失/安全/不可恢复；P1 正常使用功能错误；P2 特定输入、边界或时序错误；P3 健壮性/可维护性/体验。
每条都附相对路径与行号；除静态可证外均给出可复现命令。

### [P1][F1] 旧 `DATA_DIR/files` 布局的迁移逻辑被删除：旧部署升级后所有已有文件下载 404，且全程无提示、文档未提迁移

**位置**：

- 当前实现：[`app/app.go:88-163`](app/app.go#L88-L163)（`Open` 只建 `blobs/` 与 `temp/`）、[`app/app.go:165-199`](app/app.go#L165-L199)
  （`prepareDataDir` 只检测旧 Python 的 `files.db` / `favorite.json`，不检测 `files/` 目录）、[`app/app.go:37-42`](app/app.go#L37-L42)（`blobsDirName = "blobs"`）。
- 迁移逻辑在 `255a431` 里加过（`moveLegacyBlobDir`，`DATA_DIR/files` → `DATA_DIR/blobs` 启动一次性改名，两目录并存时拒绝启动），
  又在 `b4d185d` 里整段删除（提交信息：「去掉 DATA_DIR/files → blobs 的旧布局兼容逻辑（不做反向兼容）」）。
- 契约：[`DESIGN.md:364`](DESIGN.md#L364)（§9「结构变更只在启动时一次性迁移」）、[`DESIGN.md:77-90`](DESIGN.md#L77-L90)（数据目录只有 `blobs/`）、
  [`README.md:299-322`](README.md#L299-L322)（「数据与升级」只讲了 SQLite v1→v6 迁移与旧 Python 数据，没有任何 `files/` → `blobs/` 的说明）。

**触发条件**：任何从 `cf242e9` 及更早版本（`31de134` 起一直用 `DATA_DIR/files/<sha256>`，本机 `.data/` 就是这版布局）升级到本提交的部署。

**实际结果**：启动成功、无任何告警；`/api/files` 仍列出全部记录；**每条记录的下载与签下载地址都 404**；
`/api/storage` 反而报 `{"backend":"local","healthy":true}`（它只检查新建的空 `blobs/` 目录）。
既有 blob 还完整躺在 `DATA_DIR/files/` 里，手工 `mv files blobs` 后即可恢复（不丢数据，但对用户表现为「文件全丢了」）。

**复现 1（合成场景）**：`bash scripts/repro-blobs-layout.sh`：

```text
== 1. 全新部署：上传一份文件 ==
  blobs/<sha> 存在: yes
  升级前下载状态: 200
== 2. 模拟旧部署：blobs/ 改回旧布局 files/，重启 ==
  /api/files 记录数: 1
  升级后下载状态: 404
  升级后 download-url 状态: 404
  升级后 /api/storage: {"backend":"local","healthy":true}
  启动日志里 legacy / files/ 提示: 0
  旧路径文件还在磁盘: yes；新目录: 0 个文件
RESULT: 复现成功
```

**复现 2（本仓库真实旧数据）**：把 `.data/`（`user_version=5`、8 条记录、8 个 blob 在 `files/`）复制到临时目录，
补一份 `users.yaml` 后用 HEAD 二进制启动：记录数 8，下载状态分布 `{'404': 8}`，`/api/storage` 为 `healthy:true`；
把 `files/` 改名为 `blobs/` 后重启，下载状态分布 `{'200': 8}`。

**修复建议**（三选一，建议 1+3）：

1. 恢复 `255a431` 的 `moveLegacyBlobDir`：启动时把 `DATA_DIR/files` 一次性改名为 `blobs`（同文件系统内原子且零拷贝），
   两个目录并存时拒绝启动并提示先确认哪份才是真数据；
2. 或者在 `prepareDataDir` 里「检测到 `files/` 非空且 `blobs/` 不存在/为空 → fail-fast」，错误里直接给出
   `mv DATA_DIR/files DATA_DIR/blobs`，至少不静默；
3. `README.md`「数据与升级」补一句旧布局的迁移命令，并说明 `blobs/` 为空而 `files/` 非空时的现象。
   如果确实决定「不做反向兼容」，也应在文档里明确写下这一步，否则运维没有任何线索。

### [P2][F2] 登录 `?next=` 的站内限制仍可被反斜杠绕过：`/\evil.example` 登录后把用户带到外站（开放重定向）

**位置**：[`frontend/src/LoginPage.vue:27-32`](frontend/src/LoginPage.vue#L27-L32)（`afterLoginURL` 只用
`startsWith("/")` / `startsWith("//")` / `isAuthPage` 做字符串前缀判断）、[`frontend/src/LoginPage.vue:46`](frontend/src/LoginPage.vue#L46)
（`location.href = afterLoginURL()`）。该函数是 `5cf8a2e` 修上一轮 F5 时改的，注释明写「只接受站内路径，避免被 `?next=` 带到别的站点」。

**触发条件**：访问 `/auth?next=%2F%5Cevil.example`（即 `next` 为 `/\evil.example`）并正常登录。
按 WHATWG URL 规则，特殊 scheme 下反斜杠等价于 `/`，所以 `/\evil.example` 会被解析成 `//evil.example`。

**实际结果**：校验通过（以 `/` 开头、且不以 `//` 开头），`location.href` 被设置成外站地址，浏览器离开本站。
攻击者可以用「登录后被跳到仿冒站点」做钓鱼；会话 token 存在 localStorage、不随跳转泄露，因此影响有限，但这是可构造链接直接利用的安全问题。

**复现**：`bash scripts/repro-login-open-redirect.sh`：

```text
PASS 合法 next 回到站内 :: http://127.0.0.1:18100/ungrouped
PASS 浏览器把 /\\evil.example 解析成外站 :: http://evil.example/
PASS 登录后跳到了外站（应当被拦成 /） :: chrome-error://chromewebdata/
RESULT: 复现成功：?next= 可把登录后的用户带到外站
```

（`chrome-error://` 是浏览器已按 `http://evil.example/` 发起导航、但本机无外网导致的错误页。）

**修复建议**：不要用字符串前缀判断，改成按 origin 解析后再判断，例如：

```ts
function afterLoginURL(): string {
  const next = new URLSearchParams(location.search).get("next") ?? "";
  let url: URL;
  try {
    url = new URL(next, location.origin);
  } catch {
    return "/";
  }
  if (url.origin !== location.origin || isAuthPage(url.pathname + url.search)) return "/";
  return url.pathname + url.search;
}
```

### [P3][F3] `DATA_DIR` 存在但不是目录时，启动错误文案是 `%!w(<nil>)`；且这处检查与 `app.Open` 的检查重复

**位置**：[`main.go:203-208`](main.go#L203-L208)。

**触发条件**：`DATA_DIR` 指向一个已存在的普通文件（而不是目录），例如容器里把 ConfigMap/Secret 文件直接挂成了 `DATA_DIR`。

**实际结果**：

```text
$ DATA_DIR=/tmp/.../afile ... ./app
init failed  error="DATA_DIR \"/tmp/.../afile\" must be an existing directory: %!w(<nil>)"
```

`os.Stat` 成功、`info.IsDir()` 为 false 时 `err` 是 nil，`fmt.Errorf("...: %w", err)` 把 nil 包进 `%w`，得到 `%!w(<nil>)`。
（同一次启动里 `app.Open` → `prepareDataDir` 其实会给出正确的 "DATA_DIR … is not a directory"，但 `loadConfig` 先失败，运维只看到坏文案。）

**复现**：`bash scripts/probe-api-semantics.sh` 的 A 段（输出见上）。

**修复建议**：把「stat 失败」与「不是目录」分开处理，例如：

```go
info, err := os.Stat(dataDir)
if err != nil {
    return zero, limits, fmt.Errorf("DATA_DIR %q is not usable: %w", dataDir, err)
}
if !info.IsDir() {
    return zero, limits, fmt.Errorf("DATA_DIR %q is not a directory", dataDir)
}
```

顺带说明：这处检查只是「让目录错误先于 `users.yaml` 读取错误出现」，`app.Open`/`prepareDataDir` 里已有同一不变量的完整校验；
两处校验并存没问题，但建议把「存在且是目录」的判断抽成 `app` 里可复用的小函数，避免文案/语义再分叉。

### [P3][F4] `write`-only 账号是 DESIGN 允许的合法配置，但在界面上完全不可用（`/api/me` 要求 `read`，403 后前端按只读处理）

**位置**：[`app/users.go:142-155`](app/users.go#L142-L155)（只校验「至少一项」权限，允许 `permissions: [write]`）、
[`api/router.go:50`](api/router.go#L50)（`GET /api/me` 被要求 `app.PermissionRead`）、
[`api/router.go:199-224`](api/router.go#L199-L224)（权限闸门）、
[`frontend/src/App.vue:75`](frontend/src/App.vue#L75)（`canWrite` 依赖 `/api/me`）、[`frontend/src/App.vue:367-374`](frontend/src/App.vue#L367-L374)（`loadMe` 失败即按只读处理）。
契约：[`DESIGN.md:69-73`](DESIGN.md#L69-L73)「权限只允许 `read` / `write`，至少一项」。

**触发条件**：`DATA_DIR/users.yaml` 里配置一个只有 `write` 的用户（`permissions: [write]`），启动并登录。

**实际结果**（`bash scripts/probe-api-semantics.sh` B 段）：

```text
writer GET /api/me:     403  ← 拿不到自己的权限
writer GET /api/files:  403
```

前端在 `/api/me` 403 后把 `canWrite` 判成 false，于是上传、新建文件夹等所有写入口都被隐藏；`/api/files` 也 403，
列表页显示加载失败——`write`-only 用户既不能用界面写、也不能读，只能拿 token 直接 curl 上传接口。合法配置产生不可用状态。

**修复建议**（任选其一，建议 1）：

1. 启动加载用户表时拒绝「有 `write` 但没有 `read`」的配置（`让非法状态不可表示`），并在 DESIGN §3.1 写明 `write` 隐含需要 `read`；
2. 或让 `/api/me` 只要求「有效会话」而不是 `read`，并明确 write-only 账号的界面行为（但它仍然看不到列表，界面依旧不完整）。

### [P3][F5] README 的覆盖率表已过期：4 个包的数字与当前实测不符

**位置**：[`README.md:350-364`](README.md#L350-L364)。

**触发条件**：执行文档里写的命令 `go test ./... -cover -count=1`。

**实际结果**（2026-11-22，Go 1.27.4）：

| 包 | README 写 | 实测 |
| --- | --- | --- |
| `main`（仓库根） | 71.2% | **77.4%** |
| `api` | 87.7% | **88.5%** |
| `app` | 55.1% | **58.5%** |
| `core` | 95.0% | **94.8%** |
| `db` | 74.1% | 74.1% |
| `httpx` | 90.6% | 90.6% |
| `i18n` | 100.0% | 100.0% |

上一轮 RESPONSE 只同步了当时的数字；本轮 `b4d185d`/`279dc0e` 等提交又加了测试（`app/app_test.go`、`main_test.go`、
`api/api_test.go`、`api/storage_test.go` 等），表格没再更新。`DESIGN.md:353` 要求「覆盖率记入 README」，`AGENTS.md` 要求
「行为变更要同步 `DESIGN.md` 与 `README.md`」。

**修复建议**：用当前实测数字重写表格（连同「实测时间」一栏），或在文档里去掉具体百分比、只保留命令。

### [P3][F6] 文件夹树「首次打开默认只展开到第二级」这个行为没写进 DESIGN/README

**位置**：[`frontend/src/App.vue:262-271`](frontend/src/App.vue#L262-L271)（`applyDefaultFolded`：没有 localStorage 记录时，
把所有二级文件夹放进折叠集合，即首次打开只展开到二级）；契约描述在 [`DESIGN.md:136-138`](DESIGN.md#L136-L138)、
[`README.md:26`](README.md#L26)，两处都只说「展开 / 折叠状态存 localStorage」，没有默认展开级别。

**触发条件**：浏览器 `localStorage` 里没有 `file-share.folded-folders` 时首次打开。

**实际结果**：实测首次打开树里只出现一级与二级文件夹，三级文件夹被折叠隐藏（UI 探针
`scripts/run-ui-probe.sh` 的「树默认展开到第二级（孙1 折叠）」PASS，标签为 `['组A', '子1']`）。
行为本身合理，但 `6eb1443` 的这次行为变更没有同步契约文档，与 `AGENTS.md`「行为变更要同步 `DESIGN.md` 与 `README.md`」不符。

**修复建议**：`DESIGN.md` §5 文件夹树一节补一句「首次打开默认展开到第二级（二、三级默认折叠），用户的展开 / 折叠选择存 localStorage」。

## 4. 待确认项（未能定性为缺陷，需产品/运维确认）

1. **慢请求体没有读超时**：`main.go:89-92` 的 `http.Server` 只设了 `ReadHeaderTimeout`，没有 body 读超时。
   认证闸门 32 槽、业务 API 128 槽，攻击者可以各开若干连接慢慢发 body 长期占住槽位（大文件上传本身又确实需要长 body 时间，
   不能简单加全局 `ReadTimeout`）。DESIGN §7 把限流/访问控制交给反向代理，是否需要应用侧再兜一层（例如按 handler 设读空闲超时）待确认。
2. **`App.Blobs` 是导出可变字段，外部测试包直接替换实现**（`api/storage_test.go:50`、`:73` 用 `instance.Blobs = failingBlobs{…}` 模拟存储故障）。
   生产代码里没有别处给它赋值。若要严格遵循「不为测试向生产设计加东西」，可在 `app` 包内测或做构造注入；当前不影响行为。
3. **`/api/groups/delete` 不在未鉴权路由清单的自动化测试里**：`api/api_test.go` 的 `TestAuthRequiredRoutes` 列了 15 条受保护路由，
   漏了 `POST /api/groups/delete`（只读权限矩阵里有它）。行为本身正确（实测 401/403 都符合预期），只是缺一条回归断言。
4. **`just push` 只推默认远端**：`justfile:119-129` 的 `push` recipe 结尾是 `git push`，而 `AGENTS.md::§2` 要求
   「`git push origin main` 与 `git push local main` 都推」。用 `just push` 提交时 `local` 远端不会更新；是否要改成两次 push 待确认（也可能是有意只推 origin）。
5. **默认树展开级别之外的其它 UI 默认值**（侧栏宽度默认 248px、上传队列 REMOVE_DELAY 1.2s）同样只在代码里，文档没写；如果认为这些不值得写进
   DESIGN，可忽略本项与 F6。
6. **`USERS_FILE` 相关语义已彻底移除**：`fc834a3` 曾经支持过这个环境变量，`db78049` 又去掉；HEAD 上代码/文档/K8s 清单已一致
   （全仓库仅 `AGENTS.md` 有一句「不提供 USERS_FILE」的说明）。如果有一段发布期里用户配过 `USERS_FILE`，升级后它会被静默忽略，
   但用户表路径固定后不会读错文件（会直接因缺 `DATA_DIR/users.yaml` 而启动失败），因此只作为升级提示待确认。

## 5. 构建、测试与验证记录

### 5.1 静态检查与构建（全部通过）

| 命令 | 结果 |
| --- | --- |
| `go vet ./...` | 通过（无输出，exit 0） |
| `git ls-files --cached --others --exclude-standard -- '*.go' \| xargs gofmt -l` | 无输出（全部 gofmt-clean） |
| `go mod tidy -diff` | 无 diff（0 字节输出，exit 0） |
| `go test ./... -count=1` | 通过：`file-share` 0.63s / `api` 3.69s / `app` 5.45s / `core` 0.06s / `db` 0.06s / `httpx` 0.01s / `i18n` 0.003s |
| `CGO_ENABLED=1 go test -race ./... -count=1` | 通过（7 个包全 ok，无 data race 报告） |
| `go test ./... -cover -count=1` | 通过，覆盖率见 F5 表格 |
| `just check` | 通过：`go vet` + `go test` + `pnpm install --frozen-lockfile` + `pnpm typecheck`（`vue-tsc -b`） |
| `just build` | 通过：前端 vite build + `linux/amd64`、`linux/arm64` 两个静态二进制 |
| `cd frontend && pnpm build` | 通过（产物写回 gitignore 的 `frontend/dist`） |

### 5.2 接口与行为探针（独立实例，127.0.0.1 临时端口 + 临时 `DATA_DIR`）

- `bash scripts/probe-api-semantics.sh`：
  - A. `DATA_DIR` 指向普通文件 → 启动错误 `DATA_DIR "…" must be an existing directory: %!w(<nil>)`（F3）；
  - B. 权限矩阵：admin/reader/writer 登录正常、错误密码 401；reader 对 `/api/files`、`/api/me`、下载、签分享均 200，
    对上传 / PATCH / DELETE / 建删改名文件夹 / 排序均 403；writer 对 `/api/me`、`/api/files` 均 403（F4）；
  - C. 把 `DATA_DIR/blobs` 移走：`/api/storage` → `{"healthy":false,"error":"存储服务暂时不可用，请稍后重试"}`，
    `download-url` / `upload` / `check_file` 全部 503；目录放回后 `/api/storage` 恢复 `healthy:true`、签地址恢复 200；
  - D. 从 `users.yaml` 删掉 reader 并重启：reader 旧 token → 401，admin 旧 token → 200（DESIGN §3.1「删用户即吊销会话」成立）。
- 手工核对 405/Allow：`GET`/`POST`/`PATCH`/`DELETE`/`HEAD`/`OPTIONS /api/files/order` 全部 405 且 `Allow: PUT`
  （`279dc0e` 的修复成立，单元测试 `api/api_test.go` 也覆盖了四种 + 字面量路径不污染通配符 `Allow`）。
- 手工核对签名下载：`/s/{id}?sig&exp` 无凭据 200、`Range: bytes=0-3` 206、HEAD 200、篡改 sig / `exp=1` / 换 id 均 404；
  `share?ttl=86400` 的 exp≈now+86400s，`ttl=601` 400。

### 5.3 UI（无头 Chromium，Playwright 1.65.2，1440×900 / 390×844，zh-CN）

- `bash scripts/run-ui-probe.sh`：**22/22 PASS，控制台零错误（CONSOLE_CLEAN）**。覆盖：
  写账号顶栏有上传 / 新建文件夹、用户胶囊显示名字无徽标；只读账号顶栏无上传 / 无新建、胶囊挂「只读」徽标、
  侧栏无新建入口、文件夹行无拖拽把手、文件行无收藏 / 移动按钮、更多菜单只剩分享链接 / 24h 分享 / curl（无重命名 / 删除）；
  树默认只展开到第二级；侧栏拖拽 248→720px（不超过半屏）且刷新回 248；存储后端不可达时侧栏红点 + 「本地磁盘（无法连接）」。
- `bash scripts/run-ui-upload-retry.sh`：**5/5 PASS**（上一轮 F2 的运行时复核，详见 §2）。
- `bash scripts/repro-login-open-redirect.sh`：复现 F2。

### 5.4 用真实旧数据做的升级复现（F1）

把 `.data/`（8 条记录、`files/` 布局、`user_version=5`）复制到临时目录 + 补 `users.yaml`，用 HEAD 二进制启动：
列表 8 条、下载 `{'404': 8}`、`/api/storage` `healthy:true`、启动日志无 legacy 提示；`mv files blobs` 后重启 → `{'200': 8}`。

## 6. 未验证 / 未覆盖的项

1. **真实 S3 网关**：本轮没有配置 `S3_TEST_*`，`just test-s3` 未跑；S3 相关只做了代码阅读。真实 MinIO/Ceph/R2/OSS 上的
   multipart、Range、签名区域、`HeadBucket` 探测行为与上一轮 moto 结论一致与否未复测。
2. **k8s / Podman 实际部署**：`k8s.file-share.yaml` 的 Secret `users` 以 `subPath` 挂成 `/data/users.yaml`、`readOnlyRootFilesystem`、
   arm64 镜像启动均未在集群/Quadlet 上验证（只验证了 `just build` 能产出两个架构的二进制）。
3. **真实浏览器**：只用了 Chromium（chrome-headless-shell）；Safari/Firefox/iOS 的多文件下载拦截、
   `navigator.clipboard` 失败回退、触屏拖拽未验证。
4. **超大文件链路**：没有压测 2GiB 上限、5GiB 分段、磁盘满时上传错误路径；S3 上传信号量（4）在 4 路以上并发大文件时的排队行为未实测。
5. **多实例共桶 + 不同 `S3_PREFIX`** 的引用计数场景未部署验证（DESIGN §9 已知取舍）。
6. **`docs/screenshot-*.png` 与当前界面的逐像素一致性**：本轮只核对了尺寸（1440×900 / 390×844）与内容（登录页用户名+密码、
   顶栏用户胶囊、侧栏存储提示、移动端表格只留名称与操作列），没有重拍对比。
7. **上一轮 F5 的 `/author` 场景**：本轮用代码核对 `isAuthPage` 逻辑，没有单独跑 UI 复现（其正确性可从 `LoginPage.vue:22-25` 直接证明，
   且 F2 的复现脚本里「合法 next 回到站内」一项覆盖了正常路径）。
8. `migrate-blobs` 子命令已被删除（`b4d185d`），因此上一轮关于它的待确认项不再适用；DESIGN §9「本地目录 → S3 手工迁移」
   的说明仍保留，但本轮没有实测这条运维路径。

## 7. 结论

上一轮 F1–F10 的修复在当前 HEAD 上**基本全部成立**（F1、F2 有运行时复核；F3/F4/F6/F7/F8/F9/F10 代码与命令可证；
F5 的主诉已修但同一段校验留下了 F2 这个开放重定向缺口）。本轮 A 部分 10 个提交里，
`b4d185d` 删除旧 blob 目录迁移、`255a431` 之后的 `DATA_DIR` 布局切换是唯一会影响已有部署的改动，且没有文档兜底 → **1 个 P1（F1）**；
`5cf8a2e` 里 `?next=` 的站内校验仍可绕过 → **1 个 P2（F2）**；另有 **4 个 P3**（`DATA_DIR` 错误文案、write-only 账号、
README 覆盖率表过期、树默认展开级别未进文档）。没有发现 P0。
S3/对象存储真实网关、k8s 部署、超大文件与真实浏览器行为仍未覆盖（见 §6）。