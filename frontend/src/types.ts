/** 与后端接口逐字段对齐的数据类型（契约 §5）。 */

/** GET /api/files 里的一条记录。 */
export interface FileItem {
  id: string;
  filename: string;
  sha256: string;
  size: number;
  is_favorite: boolean;
  is_public: boolean;
  /** unix 秒 */
  created_at: number;
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
