/**
 * 统一 API 客户端：Bearer 认证 + Accept-Language + 401 处理 + detail 解析。
 *
 * - 每个请求都带 `Authorization: Bearer <session>` 与 `Accept-Language`（后端按它挑错误文案）。
 * - 除登录接口外，401 一律清 session 跳 /auth（登录页把 401 显示成用户名或密码错误）。
 * - 错误体是 `{"detail": "..."}`，直接作为 ApiError.message（后端已本地化）。
 * - 下载不能直接导航到需要 Bearer 的接口：先换一个签名 URL，再让浏览器导航到该地址，
 *   由浏览器完成流式下载（Range / 原生进度都在浏览器侧）。
 */
import { getLocale, t } from "./i18n";
import { getSession, toLogin } from "./session";
import type {
  AuthResponse,
  DownloadURLResponse,
  FileListResponse,
  MeResponse,
  StorageInfo,
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
  } catch {
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

/** POST /api/auth：成功返回会话与用户信息，失败抛 ApiError（401 = 用户名或密码不对）。 */
export function login(username: string, password: string): Promise<AuthResponse> {
  return request<AuthResponse>("/api/auth", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
    redirectOn401: false,
  });
}

/** GET /api/me：当前登录用户与权限。 */
export function getMe(): Promise<MeResponse> {
  return request<MeResponse>("/api/me");
}

/** GET /api/files：只读目录里的全部文件与分组。 */
export async function listFiles(): Promise<FileListResponse> {
  const payload = await request<FileListResponse>("/api/files");
  return {
    files: Array.isArray(payload?.files) ? payload.files : [],
    groups: Array.isArray(payload?.groups) ? payload.groups : [],
  };
}

/** PUT/DELETE /api/favorites?path=：切换收藏，返回切换后的状态。 */
export function toggleFavorite(path: string, favorite: boolean): Promise<{ is_favorite: boolean }> {
  return request<{ is_favorite: boolean }>(`/api/favorites?path=${encodeURIComponent(path)}`, {
    method: favorite ? "PUT" : "DELETE",
  });
}

/** 分享链接的有效期（秒）：默认 10 分钟，也可以签 24 小时档，后端只认这两档。 */
export type ShareLinkTTL = 600 | 86400;

/** POST /api/share：签一条 ttl 内有效的分享链接，返回可直接发给对方的相对地址。 */
export function createShareLink(path: string, ttl: ShareLinkTTL = 600): Promise<{ url: string }> {
  return request<{ url: string }>("/api/share", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path, ttl: ttl === 86400 ? "24h" : "10m" }),
  });
}

/**
 * 用签名 URL 触发浏览器下载。
 *
 * 不能直接给 /api/download 加 href —— 导航带不了 Authorization 头；
 * 也不用 fetch + blob —— 大文件会整份进内存，而且 Safari 在异步回调里点 <a download>
 * 常常不生效。换成「先换签名 URL，再让浏览器下载」：流式、可续传、原生进度都在浏览器侧。
 *
 * 必须带 download 属性：否则浏览器把 /s/<path> 当成一次同帧导航，连续点多个文件时后一次导航
 * 会取消前一次下载（分组「全部下载」只剩最后一个）。download 为空串时文件名取服务端的
 * Content-Disposition。
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

/** GET /api/storage：只读数据目录与收藏元数据的状态。 */
export function getStorageInfo(): Promise<StorageInfo> {
  return request<StorageInfo>("/api/storage");
}

/** GET /api/download-url?path=：换一个匿名下载地址（10 分钟内有效）并触发浏览器下载。 */
export async function downloadFile(path: string): Promise<void> {
  const payload = await request<DownloadURLResponse>(
    `/api/download-url?path=${encodeURIComponent(path)}`,
  );
  startDownload(payload.url);
}