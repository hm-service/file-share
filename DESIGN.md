# file-share 设计

本文件由用户主导。当前内容由 Lead 依据迁移实现起草（用户已授权先起草再审改），
页面、接口、表结构与安全模型如与预期不符请直接修改；实现与本文不一致处按缺陷处理。

## 1. 概述

file-share 是一个自托管的文件分享服务：上传文件后按 SHA-256 内容寻址去重（秒传），
可收藏、可下载、可把单个文件切换为公开并通过匿名直链分享。
交付物是单个静态链接的 Go 二进制，前端构建产物通过 `go:embed` 编入二进制，
运行期唯一的状态是数据目录 `DATA_DIR`。

技术栈：Go 1.27.3（仅标准库 `net/http` + `modernc.org/sqlite` + `golang.org/x/crypto`）、
Vue 3 + Vite + TypeScript；前端全量位于 `frontend/`，构建产物固定为 `frontend/dist`。
容器优先，面向 Podman（`podman kube play` / systemd Quadlet）。

## 2. 部署

- 镜像：`ghcr.io/hm-service/file-share:<yyyy-MM-dd>` 与 `:latest`，同时构建
  `linux/amd64` + `linux/arm64`；Go 在 `$BUILDPLATFORM` 上交叉编译。
- 多阶段 Dockerfile：node 阶段构建前端 → Go 阶段编译二进制 → alpine 运行阶段只保留一个二进制。
- `k8s.file-share.yaml`：Pod + PVC + ConfigMap + Secret；`readOnlyRootFilesystem: true`、
  `capabilities.drop: [ALL]`；探针 `/livez`、`/readyz?token=<READYZ_TOKEN>`。
- 镜像不设 `USER`，容器以 root 运行；`DATA_DIR` 需对该用户可写（PVC/具名卷默认满足，
  宿主机 bind mount 需先处理属主，否则收紧权限会因缺少 `CAP_FOWNER` 失败）。
- `file-share.kube`：Podman Quadlet 模板（`Yaml=./k8s.%p.yaml`）。
- 运行期不写二进制目录，全部状态落在挂载卷 `DATA_DIR=/data`。

## 3. 环境变量与 Secret

| 变量 | 默认 | 校验 / 说明 |
| --- | --- | --- |
| `HOST` | `127.0.0.1` | IP 或合法主机名；监听地址 |
| `PORT` | `8080` | 1–65535 整数 |
| `TZ` | 空（Go 默认） | 设置时必须是合法时区名；影响日志时间戳 |
| `DATA_DIR` | `/data` | 全部状态所在目录；必须已存在且可读可写（卷挂载或部署方手工建），启动时收紧到 0700 |
| `TRUSTED_PROXIES` | `127.0.0.1/32,::1/128` | 逗号分隔 CIDR/IP；只有来自这些地址的 `X-Forwarded-For` 才可信 |
| `MAX_AUTH_CONCURRENCY` | `32` | `/api/auth` 信号量槽位，>= 1 |
| `MAX_API_CONCURRENCY` | `128` | 其余 `/api/*` 信号量槽位，>= 1 |
| `MAX_STATIC_CONCURRENCY` | `64` | 页面/静态资源与 `/s/*` 信号量槽位，>= 1 |
| `MAX_FILE_SIZE` | `2GiB`（2147483648 字节） | 单个上传文件上限；纯整数按字节，也支持 `GiB`/`MiB` 后缀（大小写不敏感） |
| `GOMEMLIMIT` | — | 交给 Go 运行时自己处理，应用不读取 |
| `AUTH_TOKEN` | 无，**必填** Secret | 16–128 字符 `[A-Za-z0-9_]`；登录令牌，启动时 Argon2id 哈希后原始值即从内存与环境表清除 |
| `SESSION_SIGNING_KEY` | 无，**必填** Secret | 32–128 字符同上；会话 HMAC 签名密钥，读完从环境表删除；轮换即让全部会话下线 |
| `READYZ_TOKEN` | 空 | 空表示 `/readyz` 不设门槛；非空时 16–128 字符同上 |

数值/主机/端口/时区写错直接拒绝启动（exit code 1），不做静默降级。

## 4. 数据目录与表结构

```
DATA_DIR/                   0700
  file-share.db             0600  SQLite（WAL 模式）
  file-share.db-wal/-shm          WAL 附带文件
  files/                    0700  物理文件，文件名是内容的 sha256
    <sha256>                0600  内容寻址；多条记录可指向同一文件（去重）
    .upload-*.tmp                 上传中的临时文件，启动时清理残留
```

