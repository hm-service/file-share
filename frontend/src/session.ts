/**
 * 会话存取：一个不透明 token + 过期时间，存在 localStorage（key: file-share.session）。
 *
 * 后端是无状态 HMAC 会话，没有 logout 接口，所以「退出登录」= 清掉本地会话回 /auth。
 * 401 时同样走这里清掉，避免拿一个已失效的 token 反复请求。
 */
import type { Session } from "./types";

const STORAGE_KEY = "file-share.session";

function parseSession(raw: string | null): Session | null {
  if (!raw) return null;
  try {
    const value = JSON.parse(raw) as Partial<Session> | null;
    if (!value || typeof value.token !== "string" || !value.token) return null;
    if (typeof value.expires_at !== "number" || !Number.isFinite(value.expires_at)) return null;
    return { token: value.token, expires_at: value.expires_at };
  } catch {
    return null;
  }
}

/** 读会话；已过期就顺手清掉并返回 null。 */
export function getSession(): Session | null {
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(STORAGE_KEY);
  } catch {
    /* 隐私模式下 localStorage 不可用，当作未登录 */
    return null;
  }
  const session = parseSession(raw);
  if (!session) return null;
  if (session.expires_at * 1000 <= Date.now()) {
    clearSession();
    return null;
  }
  return session;
}

export function saveSession(session: Session): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(session));
  } catch {
    /* 存不下就只能本次内存里有效；接口调用会拿不到 token 再被 401 送回登录页 */
  }
}

export function clearSession(): void {
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    /* 同上 */
  }
}

/** 主页面入口用：没有有效会话就跳 /auth，返回是否已登录。 */
export function requireSession(): boolean {
  if (getSession()) return true;
  location.replace("/auth");
  return false;
}

/** 退出登录 / 掉登录态：清会话回登录页。 */
export function toLogin(): void {
  clearSession();
  location.replace("/auth");
}
