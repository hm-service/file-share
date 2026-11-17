# file-share 设计

本文件由用户主导。当前内容由 Lead 依据迁移实现起草（用户已授权先起草再审改），
页面、接口、表结构与安全模型如与预期不符请直接修改；实现与本文不一致处按缺陷处理。

## 1. 概述

file-share 是一个自托管的文件分享服务：上传文件后按 SHA-256 内容寻址去重（秒传），
可收藏、可下载、可生成 10 分钟有效的签名分享链接（对方免登录下载）。
交付物是单个静态链接的 Go 二进制，前端构建产物通过 `go:embed` 编入二进制，
运行期唯一的状态是数据目录 `DATA_DIR`。

技术栈：Go 1.27.4（仅标准库 `net/http` + `modernc.org/sqlite` + `golang.org/x/crypto`）、
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
| `HOST` | `0.0.0.0` | IP 或合法主机名；监听地址 |
| `PORT` | `8080` | 1–65535 整数 |
| `TZ` | 空（Go 默认） | 设置时必须是合法时区名；影响日志时间戳 |
| `DATA_DIR` | `/data` | 全部状态所在目录；必须已存在且可读可写（卷挂载或部署方手工建），启动时收紧到 0700 |
| `TRUSTED_PROXIES` | `127.0.0.1/32,::1/128` | 逗号分隔 CIDR/IP；只有来自这些地址的 `X-Forwarded-For` 才可信 |
| `MAX_AUTH_CONCURRENCY` | `32` | `/api/auth` 信号量槽位，>= 1 |
| `MAX_API_CONCURRENCY` | `128` | 其余 `/api/*` 信号量槽位，>= 1 |
| `MAX_STATIC_CONCURRENCY` | `64` | 页面/静态资源与 `/s/{id}`（签名下载）信号量槽位，>= 1 |
| `MAX_FILE_SIZE` | `2GiB`（2147483648 字节） | 单个上传文件上限；纯整数按字节，也支持 `GiB`/`MiB` 后缀（大小写不敏感） |
| `GOMEMLIMIT` | — | 交给 Go 运行时自己处理，应用不读取 |
| `AUTH_TOKEN` | 无，**必填** Secret | 16–128 字符 `[A-Za-z0-9_]`；登录令牌，启动时 Argon2id 哈希后原始值即从内存与环境表清除 |
| `SESSION_SIGNING_KEY` | 无，**必填** Secret | 32–128 字符同上；会话 HMAC 签名密钥，读完从环境表删除；轮换即让全部会话下线 |
| `URL_SIGNATURE_KEY` | 无，**必填** Secret | 32–128 字符同上；签名 URL 的 HMAC 密钥，读完从环境表删除；轮换即让已签出的下载 / 分享链接失效 |
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
  group_path  TEXT    NOT NULL DEFAULT '',  -- 分组路径（1~3 段，如 "顶级" / "顶级/二级" / "顶级/二级/三级"），空串 = 未分组
  sort_order  INTEGER NOT NULL DEFAULT 0,   -- 列表顺序，升序，越小越靠前
  created_at  INTEGER NOT NULL              -- unix 秒
);
CREATE INDEX IF NOT EXISTS file_sha256_idx ON file (sha256);