```sql
CREATE TABLE IF NOT EXISTS file (
  id          TEXT    PRIMARY KEY,          -- uuid4，32 位小写十六进制，无连字符
  filename    TEXT    NOT NULL,             -- 展示名 / 下载名，仅 basename，<= 255 字节
  sha256      TEXT    NOT NULL,             -- 内容指纹，64 位小写十六进制
  size        INTEGER NOT NULL,             -- 字节数
  is_favorite INTEGER NOT NULL DEFAULT 0,   -- 0/1，全局收藏
  is_public   INTEGER NOT NULL DEFAULT 0,   -- 0/1，公开后 /s/{id} 可匿名下载
  group_name  TEXT    NOT NULL DEFAULT '',  -- 一级分组名，空串 = 未分组
  sort_order  INTEGER NOT NULL DEFAULT 0,   -- 列表顺序，升序，越小越靠前
  created_at  INTEGER NOT NULL              -- unix 秒
);
CREATE INDEX IF NOT EXISTS file_sha256_idx ON file (sha256);

CREATE TABLE IF NOT EXISTS group_order (
  name       TEXT    PRIMARY KEY,          -- 当前有文件的分组名，空分组不落库
  sort_order INTEGER NOT NULL              -- 分组展示顺序，升序，越小越靠前
);
```

- 单库文件、单连接（`SetMaxOpenConns(1)`），无外键；一致性由代码保证。
- `PRAGMA user_version = 3` 标识当前结构；旧库启动时在事务内逐级迁移（v1 → v2 补两列，
  v2 → v3 建 `group_order` 并按分组名补初始顺序），新库直接建 v3；更高版本 fail-fast。
- 列表排序：`ORDER BY sort_order ASC, created_at DESC, id ASC`；上传/秒传插入时 `sort_order = MAX+1`。
- 分组是文件上的字符串字段（一级、无分组表）；分组列表由文件集合现算，空串表示未分组。
  分组展示顺序单独存在 `group_order`（只保留有文件的分组：新分组追加到末尾，改名保持位置，
  最后一个文件被移走 / 删除时清理对应行）。
- 收藏与公开状态是全局的（本服务只有一个凭据身份）。
- 删除记录时，若同一 `sha256` 已无其他记录引用，则删除物理文件。
- **数据升级策略**：结构变更在启动时一次性迁移（不做运行期兼容分支）；检测到更旧的 Python 版本数据
  （`DATA_DIR/files.db` 或 `DATA_DIR/favorite.json`）或更高的 schema 版本时，服务拒绝启动；
  升级前备份 `DATA_DIR` 是既定代价。

## 5. 页面

| 路径 | 页面 | 说明 |
| --- | --- | --- |
| `/` | 主应用 | 桌面：侧栏（全部/收藏/分组 + 上传入口）+ 内容区（搜索、文件列表、行内操作、分组内拖拽排序、分组全部下载）+ 浮动上传坞；窄屏：顶栏 + 标签页 + 分组 chips + 列表 + 贴底上传坞；内容区整块可拖放上传 |
| `/auth` | 登录页 | 输入 `AUTH_TOKEN` 换取会话；成功后进入主应用 |

前端为两个独立 HTML 入口（`index.html` / `auth.html`），不引入路由库；
所有文案走前端 i18n（简体中文 / English），语言选择存 `localStorage`，请求通过
`Accept-Language` 传给后端。页面不含内联脚本、不引用外部资源。

- 未登录访问主应用会被重定向到 `/auth`；接口返回 401 时前端清除会话并跳转。
- 登录页桌面端是「品牌介绍 + 登录卡」分栏，窄屏收成品牌条 + 登录卡。
- 上传队列以浮动上传坞呈现（桌面右下角、窄屏贴底），上传中显示实时速度，支持取消（中断请求，状态记为已取消）
  与重试（重新排队）；每完成一个文件就通知主界面增量刷新列表。内容区整块都是拖放目标，
  拖入文件时显示覆盖层提示，松开即入队。
- 「复制直链」只在文件公开时可用，复制结果为 `<origin>/s/<file_id>`。
- 「全部」视图平铺展示、固定按上传时间倒序（同一秒按 id 稳定排序），不参与拖拽；
  收藏 / 单分组 / 未分组视图按 `sort_order` 排序，可拖拽调整顺序。
  搜索按文件名过滤；过滤期间禁用拖拽，避免提交子集顺序打乱隐藏文件。
- 行内操作：收藏、下载、重命名（`window.prompt`，与新建分组同一交互）、复制直链、公开/私有、
  移动到分组、删除。重命名只改展示名 / 下载名，不动顺序。每行同时显示完整 SHA-256（等宽字体、
  单行省略，文本可选中复制）；搜索框旁有独立刷新按钮，手动与自动刷新按请求序号取最新结果。
