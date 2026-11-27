# file-share 设计

## 1. 概述

file-share 是一个**只读**的文件浏览前端：数据源是 SMB 共享的只读目录（同一台机器上
samba 的 `daveny/` 目录），页面只做浏览、搜索、收藏、下载与分享链接，不持有文件副本、
不写文件内容，也没有上传、删除、重命名、移动与排序。

- 一个静态链接的 Go 二进制，前端产物 `go:embed` 进去，运行时只有 `DATA_DIR` 与只读的 `FILES_DIR`。
- 目录即分组、文件即文件：每次列表请求实时扫描 `FILES_DIR`，SMB 侧增删文件刷新即见，不做同步。
- 收藏是本地元数据（`DATA_DIR/favorites/<用户名>.json`，每个用户一份），不碰只读数据目录。
- 用户表给每个账号配可见范围（`access`）：未授权目录与文件直接从视图里消失，
  但用户看到的路径始终是数据目录下的完整路径，不做「以授权目录为根」的重写。
- 分享链接仍是签名 URL：`/s/<完整相对路径>?sig=&exp=`，10 分钟 / 24 小时两档。

## 2. 部署

单机（front-server）用 podman kube play `k8s.file-share.yaml`：

- 两个 PVC：`file-share-data`（`DATA_DIR`，放用户表与收藏元数据，可写）与 `samba-data`
  （SMB 共享的数据卷，只读挂到容器内 `/samba`）。
- `FILES_DIR=/samba/daveny`：应用只读扫描该目录，路径就是 SMB 上 `\\host\share\daveny` 的内容。
- 容器 `readOnlyRootFilesystem: true`、非 root、不保留 capabilities；`samba-data` 只读挂载是
  第二道防线（即使代码有 bug 也写不进数据目录）。
- `/livez` 永远 200；`/readyz` 检查 `DATA_DIR` 可写与 `FILES_DIR` 可读，可配 `READYZ_TOKEN`。
- 镜像由 CI 构建（linux/amd64 + linux/arm64）推到 GHCR 与 ACR，front 上 `podman pull` 后重建 Pod。

本地开发：`DATA_DIR=./.scratch/data FILES_DIR=./.scratch/files go run .`，
`.scratch/files` 可以指向任意测试目录（或真实数据目录的副本）。

## 3. 环境变量与 Secret

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `DATA_DIR` | `/data` | 可写数据目录：用户表与收藏元数据；必须已存在，启动时收紧到 0700。 |
| `FILES_DIR` | `/data/files` | 只读数据根目录；必须已存在且可读。 |
| `TRUSTED_PROXIES` | 空 | 逗号分隔的 CIDR，用于取客户端 IP。 |
| `SESSION_SIGNING_KEY` | 无（必填） | 会话 HMAC 密钥，32–128 位 `[A-Za-z0-9_]`。 |
| `URL_SIGNATURE_KEY` | 无（必填） | 签名 URL 的 HMAC 密钥，32–128 位 `[A-Za-z0-9_]`。 |
| `READYZ_TOKEN` | 空 | 非空时 `/readyz` 要求 `?token=` 一致。 |
| `HOST` / `PORT` | `0.0.0.0` / `8080` | 监听地址。 |
| `TZ` | 空 | 设了必须是有效的时区名。 |
| `MAX_AUTH_CONCURRENCY` / `MAX_API_CONCURRENCY` / `MAX_STATIC_CONCURRENCY` | 32 / 128 / 64 | 三组请求的信号量槽位。 |

用户表固定读 `DATA_DIR/users.yaml`（容器里由 Secret 以 subPath 挂载）。每个账号两个字段：

- `permissions`：`read`（浏览、下载、分享）与 `fav`（收藏）；`fav` 必须同时带 `read`。
- `access`：可见范围的绝对路径数组，必填。`["/"]` 表示整个数据目录；`["/目录A"]` 表示只显示
  该目录；多值取并集。大小写敏感：配置写 `/DOCS` 就只认 `/DOCS`，实际目录是 `/Docs` 时启动失败，
  不替用户猜大小写。条目必须是已存在的目录，段级符号链接拒绝启动。

