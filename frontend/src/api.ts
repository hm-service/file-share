/**
 * 统一 API 客户端：Bearer 认证 + Accept-Language + 401 处理 + detail 解析。
 *
 * - 每个请求都带 `Authorization: Bearer <session>` 与 `Accept-Language`（后端按它挑错误文案）。
 * - 除登录接口外，401 一律清 session 跳 /auth（登录页自己把 401 显示成「令牌不正确」）。
 * - 错误体是 `{"detail": "..."}`，直接作为 ApiError.message（后端已本地化）。
 * - 下载不能直接导航到需要 Bearer 的接口：先换一个签名 URL，再让浏览器导航到该地址，
 *   由浏览器完成流式下载（Range / 原生进度都在浏览器侧）。
 */
import { abortError, isAbortError } from "./abort";
import { getLocale, t } from "./i18n";
import { getSession, toLogin } from "./session";
import type {
  AuthResponse,
  CheckFileResponse,
  DownloadURLResponse,
  FileItem,
  FileListResponse,
  FileOrderItem,
  UploadResponse,
} from "./types";

/** 接口报错：带 HTTP 状态码（0 = 网络层失败），message 是后端 detail 或本地化兜底。 */
export class ApiError extends Error {
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

interface RequestOptions extends RequestInit {
  /** 登录请求自己处理 401，不能触发跳转。 */
  redirectOn401?: boolean;
}

/** 当前请求头：Accept-Language 跟界面语言，有会话就带 Bearer。 */
export function authHeaders(extra: Record<string, string> = {}): Record<string, string> {
  const headers: Record<string, string> = { "Accept-Language": getLocale(), ...extra };
  const session = getSession();
  if (session) headers.Authorization = `Bearer ${session.token}`;
  return headers;
}

function parseJson(text: string): Record<string, unknown> {
  try {
    const value: unknown = JSON.parse(text);
    return value !== null && typeof value === "object" ? (value as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function detailOf(payload: Record<string, unknown>): string {
  return typeof payload.detail === "string" ? payload.detail : "";
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { redirectOn401 = true, headers: extraHeaders, ...init } = options;
  let response: Response;
  try {
    response = await fetch(path, {
      ...init,
      headers: authHeaders((extraHeaders as Record<string, string> | undefined) ?? {}),
    });
  } catch (error) {
    // 取消不是「网络异常」：把 AbortError 原样抛给调用方，由上传队列识别成 cancelled
    if (isAbortError(error)) throw error;
    throw new ApiError(t("error.network"), 0);
  }

  if (response.status === 204) return undefined as T;
  const payload = parseJson(await response.text().catch(() => ""));

  if (response.status === 401) {
    if (redirectOn401) toLogin();
    throw new ApiError(detailOf(payload) || t("error.network"), 401);
  }
  if (!response.ok) {
    throw new ApiError(detailOf(payload) || t("error.network"), response.status);
  }
  return payload as T;
}

/** POST /api/auth：成功返回会话，失败抛 ApiError（401 = 令牌不对）。 */
export function login(token: string): Promise<AuthResponse> {
  return request<AuthResponse>("/api/auth", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token }),
    redirectOn401: false,
  });
}

/** GET /api/files：文件列表 + 全部分组（含空分组）。 */
export async function listFiles(): Promise<FileListResponse> {
  const payload = await request<FileListResponse>("/api/files");
  return {
    files: Array.isArray(payload?.files) ? payload.files : [],
    groups: Array.isArray(payload?.groups) ? payload.groups : [],
  };
}

/** POST /api/check_file：服务端已有该 blob 时直接建记录（秒传）。 */
export function checkFile(
  sha256: string,
  filename: string,
  signal?: AbortSignal,
): Promise<CheckFileResponse> {
  const body = new FormData();
  body.append("client_sha256", sha256);
  body.append("filename", filename);
  return request<CheckFileResponse>("/api/check_file", { method: "POST", body, signal });
}

/**
 * POST /api/upload：用 XHR 而不是 fetch —— 只有 XHR 能拿到上传进度事件。
 * 必须手动带上 Bearer 头，不能依赖浏览器自动带。
 *
 * 可选 AbortSignal：队列点「取消」时 abort() 掉 XHR（服务端会丢弃临时文件），
 * 这里统一 reject 一个 AbortError，让调用方识别成 cancelled 而不是 failed。
 */
export function uploadFile(
  file: File,
  sha256: string,
  onProgress?: (percent: number) => void,
  signal?: AbortSignal,
): Promise<UploadResponse> {
  return new Promise<UploadResponse>((resolve, reject) => {
    if (signal?.aborted) {
      reject(abortError());
      return;
    }

    const xhr = new XMLHttpRequest();
    let settled = false;

    const cleanup = (): void => {
      signal?.removeEventListener("abort", onAbort);
    };
    const finish = (action: () => void): void => {
      if (settled) return;
      settled = true;
      cleanup();
      action();
    };
    const onAbort = (): void => {
      xhr.abort();
    };
    signal?.addEventListener("abort", onAbort, { once: true });

    xhr.open("POST", "/api/upload");
    const headers = authHeaders();
    for (const [name, value] of Object.entries(headers)) xhr.setRequestHeader(name, value);

    xhr.upload.addEventListener("progress", (event) => {
      if (event.lengthComputable && !settled) {
        onProgress?.(Math.round((event.loaded / event.total) * 100));
      }
    });

    xhr.addEventListener("load", () => {
      finish(() => {
        const payload = parseJson(xhr.responseText);
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve(payload as unknown as UploadResponse);
          return;
        }
        if (xhr.status === 401) toLogin();
        reject(new ApiError(detailOf(payload) || t("error.network"), xhr.status));
      });
    });
    xhr.addEventListener("error", () => {
      finish(() => reject(new ApiError(t("error.network"), 0)));
    });
    xhr.addEventListener("abort", () => {
      finish(() => reject(abortError()));
    });

    const body = new FormData();
    body.append("file", file, file.name);
    body.append("client_sha256", sha256);
    xhr.send(body);
  });
}