- 分组是文件上的一级字符串字段：行内菜单可移动到已有分组或新建分组（`window.prompt`），
  也可以直接把文件拖到侧栏分组标签 / 移动端 chips 上（落点虚线高亮，松手即改分组）；
  侧栏 / 移动端 chips 同时用于按分组过滤；分组本身可直接拖拽排序（顺序存 `group_order`，
  松手提交 `PUT /api/groups/order`，失败回滚到服务端顺序）；单分组视图标题旁可重命名分组
  （批量改写组内 `group_name`，保持组内顺序；目标分组已存在时拒绝，不做隐式合并）；
  不提供空分组、分组删除与跨组拖拽。
- 单分组视图提供「全部下载」：前端按组内顺序逐个用登录态换 60 秒票据、逐个触发 `/d?t=…` 下载，
  每个文件是浏览器里独立的一次下载（不打包成 zip）；浏览器可能询问是否允许下载多个文件。
- 拖拽排序使用 SortableJS（行首 grip 为 handle，触屏长按），按段提交 0..n-1 的 `sort_order`；
  `PUT /api/files/order` 失败时提示并回滚到服务端顺序。
- 文件名搜索与「全部 / 收藏」切换都是前端行为（只在已拉取的列表上过滤，不新增后端接口）。
- 响应式：桌面为固定侧栏 + 内容区 + 浮动上传坞；≤900px 改为顶部工具栏 + 标签页 + 分组 chips + 文件列表 + 贴底上传面板，
  文件操作按钮常显并适配安全区。两种布局共用同一套组件与状态。

## 6. 接口

错误响应统一为 `{"detail": "<按 Accept-Language 本地化的文案>"}`。
登录后使用 `Authorization: Bearer <session>`。

### 6.1 认证

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/auth` | 请求 `{"token":"<AUTH_TOKEN>"}`；成功 `200 {"token":"<session>","expires_at":<unix>}`；错误 400/401/405 |

- 原始凭据只允许出现在 `/api/auth`；校验有 1–1.2 秒的最小耗时，防止时序侧信道。
- 会话：`base64url("<exp>|<nonce>") + "." + base64url(HMAC-SHA256(SESSION_SIGNING_KEY, "v1|"+payload))`，
  nonce 16 字节随机，有效期 30 天。先验签名再验过期。
- 无状态会话：无法单独撤销某个会话，只能轮换 `SESSION_SIGNING_KEY` 让全部下线；退出登录由前端清除凭据。

### 6.2 文件（均需登录）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/files` | 列表：`{"files":[{"id","filename","sha256","size","is_favorite","is_public","group_name","sort_order","created_at"}],"groups":["分组名",…]}`；文件按 `sort_order` 升序、创建时间倒序，`groups` 按分组展示顺序排列 |
| POST | `/api/check_file` | 秒传预检（multipart：`client_sha256`、`filename`）；命中则直接建记录并返回 `{"exists":true,"id","filename","size"}`，否则 `{"exists":false}` |
| POST | `/api/upload` | multipart：`file` + `client_sha256`；流式落盘并校验 SHA-256；成功 `{"id","sha256"}`；超限 413 |
| PATCH | `/api/files/{id}` | 重命名 / 设置分组：`{"filename","group_name"}`（至少一个；分组名 trim 后 ≤64 字节，空串=未分组，改分组会移到列表末尾）；成功返回完整文件；非法 400，不存在 404 |
| POST | `/api/groups/rename` | 批量重命名分组：`{"name","new_name"}`；组内 `sort_order` 不变；成功 204；目标分组已存在 409，源分组不存在 404，非法名 400 |
| PUT | `/api/groups/order` | 保存分组顺序：`{"names":["…"]}` 必须覆盖当前全部分组；成功 204；集合对不上 409，重复 / 空名 400 |
| PUT | `/api/files/order` | 批量排序：`{"items":[{"id","sort_order"}]}`（1..2000 项，id 不重复，0..1e9）；成功 204；任一 id 不存在则整批回滚 404 |
| POST | `/api/files/{id}/favorite` | 切换收藏，返回 `{"is_favorite":bool}` |
| POST | `/api/files/{id}/public` | 切换公开，返回 `{"is_public":bool}` |
| GET | `/api/files/{id}/download` | 附件下载（支持 Range/HEAD），需 Bearer |
| GET | `/api/files/{id}/download-ticket` | 换短期下载票据：`{"url":"/d?t=…"}`（60 秒，绑定单文件） |
| DELETE | `/api/files/{id}` | 删除记录；无引用后删除物理文件；返回 204 |

