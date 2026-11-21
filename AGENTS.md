# AGENTS.md

本文件是 file-share 的项目级约定。与通用规范冲突时，优先级为 `DESIGN.md` > 本文件 > 通用技能规范
（`create-lite-go-web-app`）；通用规范里没被 `DESIGN.md` 覆盖的部分仍然有效。

## 1. 与通用技能规范的有意差异

以下取舍已写进 `DESIGN.md`，是本项目的既定设计，不要按通用规范「纠正」：

- 认证是 YAML 用户表 + 明文密码 + SHA-256 恒时比较；会话凭据是 v2 `<exp>|<nonce>|<username>`。
  不用 argon2，也不用 `uuid|exp|nonce` 的 v1 格式。
- 用户表固定读 `DATA_DIR/users.yaml`，不提供 `USERS_FILE` 这类路径覆盖。
- 签名 URL 只有 10 分钟与 24 小时两档，不做时间取整。
- `TZ` 默认空（Go 默认时区）；`READYZ_TOKEN` 可空；`SESSION_SIGNING_KEY` / `URL_SIGNATURE_KEY` 必填。
- 数据库用 TEXT 主键、表名 `file` / `groups`，`PRAGMA user_version` 逐级迁移并保留旧库兼容。
- 请求体与 JSON 字段用 snake_case（`group_path`、`new_name`、`delete_files`、`sort_order`）。
- 前端用 SortableJS 实现拖拽排序（`DESIGN.md` 明确要求）。
- `main.go` 集中做环境变量解析与校验（`loadConfig` 一族），不拆 `config.go`：单入口接线，
  校验顺序与启动顺序同屏可见。
- 回归用例放在各自包的 `*_test.go` 里、贴近被测代码，不单独建 `regress_test.go`。
- 用户表文件由部署方提供（容器里是只读 Secret 挂载），应用不做 chmod；本地部署建议 0600。
- 只有 `/api/*` 与 `/s*` 的 4xx 是 JSON `{"detail": "…"}`；站点外壳与探针保持纯文本或空响应体。
- 应用不处理 `GOMEMLIMIT`，交给 Go 运行时与部署方。

## 2. 工程约定

- 提交前 `just check` 必须全绿；改到二进制时再跑 `just build`（linux/amd64 + linux/arm64 静态链接）。
- 行为变更要同步 `DESIGN.md` 与 `README.md`；界面变更后用无头 Chromium 重拍 `docs/screenshot-*.png`，
  并确认控制台无错误。
- 提交信息一行一个正交改动，只写做了什么，不写细节与原因。
- 推送同时覆盖两个远端：`git push origin main` 与 `git push local main`。
- 仓库里不提交真实凭据；部署清单里的 Secret 一律用占位值。