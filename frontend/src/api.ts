/**
 * 统一 API 客户端：Bearer 认证 + Accept-Language + 401 处理 + detail 解析。
 *
 * - 每个请求都带 `Authorization: Bearer <session>` 与 `Accept-Language`（后端按它挑错误文案）。
 * - 除登录接口外，401 一律清 session 跳 /auth（登录页自己把 401 显示成「令牌不正确」）。
 * - 错误体是 `{"detail": "..."}`，直接作为 ApiError.message（后端已本地化）。
 * - 下载必须带 Bearer：fetch 拿 blob → objectURL → 触发 <a download>，不能直接 location.href。
 */
import { abortError, isAbortError } from "./abort";
import { getLocale, t } from "./i18n";
import { getSession, toLogin } from "./session";
import type {
  AuthResponse,
  CheckFileResponse,
  FileItem,
  FileListResponse,
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

/** GET /api/files。 */
export async function listFiles(): Promise<FileItem[]> {
  const payload = await request<FileListResponse>("/api/files");
  return Array.isArray(payload?.files) ? payload.files : [];
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

/** POST /api/files/{id}/public：返回切换后的状态。 */
export function togglePublic(id: string): Promise<{ is_public: boolean }> {
  return request<{ is_public: boolean }>(`/api/files/${encodeURIComponent(id)}/public`, {
    method: "POST",
  });
}

/** DELETE /api/files/{id}（204，无响应体）。 */
export function deleteFile(id: string): Promise<void> {
  return request<void>(`/api/files/${encodeURIComponent(id)}`, { method: "DELETE" });
}

/** 公开直链（复制用）：同源 /s/{id}，匿名可下载。 */
export function publicLink(id: string): string {
  return `${location.origin}/s/${encodeURIComponent(id)}`;
}

/**
 * GET /api/files/{id}/download：带 Bearer 取回 blob，再触发浏览器下载。
 * 直接用 location.href 或 <a href> 不会带 Authorization 头，所以必须走 fetch。
 */
export async function downloadFile(id: string, filename: string): Promise<void> {
  let response: Response;
  try {
    response = await fetch(`/api/files/${encodeURIComponent(id)}/download`, {
      headers: authHeaders(),
    });
  } catch {
    throw new ApiError(t("error.network"), 0);
  }
  if (!response.ok) {
    const payload = parseJson(await response.text().catch(() => ""));
    if (response.status === 401) toLogin();
    throw new ApiError(detailOf(payload) || t("error.network"), response.status);
  }
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.rel = "noopener";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  // 立刻 revoke 在部分浏览器里会让下载还没开始就失效，推迟一拍再回收
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}