上传约束：字段顺序不限；服务端边收边算哈希，与客户端指纹不符即失败并清理临时文件；
单文件超过 `MAX_FILE_SIZE` 返回 413；同内容并发上传只保留一份物理文件、每条上传各一条记录。
取消上传由前端 `AbortController`/`XHR.abort()` 中断请求，服务端按失败路径清理临时文件，不留下半截数据。

### 6.3 公开下载（无需登录）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/s/{id}` | 仅当记录 `is_public = 1` 且物理文件存在时返回附件；其余情况一律 404，不区分原因 |
| GET | `/d?t=…` | 凭 60 秒票据下载单个文件，返回附件（支持 Range/HEAD）；票据无效 / 过期一律 404 |

票据格式：`base64url("<exp>|<file_id>") + "." + base64url(HMAC-SHA256(key, "d1|"+payload))`，
与会话共用 `SESSION_SIGNING_KEY` 但签名前缀不同，两者不能互相冒用。票据只放在查询参数里，
访问日志只记录 `/d`，不会把凭据写进日志；下载完成后过期即失效。

### 6.4 探针

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/livez` | 进程活着就 200；无闸门、不记日志 |
| GET | `/readyz` | 校验数据目录/数据库权限与读写（`__probe` 表）；`READYZ_TOKEN` 非空时需 `?token=`；失败 503，原因只进日志 |

## 7. 安全模型

- 认证：单 token（`AUTH_TOKEN`）。启动时用 Argon2id（m=19456, t=2, p=1）哈希，只保留哈希值；
  登录以恒定时间校验并补足最小耗时。
- 授权：所有 `/api/*` 除 `POST /api/auth` 外均需有效会话；公开文件只通过 `/s/{id}` 暴露；
  浏览器下载用 60 秒、绑定单个文件的 HMAC 票据走 `/d?t=…`，票据不是会话凭据，不进日志。
- 会话签名密钥、登录令牌、就绪探针令牌都按 Secret 处理：读完立即从进程环境表删除。
- 数据权限：`DATA_DIR` 0700、`files/` 0700、数据库 0600、物理文件 0600；启动时收紧，
  `/readyz` 持续断言。
- 响应头：`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`、
  `X-Frame-Options: DENY`、严格 CSP
  （`default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none';`）。
- 输入上限：登录 JSON 4 KiB、其它 JSON 64 KiB、排序接口 256 KiB（最多 2000 项）、秒传表单 1 MiB、上传 `MAX_FILE_SIZE`；
  文件名只取 basename 并做长度/字符校验，分组名 trim 后 ≤64 字节且不含控制字符。
- 并发闸门：认证、业务 API、静态资源/公开下载各自独立信号量；Argon2id 计算并发受认证闸门约束。
- 反代职责：TLS、限流、访问控制在反向代理完成；应用只信任 `TRUSTED_PROXIES` 内的转发头。
- 日志：结构化 JSON 到 stdout，一条请求一行，含真实客户端 IP 与 RFC 3339 时间戳；
  敏感值不落日志。`/livez` 不记录访问日志。

## 8. 构建与验证

- 命令入口 `just`：`just check`（gofmt/vet/test + 前端 typecheck）、`just build`
  （前端构建 + 交叉编译到 `.dist/app-<os>-<arch>`，纯静态单二进制）、`just run`、`just stop`。
- 二进制用 `CGO_ENABLED=0 -ldflags="-w -s" -trimpath` 构建，`ldd` 应报告
  `not a dynamic executable`。
- 测试：标准库 `testing`，表格驱动；`internal/testenv` 提供临时 `DATA_DIR` 实例；
  覆盖认证/会话、上传秒传与去重、公开路由、探针、错误路径、安全头与 i18n。覆盖率实测值记入 README。
- 前端验证：构建后用 `scripts/shot.mjs`（skill 自带，零依赖 CDP）截图并转发控制台错误；
  关键截图存入 `docs/` 并在 README 引用。

## 9. 已知取舍

- 无多用户/权限模型：`AUTH_TOKEN` 是唯一身份，收藏与公开状态全局共享。
- 会话不可单独撤销；轮换 `SESSION_SIGNING_KEY` 会让所有设备下线。
- 公开直链无过期时间/访问计数；需要更细粒度分享控制时再引入独立分享表。
- 分组只有一级，且没有独立分组表：不能创建空分组，也不支持重命名/删除分组；移动文件只能通过行内菜单，跨组拖拽未实现。
- 拖拽只按段提交相对顺序；搜索过滤时禁用拖拽。收藏视图的拖拽只调整收藏项之间的 `sort_order`。
- 旧 Python 版本数据不迁移；v1 起的 SQLite 结构会在启动时自动迁移，升级前仍需备份 `DATA_DIR`。