```yaml
users:
  - name: admin
    password: ...
    permissions: [read, fav]
    access: ["/"]
  - name: viewer
    password: ...
    permissions: [read]
    access: ["/目录A"]
```

## 4. 数据目录与运行期状态

`DATA_DIR`（应用自己维护，0700）：

```
users.yaml              部署方提供，只读用户表
favorites/<用户名>.json  应用维护的每用户收藏元数据（0600），{"paths": ["目录A/说明.md", ...]}
```

没有数据库、没有 blob 目录、没有上传暂存：只读视图不存任何文件内容。

`FILES_DIR`（只读）：

- 目录即分组，文件即文件；**深度不限制**。
- 可见范围（`access`）之外的目录与文件整棵跳过：不进入列表、搜索、收藏与下载；
  授权目录的祖先链保留，用户能看到自己处在哪一层。
- 点开头的文件 / 目录是普通数据，照常展示与下载（不做隐藏处理）。
- 符号链接与设备等非常规文件跳过：不跟随链接，避免逃出数据根目录。
- 排序：分组与文件都按名称（大小写不敏感）排序；「最近」按修改时间倒序取 20 条。
- 文件时间取文件系统 mtime，大小取 `st_size`。

## 5. 页面

- 视图与地址栏一一对应：`/`「全部」（根目录）、`/recent`「最近」、`/favorites`「收藏」、
  `/<任意深度路径>`「文件夹」。面包屑第一项「全部」可点，随时回到根目录。
- 桌面端：「顶栏（搜索框 / 语言 / 当前账号与只读徽标 / 退出） + 左侧目录树 + 内容区表格」。
- 窄屏：目录树收进抽屉（左上角菜单按钮打开）；内容区隐藏大小与修改时间列；「下载全部」
  收进右下角悬浮按钮（FAB）；页面禁用双指与双击缩放（`viewport` 上限 1 倍 + `touch-action`）。
- 抽屉里点行首箭头只展开 / 收起子树，抽屉保持打开；点文件夹名才进入该目录并关闭抽屉。
- 目录树：快捷入口（全部 / 最近 / 收藏，收藏入口在无 `fav` 权限时不显示） + 任意深度的目录树
  （空目录也显示，行尾是子树文件数），展开 / 折叠状态存 `localStorage`。
- 内容区：面包屑逐级可点；子目录行在前（显示子树项数），文件行在后；右上角「下载全部（大小）」
  下载当前视图里的可见文件。
- 文件行操作：下载、分享链接（10 分钟档）、收藏（无 `fav` 权限时不显示）、
  「更多」（复制 10 分钟 / 24 小时分享链接）；
  文件名点开详情（大小、修改时间、所在目录、完整路径）。移动端把分享入口收进「更多」菜单。
- 「最近」与搜索结果跨目录混排，行内标出文件所在目录。
- 搜索是全局的：在可见范围内按文件名子串过滤，写进 `?q=`；搜索期间面包屑显示「搜索结果」。
- 没有上传按钮、拖拽上传、删除、重命名、移动、排序：界面里不出现这些入口。

## 6. 接口

全部接口在 `/api` 下（登录除外），统一 `Authorization: Bearer <session>`；错误体是
`{"detail": "…"}`（按 `Accept-Language` 本地化）。

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| POST | `/api/auth` | 无 | 用户名 + 密码换会话，返回 `{token, expires_at, user}`。 |
| GET | `/api/me` | read | 当前用户、权限与可见范围：`{name, permissions, access}`。 |
| GET | `/api/files` | read | 返回 `{files, groups}`：当前用户可见范围内的文件与目录（含空目录）。 |
| GET | `/api/storage` | read | `{backend:"dir", healthy, error?}`：只读目录与收藏元数据是否可用。 |
| PUT / DELETE | `/api/favorites?path=` | read+fav | 收藏 / 取消收藏，返回 `{is_favorite}`；只能收藏可见路径。 |
| POST | `/api/share` | read | body `{"path","ttl":"10m"|"24h"}`，返回 `{url}`。 |
| GET | `/api/download-url?path=` | read | 签一个 10 分钟有效的下载地址，返回 `{url}`。 |
| GET | `/s/<相对路径>?sig=&exp=` | 签名 | 匿名下载入口；路径逐段百分号编码，签名覆盖转义后的路径。 |
| GET | `/livez`、`/readyz` | 无 | 存活 / 就绪探针。 |