/** POST /api/files/{id}/favorite：返回切换后的状态。 */
export function toggleFavorite(id: string): Promise<{ is_favorite: boolean }> {
  return request<{ is_favorite: boolean }>(
    `/api/files/${encodeURIComponent(id)}/favorite`,
    { method: "POST" },
  );
}

/** POST /api/files/{id}/share：签一条 10 分钟有效的分享链接，返回可直接发给对方的相对地址。 */
export function createShareLink(id: string): Promise<{ url: string }> {
  return request<{ url: string }>(`/api/files/${encodeURIComponent(id)}/share`, {
    method: "POST",
  });
}

/** DELETE /api/files/{id}（204，无响应体）。 */
export function deleteFile(id: string): Promise<void> {
  return request<void>(`/api/files/${encodeURIComponent(id)}`, { method: "DELETE" });
}

/** PATCH /api/files/{id}：修改分组路径（空串 = 移出分组），返回更新后的完整记录。 */
export function updateFileGroup(id: string, groupPath: string): Promise<FileItem> {
  return request<FileItem>(`/api/files/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ group_path: groupPath }),
  });
}

/** PATCH /api/files/{id}：重命名文件，返回更新后的完整记录。 */
export function renameFile(id: string, filename: string): Promise<FileItem> {
  return request<FileItem>(`/api/files/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ filename }),
  });
}

/** PUT /api/groups/order：重写某个父级下子分组的顺序（需覆盖该父级下全部分组，成功 204）。 */
export function reorderGroups(parent: string, names: string[]): Promise<void> {
  return request<void>("/api/groups/order", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ parent, names }),
  });
}

/** POST /api/groups：手动新建一个空分组（"父/子" 表示二级分组），成功返回规范化后的路径。 */
export function createGroup(path: string): Promise<{ path: string }> {
  return request<{ path: string }>("/api/groups", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path }),
  });
}

/**
 * POST /api/groups/delete：删除分组，子分组一并删除（成功 204）。
 * deleteFiles 为 true 时组内文件一起删掉，否则上移到上级分组 / 未分组。
 */
export function deleteGroup(path: string, deleteFiles: boolean): Promise<void> {
  return request<void>("/api/groups/delete", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path, delete_files: deleteFiles }),
  });
}

/** POST /api/groups/rename：改写分组路径的最后一段（子分组与组内文件一起改名，成功 204）。 */
export function renameGroup(path: string, newName: string): Promise<void> {
  return request<void>("/api/groups/rename", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path, new_name: newName }),
  });
}

/** PUT /api/files/order：拖拽后按段内新顺序提交（0..n-1），成功 204。 */
export function reorderFiles(items: FileOrderItem[]): Promise<void> {
  return request<void>("/api/files/order", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ items }),
  });
}

/**
 * 用签名 URL 触发浏览器下载。
 *
 * 不能直接给 /api/files/{id}/download 加 href —— 导航带不了 Authorization 头；
 * 也不用 fetch + blob —— 大文件会整份进内存，而且 Safari 在异步回调里点 <a download>
 * 常常不生效。换成「先换签名 URL，再让浏览器下载」：流式、可续传、原生进度都在浏览器侧。
 *
 * 必须带 download 属性：否则浏览器把 /s/<id> 当成一次同帧导航，连续点多个文件时后一次导航
 * 会取消前一次下载（分组「全部下载」只剩最后一个）。download 为空串时文件名取服务端的
 * Content-Disposition，重命名后的名字仍然正确。
 */
function startDownload(url: string): void {
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = "";
  anchor.rel = "noopener";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
}

/** GET /api/files/{id}/download-url：换一个匿名下载地址（到下一个 UTC 整点失效）。 */
export async function downloadFile(id: string): Promise<void> {
  const payload = await request<DownloadURLResponse>(
    `/api/files/${encodeURIComponent(id)}/download-url`,
  );
  startDownload(payload.url);
}