CREATE TABLE IF NOT EXISTS groups (
  path       TEXT    PRIMARY KEY,          -- 分组完整路径（空分组也保留，可手动新建）
  parent     TEXT    NOT NULL,             -- 父分组路径，空串 = 一级
  name       TEXT    NOT NULL,             -- 路径最后一段
  sort_order INTEGER NOT NULL,             -- 同一父级内的顺序，升序，越小越靠前
  UNIQUE (parent, name)
);
```

- 单库文件、单连接（`SetMaxOpenConns(1)`），无外键；一致性由代码保证。
- `PRAGMA user_version = 6` 标识当前结构；旧库启动时在事务内逐级迁移（v1 → v2 补两列，
  v2 → v3 建分组顺序表并按分组名补初始顺序，v3 → v4 把 `group_order` 升级为分组注册表 `groups`，
  v4 → v5 给 `groups` 补 `parent` / `name` 并把 `file.group_name` 改名 `group_path`，
  v5 → v6 删掉 `is_public` 列），新库直接建 v6；更高版本 fail-fast。
- 列表排序：`ORDER BY sort_order ASC, created_at DESC, id ASC`；上传/秒传插入时 `sort_order = MAX+1`。
- 分组最多三级：`groups` 表存路径、父路径与同级顺序（空分组也是合法分组：新分组追加到同一父级
  末尾，改名保持位置，删掉组内最后一个文件不会删除分组）；文件上的 `group_path` 指向分组路径，
  空串表示未分组。一级 / 二级是分层，各有页面；三级只是分组，没有路由。改名 / 删除按路径前缀处理
  全部后代：后代分组与组内文件路径在同一个事务里一起改写 / 级联删除。
- 收藏状态是全局的（本服务只有一个凭据身份）。
- 删除记录时，若同一 `sha256` 已无其他记录引用，则删除物理文件。
- **数据升级策略**：结构变更在启动时一次性迁移（不做运行期兼容分支）；检测到更旧的 Python 版本数据
  （`DATA_DIR/files.db` 或 `DATA_DIR/favorite.json`）或更高的 schema 版本时，服务拒绝启动；
  升级前备份 `DATA_DIR` 是既定代价。

## 5. 页面

| 路径 | 页面 | 说明 |
| --- | --- | --- |
| `/` | 主应用（最近） | 桌面：侧栏（最近/收藏/分组 + 上传入口）+ 内容区（搜索、文件列表、行内操作、分组内拖拽排序、分组全部下载）+ 浮动上传坞；窄屏：顶栏 + 标签页 + 分组 chips + 列表 + 贴底上传坞；内容区整块可拖放上传 |
| `/favorites` | 主应用（收藏） | 同一份应用页面，按路径还原视图 |
| `/ungrouped` | 主应用（未分组） | 同上 |
| `/<一级>` | 主应用（一级分组） | 路径段按 URL 转义（`encodeURIComponent` / `decodeURIComponent`）；内容区按二级分组分段，每段只列该二级分组的直属文件 |
| `/<一级>/<二级>` | 主应用（二级分组） | 逐段转义；内容区按三级分组分段；超过两段的路径 404；未知分组回退「最近」并修正地址 |
| `/auth` | 登录页 | 输入 `AUTH_TOKEN` 换取会话；成功后进入主应用 |

前端为两个独立 HTML 入口（`index.html` / `auth.html`），不引入路由库：视图与路径是一张手写映射，
切换视图用 `history.pushState`，监听 `popstate` 还原前进 / 后退。搜索词与内容区选中的分组段写在
查询参数里（`?q=` 搜索词、`?section=` 段路径或 `direct`）：视图切换仍用 pushState 进历史，这两个
参数用 replaceState 就地更新（打字、点段不产生历史条目）；打开带参数的链接会还原搜索框与段筛选，
参数不合法（分组已不存在等）时回退「全部」并清理地址。服务端把一到两段路径
（`/favorites`、`/ungrouped`、`/<一级>`、`/<一级>/<二级>`）交给同一个 `index.html`；
`/assets/*` 与 `/auth/*` 下的路径、`*.html`、超过两段的路径仍只认构建产物里的真实文件，其余 404。
所有文案走前端 i18n（简体中文 / English），语言选择存 `localStorage`，请求通过
`Accept-Language` 传给后端。页面不含内联脚本、不引用外部资源。

- 未登录访问主应用会被重定向到 `/auth`（带上原地址的 `?next=`），登录成功后回到原地址，
  深链接里的搜索词 / 段筛选也随之恢复；接口返回 401 时前端清除会话并跳转。
- 登录页桌面端是「品牌介绍 + 登录卡」分栏，窄屏收成品牌条 + 登录卡。
- 上传队列以浮动上传坞呈现（桌面右下角、窄屏贴底），上传中显示实时速度，支持取消（中断请求，状态记为已取消）
  与重试（重新排队）；每完成一个文件就通知主界面增量刷新列表。入队时记下当前所在的分组视图，
  上传成功后把记录移动进该分组（在「最近 / 收藏 / 未分组」上传则保持未分组）。内容区整块都是拖放目标，
  拖入文件时显示覆盖层提示，松开即入队。
- 「复制分享链接」与「复制 curl 命令」都先向服务端签一个 10 分钟有效的分享链接，再复制绝对地址：
  前者是 `<origin>/s/{id}?sig=…&exp=…`，后者是 `curl -f -o '<文件名>' '<origin>/s/{id}?sig=…&exp=…'`
  （文件名取记录里的名字，落到当前目录）。签名失败时在内容区顶部提示。
- 「最近」视图按上传时间倒序（同一秒按 id 稳定排序）、按本地日期分段，标题形如
  `2026年11月7号` / `November 7, 2026`；只列最近 20 个，列表被截断时副标题写明「显示最近 20 个，共 N 个」；
  不参与拖拽。收藏 / 单分组 / 未分组视图按 `sort_order` 排序，可拖拽调整顺序。
  搜索按文件名过滤；「最近」视图有关键词时在全部文件里匹配（不截断）；分组视图里搜索自动放宽到全部段
  （不受当前段筛选限制，清空搜索后回到原段），其余视图只在各自集合内过滤；过滤期间禁用文件行拖拽与
  段拖拽，避免提交子集顺序打乱隐藏文件。
- 行内五个按钮：「下载、复制分享链接、收藏、分组、更多」，右对齐。分享按钮签名成功后短暂显示勾，
  签名中禁用；分组按钮单独打开「移动到分组」菜单（按层级缩进最多三级，分组多时整张菜单滚动）；
  「更多」菜单放重命名（自绘输入对话框）、复制 curl 命令与删除。重命名只改展示名 / 下载名，不动顺序。每行同时显示完整
  SHA-256（等宽字体、单行省略，文本可选中复制）；搜索框旁有独立刷新按钮，手动与自动刷新按请求序号取最新结果。
- 分组最多三级：一级 / 二级是分层，各有页面；三级只是分组，没有路由，只在二级分组的内容区里分段。
  桌面侧栏缩进展示一级与二级分组，一级分组行尾的箭头折叠 / 展开它的子分组（折叠状态只在内存里，
  刷新后全部展开；折叠时父行承担「当前位置」高亮）。窄屏 chips 分两行：第一行一级分组，第二行是
  当前一级分组的二级分组（chips 不折叠）。每组是独立的拖拽容器，一层一层排：侧栏的一 / 二级与
  内容区的各段都能拖，父分组连同子分组整体移动，松手提交 `PUT /api/groups/order`，失败回滚到
  服务端顺序。分组项是 `<button>`，Chrome 的原生 DnD 对按钮不生效，所以分组排序统一用 SortableJS
  的 fallback 拖拽；内容区段的拖拽 handle 是段头，段体里的文件行有自己的排序与拖拽。
- 内容区分段：当前分组下的每个子分组一段；一级视图的段只列该二级分组的直属文件（不把三级分组的
  文件堆上来），二级视图的段列三级分组的文件（三级没有后代，等同直属）。直接挂在当前分组上的文件
  单独成「直属文件」段（固定在最上面、只有存在时才出现）；段头可折叠，段头右侧有本段下载与
  分组菜单。分段默认折叠，展开状态只在内存里（不写 localStorage、不进地址栏），刷新或切换视图后
  恢复折叠；搜索时自动展开命中的段，清空搜索后恢复。分段上方的胶囊在分组视图里始终显示
  （「全部 / 直属文件 / 各子分组 / 新建子分组」），没有子分组时只剩「新建子分组」；选中某个子分组后
  上传目标跟着落到该分组，标题下注明「上传到「X」」。搜索或按段筛选时只有部分段在 DOM 里，这时禁用
  段拖拽，避免提交顺序把隐藏段挤乱。侧栏徽标显示分组子树（含全部后代）的文件数。
- 把文件拖到内容区：落在某个段的整张卡片（或该分组的胶囊）上就移进该分组，落在段以外的内容区
  就移到当前分组的直属文件；落在文件已经所在的分组上不做任何操作。
- 新建分组：侧栏底部输入 `父/子/孙` 最多建三级（父级不存在时由服务端一并创建）；分组条目右键
  （窄屏用分组视图标题旁的「⋯」）或内容区段头 / 胶囊可直接「创建子分组」，只输入子分组名；
  三级分组没有子分组，菜单里不再出现「创建子分组」。
- 分组条目右键菜单是「重命名分组、创建子分组、删除分组」。删除会连带全部后代分组，确认框为自绘对话框
  （磨砂黑色半透明遮罩）：有文件时显示数量并给出「同时删除这 N 个文件」勾选项，默认不勾选；
  不勾选时文件上移到被删分组的父路径（一级分组上移到未分组）。
- 重命名分组只改路径最后一段，改名时全部后代分组与文件路径一起改；当前视图停在被改名
  分组或其子分组上时，地址同步改名。目标分组已存在时拒绝，不做隐式合并。
  窄屏没有右键，分组视图标题旁的「⋯」打开同一个菜单。
- 行内菜单可移动到任意分组（一到三级）或新建分组（自绘输入对话框），也可以直接把文件拖到侧栏分组标签 /
  移动端 chips / 内容区某个段的整张卡片上（落点虚线高亮，松手即改分组）；拖到段以外的内容区则落到
  当前分组的直属文件。在分组视图里上传的文件会直接归入该分组。
  不提供分组删除与跨层拖拽。
- 单分组视图提供「全部下载」：前端按当前可见（段筛选 / 搜索之后）的文件顺序逐个用登录态签下载地址（10 分钟内有效）、
  逐个触发 `/s/{id}?sig=…&exp=…` 下载，每个文件是浏览器里独立的一次下载（不打包成 zip）；
  浏览器可能询问是否允许下载多个文件。
- 拖拽排序使用 SortableJS（行首 grip 为 handle，触屏长按），按段提交 0..n-1 的 `sort_order`；
  `PUT /api/files/order` 失败时提示并回滚到服务端顺序。
- 文件名搜索、段筛选与「最近 / 收藏」切换都是前端行为（只在已拉取的列表上过滤，不新增后端接口；
  搜索词与段筛选写进地址栏查询参数，便于分享定位）。
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
| GET | `/api/files` | 列表：`{"files":[{"id","filename","sha256","size","is_favorite","group_path","sort_order","created_at"}],"groups":[{"path","parent","name"},…]}`；文件按 `sort_order` 升序、创建时间倒序，`groups` 含空分组、按分组展示顺序排列 |
| POST | `/api/check_file` | 秒传预检（multipart：`client_sha256`、`filename`）；命中则直接建记录并返回 `{"exists":true,"id","filename","size"}`，否则 `{"exists":false}` |
| POST | `/api/upload` | multipart：`file` + `client_sha256`；流式落盘并校验 SHA-256；成功 `{"id","sha256"}`；超限 413 |
| PATCH | `/api/files/{id}` | 重命名 / 设置分组：`{"filename","group_path"}`（至少一个；分组路径 1~3 段、每段 trim 后 ≤64 字节，空串=未分组，改分组会移到列表末尾）；成功返回完整文件；非法 400，不存在 404 |
| POST | `/api/groups` | 手动新建空分组：`{"path"}`（1~3 段）；成功 201 `{"path"}`；已存在 409，非法路径 400 |
| POST | `/api/groups/rename` | 重命名分组最后一段（空分组也可），全部后代分组与文件路径一起改写：`{"path","new_name"}`；组内 `sort_order` 不变；成功 204；目标分组已存在 409，源分组不存在 404，非法名 400 |
| POST | `/api/groups/delete` | 删除分组：`{"path","delete_files"}`；全部后代级联；`delete_files=false` 时组内文件上移到父路径，`true` 时连记录一起删并清理无引用的物理文件；成功 204，不存在 404 |
| PUT | `/api/groups/order` | 保存某一层分组顺序：`{"parent","names"}`，`parent` 为空串表示顶层，`names` 必须覆盖该父级下全部分组；成功 204；集合对不上 409，重复 / 空名 400 |
| PUT | `/api/files/order` | 批量排序：`{"items":[{"id","sort_order"}]}`（1..2000 项，id 不重复，0..1e9）；成功 204；任一 id 不存在则整批回滚 404 |
| POST | `/api/files/{id}/favorite` | 切换收藏，返回 `{"is_favorite":bool}` |
| GET | `/api/files/{id}/download` | 附件下载（支持 Range/HEAD），需 Bearer |
| GET | `/api/files/{id}/download-url` | 签浏览器下载地址：`{"url":"/s/{id}?sig=…&exp=…"}`（10 分钟，绑定单文件） |
| POST | `/api/files/{id}/share` | 签分享链接：`{"url":"/s/{id}?sig=…&exp=…"}`（10 分钟，绑定单文件） |
| DELETE | `/api/files/{id}` | 删除记录；无引用后删除物理文件；返回 204 |

上传约束：字段顺序不限；服务端边收边算哈希，与客户端指纹不符即失败并清理临时文件；
单文件超过 `MAX_FILE_SIZE` 返回 413；同内容并发上传只保留一份物理文件、每条上传各一条记录。
取消上传由前端 `AbortController`/`XHR.abort()` 中断请求，服务端按失败路径清理临时文件，不留下半截数据。

### 6.3 匿名下载（无需登录）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/s/{file_id}?sig=…&exp=…` | 凭签名下载单个文件，返回附件（支持 Range/HEAD）；签名无效 / 过期一律 404 |

签名：`sig = base64url(HMAC-SHA256(URL_SIGNATURE_KEY, "v1|<method>|<escapedPath>|<exp>"))`，
`escapedPath` 是 `/s/{file_id}`、不含查询参数；URL 固定为 `sig` 在前、`exp` 在后，不需要 `Authorization`。
校验先验签、再判过期，签名覆盖方法与路径，换文件或改路径都验不过。浏览器下载地址与分享链接
都是 10 分钟有效（`SignedURLTTL`），两者格式与校验相同，只是用途不同。签名 URL 与会话凭据密钥不同、不能互相冒用，访问日志只记录 `/s/{file_id}`。

### 6.4 探针

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/livez` | 进程活着就 200；无闸门、不记日志 |
| GET | `/readyz` | 校验数据目录/数据库权限与读写（`__probe` 表）；`READYZ_TOKEN` 非空时需 `?token=`；失败 503，原因只进日志 |

## 7. 安全模型

- 认证：单 token（`AUTH_TOKEN`）。启动时用 Argon2id（m=19456, t=2, p=1）哈希，只保留哈希值；
  登录以恒定时间校验并补足最小耗时。
- 授权：所有 `/api/*` 除 `POST /api/auth` 外均需有效会话；匿名下载只通过 `/s/{id}?sig=&exp=` 的签名 URL：
  浏览器下载地址与分享链接都 10 分钟失效，绑定单个文件、不是会话凭据、不进日志。
- 会话签名密钥、URL 签名密钥、登录令牌、就绪探针令牌都按 Secret 处理：读完立即从进程环境表删除。
- 数据权限：`DATA_DIR` 0700、`files/` 0700、数据库 0600、物理文件 0600；启动时收紧，
  `/readyz` 持续断言。
- 响应头：`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`、
  `X-Frame-Options: DENY`、严格 CSP
  （`default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none';`）。
- 输入上限：登录 JSON 4 KiB、其它 JSON 64 KiB、排序接口 256 KiB（最多 2000 项）、秒传表单 1 MiB、上传 `MAX_FILE_SIZE`；
  文件名只取 basename 并做长度/字符校验；分组路径最多三级，每段 trim 后 ≤64 字节、不含控制字符与 `/`、
  不是 `.` / `..`、不以 `.html` 结尾，一级段不与站点已有路径（`api`、`auth`、`favorites` 等）重名。
- 并发闸门：认证、业务 API、静态资源/签名下载各自独立信号量；Argon2id 计算并发受认证闸门约束。
- 反代职责：TLS、限流、访问控制在反向代理完成；应用只信任 `TRUSTED_PROXIES` 内的转发头。
- 日志：结构化 JSON 到 stdout，一条请求一行，含真实客户端 IP 与 RFC 3339 时间戳；
  敏感值不落日志。`/livez` 不记录访问日志。

## 8. 构建与验证

- 命令入口 `just`：`just check`（gofmt/vet/test + 前端 typecheck）、`just build`
  （前端构建 + 交叉编译到 `.dist/app-<os>-<arch>`，纯静态单二进制）、`just run`、`just stop`。
- 二进制用 `CGO_ENABLED=0 -ldflags="-w -s" -trimpath` 构建，`ldd` 应报告
  `not a dynamic executable`。
- 测试：标准库 `testing`，表格驱动；`internal/testenv` 提供临时 `DATA_DIR` 实例；
  覆盖认证/会话、上传秒传与去重、签名 URL 与分享链接、探针、错误路径、安全头与 i18n。覆盖率实测值记入 README。
- 前端验证：构建后用 `scripts/shot.mjs`（skill 自带，零依赖 CDP）截图并转发控制台错误；
  关键截图存入 `docs/` 并在 README 引用。

## 9. 已知取舍

- 无多用户/权限模型：`AUTH_TOKEN` 是唯一身份，收藏状态全局共享。
- 会话不可单独撤销；轮换 `SESSION_SIGNING_KEY` 会让所有设备下线。已签出的签名 URL 独立于会话：
  轮换 `URL_SIGNATURE_KEY` 会让全部下载 / 分享链接立即失效，但不会把用户踢下线。
- 分享链接有效期内无法提前撤销、不记访问次数；需要更细粒度分享控制时再引入独立分享表。
- 分组最多三级（一级 / 二级是页面，三级只是内容区分组）；移动文件只能通过行内菜单或把行拖到
  分组标签 / 内容区段头上，跨父级拖拽排序未实现。
- 拖拽只按段提交相对顺序；搜索过滤或按段筛选时禁用文件行拖拽与段拖拽。收藏视图的拖拽只调整收藏项之间的 `sort_order`。
- 旧 Python 版本数据不迁移；v1 起的 SQLite 结构会在启动时自动迁移，升级前仍需备份 `DATA_DIR`。