`GET /api/files` 的单文件记录：

```json
{"id": "软件/说明.md", "filename": "说明.md", "group_path": "软件",
 "size": 9, "created_at": 1795086516, "is_favorite": false}
```

`id` 就是相对 `FILES_DIR` 的完整路径（含授权目录本身，例如受限账号看到的是
`目录A/说明.md`），下载与分享都用它；分组：

```json
{"path": "软件/安卓应用", "parent": "软件", "name": "安卓应用"}
```

## 7. 安全模型

- 会话：HMAC-SHA256 无状态凭据（`v2|<exp>|<nonce>|<username>`），30 天有效；改
  `SESSION_SIGNING_KEY` 会让所有会话失效。
- 登录：用户表 YAML 明文密码，SHA-256 恒时比较，且每次登录至少 1 秒 + 抖动，抹掉时序侧信道。
- 签名 URL：`body = v1|GET|<escapedPath>|<exp>`，HMAC 用 `URL_SIGNATURE_KEY`；换路径、换方法、
  改过期时间都验不过。10 分钟（页面内下载）与 24 小时（分享链接）两档；HEAD 与 GET 共用签名。
- 可见范围：请求路径必须命中账号的 `access`，否则按「不存在」处理（404，不泄露存在性）。
  不可访问的目录与文件在扫描阶段就整棵跳过，列表、搜索、收藏、签发链接都拿不到。
- 路径安全：所有外部路径先做相对路径校验（拒绝绝对路径、`..`、空段、反斜杠），
  再用 `os.Root` 打开；打开前逐段 `Lstat` 拒绝符号链接 —— `os.Root` 只挡住逃出数据目录，
  数据目录内部的链接仍会被跟随，段级检查才能防止「目录A 里的链接指向目录B」绕开可见范围。
  `samba-data` 只读挂载是第二道防线。
- 容器：`readOnlyRootFilesystem`、非 root、drop ALL capabilities；`/data` 是唯一可写卷。
- 数据目录 0700；`favorites/` 0700、每个收藏文件 0600；用户表由部署方维护。

## 8. 构建与验证

- `just check`：gofmt 检查 + `go vet` + `go test` + 前端 `pnpm typecheck`。
- `just frontend` / `just build`：构建前端产物并交叉编译 linux/amd64 + arm64。
- Go 测试：`app` 覆盖目录扫描（任意深度、隐藏文件照常展示、符号链接、空目录）、
  可见范围过滤与祖先链、可见范围大小写敏感、收藏按用户隔离、路径校验、签名 URL 往返；`api` 覆盖列表 / 收藏（含 fav 权限 403）/
  分享 / 下载 / 401 / 404 / 405；`main` 覆盖环境变量解析与站点外壳。
- 本地验收：`FILES_DIR` 指向测试目录起服务，用 Playwright 走「登录 → 目录 → 搜索 → 收藏 →
  下载 → 分享链接 → 移动端」。
- 文档截图由无头 Chromium 重拍，`DESIGN.md` / `README.md` 与实现同步。

## 9. 已知取舍

- 可见范围与文件路径都按大小写精确匹配：手输 URL 大小写不对时 404（从列表点进来的路径
  永远是真实大小写，不受影响）。
- `recent` 与 `favorites` 是保留的一级路径：顶级目录恰好叫这两个名字时会被当成对应视图
  （只读视图不能改数据目录，属于已知限制）。
- 每次列表请求实时扫描 `FILES_DIR`，不做缓存或索引：当前规模（数百文件）毫秒级；
  目录数量到十万级时再考虑索引。
- 目录深度不限制（页面路由最多认 16 段，超过按未知路径 404）。
- 收藏记录与文件解耦：SMB 侧删掉文件后收藏记录仍在，读取时自然匹配不到、不显示。
- 没有文件内容预览与打包下载：下载是逐个文件的独立下载（浏览器可能询问是否允许批量下载）。
- 上传 / 删除 / 重命名 / 移动 / 排序全部移除；要改文件请直接操作 SMB 共享。
- 数据目录由部署方负责备份、权限与监控；应用只读它。