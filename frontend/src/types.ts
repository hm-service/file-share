/** 与后端接口逐字段对齐的数据类型：字段名与响应体一致，见 README 的接口表。 */

/** GET /api/files 里的一条记录。 */
export interface FileItem {
  /** 数据目录下的完整相对路径（同时是下载与分享的标识）。 */
  id: string;
  filename: string;
  /** 父目录相对路径；根目录直属文件为空串。 */
  group_path: string;
  size: number;
  /** 文件修改时间（unix 秒）。 */
  created_at: number;
  is_favorite: boolean;
}

/** GET /api/files 里的一条分组（任意深度）。 */
export interface GroupItem {
  /** 完整路径，段之间用 "/" 分隔。 */
  path: string;
  /** 父分组路径；顶级分组为空串。 */
  parent: string;
  /** 路径最后一段。 */
  name: string;
}

export interface FileListResponse {
  files: FileItem[];
  /** 全部分组（含空分组）。 */
  groups: GroupItem[];
}

/** POST /api/share 的响应：浏览器可直接导航的签名地址。 */
export interface DownloadURLResponse {
  url: string;
}

/** 权限名：与后端用户表里的 read / fav 一一对应。 */
export type PermissionName = "read" | "fav";

/** 当前登录用户：名字、权限与可见范围，永远是后端下发的，不落本地存储。 */
export interface UserInfo {
  name: string;
  permissions: PermissionName[];
  /** 可见范围："/" 表示整个数据目录，其余是授权目录的完整路径。 */
  access: string[];
}

/** GET /api/me 的响应。 */
export type MeResponse = UserInfo;

/** POST /api/auth 成功体。 */
export interface AuthResponse {
  token: string;
  expires_at: number;
  user: UserInfo;
}

/** 存进 localStorage 的会话。 */
export interface Session {
  token: string;
  /** unix 秒 */
  expires_at: number;
}

/** GET /api/storage：只读数据目录 + 收藏元数据是否可用。 */
export interface StorageInfo {
  backend: "dir";
  /** 每次请求实时探测：false 表示只读目录或 DATA_DIR 不可用。 */
  healthy: boolean;
  /** healthy 为 false 时的本地化错误文案。 */
  error?: string;
}

/** 内容区里的一行子文件夹：path 是完整路径，fileCount 是子树（含后代）的文件数。 */
export interface DirEntry {
  path: string;
  name: string;
  fileCount: number;
}