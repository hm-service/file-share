/** 与后端接口逐字段对齐的数据类型（契约 §5）。 */

/** GET /api/files 里的一条记录。 */
export interface FileItem {
  id: string;
  filename: string;
  sha256: string;
  size: number;
  is_favorite: boolean;
  is_public: boolean;
  /** 一级分组名；空串表示未分组。 */
  group_name: string;
  /** 所在分组（或未分组段）内的排序序号，0..n-1。 */
  sort_order: number;
  /** unix 秒 */
  created_at: number;
}

/** PUT /api/files/order 的一项：拖拽后该段内的新序号。 */
export interface FileOrderItem {
  id: string;
  sort_order: number;
}

export interface FileListResponse {
  files: FileItem[];
}

/** POST /api/auth 成功体。 */
export interface AuthResponse {
  token: string;
  expires_at: number;
}

/** 存进 localStorage 的会话。 */
export interface Session {
  token: string;
  /** unix 秒 */
  expires_at: number;
}

/** POST /api/check_file 的两种结果。 */
export interface CheckFileResponse {
  exists: boolean;
  id?: string;
  filename?: string;
  size?: number;
}

/** POST /api/upload 成功体。 */
export interface UploadResponse {
  id: string;
  sha256: string;
}
