# 审查响应：2026-11-22T00-32-17+0800_279dc0

- 对应审查记录：本目录 `REVIEW.md`（被审查提交 `279dc0e5e91096d9114f8ac180e1e63473590964`）
- 响应时间：2026-11-22T00:51:24+0800
- 角色：修复者（fixer）

## 逐项响应

### F1（P1）旧 `DATA_DIR/files` 布局升级后下载全 404 —— 部分采纳：不恢复自动迁移

- 决定：自动改名与「两目录并存拒绝启动」都属于反向兼容，用户已明确要求删除（`b4d185d`
  的改动依据），因此不在启动逻辑里恢复兼容分支。
- 做法：
  - `app/app.go` 新增 `warnLegacyBlobDir`：`blobs/` 为空而 `files/` 非空时打一条 WARN，
    带 `mv files blobs` 提示；不改名、不拒绝启动。
  - `README.md`「数据与升级」补旧布局的迁移步骤与现象；`DESIGN.md` §4、§9 同步。
- 验证：重跑 `scripts/repro-blobs-layout.sh`，输出「启动日志里 legacy / files/ 提示: 1」；
  手工 `mv files blobs` 后下载恢复 200（即审查记录 §5.4 的恢复路径）。
- 回归测试：`app/app_test.go` 新增 `TestOpenWarnsLegacyBlobDir`（旧布局有日志、`blobs/` 非空时不再提示）。

### F2（P2）登录 `?next=` 开放重定向 —— 已修复

- 做法：`frontend/src/LoginPage.vue` 的 `afterLoginURL` 改为 `new URL(next, location.origin)`
  解析后比较 `origin`，并继续拦登录页自身；只返回 `pathname + search`。
- 验证：重跑 `scripts/repro-login-open-redirect.sh`：合法 `next` 仍回 `/ungrouped`；
  `/\evil.example` 登录后停在 `http://127.0.0.1:18100/`（原「跳到外站」断言变成 FAIL）。
- 回归：前端没有单测框架，行为由上述浏览器脚本覆盖。

### F3（P3）`DATA_DIR` 不是目录时错误文案 `%!w(<nil>)` —— 已修复

- 做法：`main.go` 把 stat 失败与非目录分开：`DATA_DIR %q is not usable: %w` /
  `DATA_DIR %q is not a directory`。
- 验证：`scripts/probe-api-semantics.sh` A 段现在输出 `DATA_DIR "…" is not a directory`。
- 说明：这处前置校验用于让目录错误先于 `users.yaml` 读取错误出现；完整不变量仍由
  `app.Open` 校验（`AGENTS.md` 已记录 `main.go` 集中做环境变量校验的取舍，重复保留）。

### F4（P3）`write`-only 账号不可用 —— 已修复（拒绝非法状态）

- 做法：`app/users.go` 的 `parsePermissions` 拒绝「有 `write` 但没有 `read`」的配置，
  错误为 `write requires read; add read to permissions`；`DESIGN.md` §3.1、`README.md`
  用户表、`AGENTS.md` 写明 `write` 必须同时带 `read`。
- 验证：`permissions: [write]` 的用户表启动即失败并给出上述错误；只读账号矩阵
  `/api/me` 200、`/api/files` 200、上传 / 建文件夹 / PATCH 均 403。
- 回归测试：`main_test.go` 的 `TestLoadConfig` 新增「用户表里只有 write」用例。

### F5（P3）README 覆盖率表过期 —— 已修复

更新为 2026-11-22 实测（`go test ./... -cover -count=1`）：
`main` 77.3% / `api` 88.5% / `app` 59.6% / `core` 94.8% / `db` 74.1% /
`httpx` 90.6% / `i18n` 100.0%。

### F6（P3）树默认展开级别未进文档 —— 已修复

`DESIGN.md` §5 与 `README.md` 功能段补「首次打开默认展开到第二级（更深的默认折叠）」。

## 待确认项

1. 慢请求体读超时：不修。`DESIGN.md` §9 新增已知取舍：慢连接交给反向代理的限流 / 超时兜底，
   大文件上传本身需要长 body。
2. `App.Blobs` 导出可替换：保留。仅测试用它模拟后端故障，生产没有赋值；改成构造注入会为测试改生产设计。
3. `/api/groups/delete` 不在未鉴权路由清单：已补进 `TestAuthRequiredRoutes`。
4. `just push` 只推默认远端：`justfile` 改为 `git push origin` + `git push local`。
5. 其它 UI 默认值：不写入文档（侧栏宽度、上传队列延迟属实现细节；有行为含义的树默认展开级别已由 F6 覆盖）。
6. `USERS_FILE` 升级提示：`README.md`「数据与升级」补一句——早期版本用过的 `USERS_FILE` 已移除，配了也会被忽略。

## 构建与测试

| 命令 | 结果 |
| --- | --- |
| `go vet ./...` | 通过 |
| `go test ./... -count=1` | 7 个包全部 ok |
| `go test ./... -cover -count=1` | 覆盖率见 F5 |
| `just check` | 通过（gofmt / vet / test + `pnpm typecheck`） |
| `just build` | 通过（linux/amd64、linux/arm64 静态二进制） |
| `scripts/repro-blobs-layout.sh` | 旧布局启动警告出现（F1 修复生效） |
| `scripts/repro-login-open-redirect.sh` | 外站跳转被拦截（F2 修复生效） |
| `scripts/run-ui-probe.sh` | 22/22 PASS，控制台无错误 |

测试数量变化：`app` 新增 1 个测试；`TestLoadConfig` 新增 2 个子用例；`TestAuthRequiredRoutes` 新增 1 条路由。
未自动验证：F2 没有前端单测框架，仅用浏览器脚本验证；`probe-api-semantics.sh` 的 B 段按旧语义构造
write-only 账号，修复后会启动失败，属预期（已用独立脚本复核只读矩阵与 write-only 拒绝）。

## 变更文件

- `app/app.go`、`app/app_test.go`、`app/users.go`
- `main.go`、`main_test.go`
- `api/api_test.go`
- `frontend/src/LoginPage.vue`
- `README.md`、`DESIGN.md`、`AGENTS.md`、`justfile`
- `code-reviews/2026-11-22T00-32-17+0800_279dc0/`（REVIEW.md、RESPONSE.md、scripts/）