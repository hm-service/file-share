/**
 * 极简 i18n：键是点号分层的符号名，中英两栏同时给出。
 *
 * - 键集合用联合类型写死：`Record<MessageKey, Message>` 要求两栏逐键齐全，
 *   漏键 / 漏栏在 `pnpm typecheck` 时直接报错；`t()` 的参数也是 MessageKey，
 *   模板里写错键同样编译不过。
 * - 运行期查不到就原样回键名，不做回退，坏键在界面上一眼可见。
 * - 语言存 localStorage（file-share.locale），切换时同步 <html lang> 与标题；
 *   `t()` 读的是响应式 ref，模板会在切换后自动重渲染，不需要整页 reload。
 */
import { ref } from "vue";

const STORAGE_KEY = "file-share.locale";

export interface Language {
  readonly id: LocaleId;
  /** 语言自称，不翻译。 */
  readonly label: string;
  /** 写进 <html lang> 的 BCP 47 值。 */
  readonly htmlLang: string;
}

export type LocaleId = "zh-Hans" | "en";

export const LANGUAGES: readonly Language[] = [
  { id: "zh-Hans", label: "中文", htmlLang: "zh-CN" },
  { id: "en", label: "EN", htmlLang: "en" },
];

/** 所有界面文案的键；中英两栏都缺一不可。 */
export type MessageKey =
  | "app.title"
  | "app.tagline"
  | "auth.hero-title"
  | "auth.point.instant"
  | "auth.point.share"
  | "auth.point.private"
  | "auth.title"
  | "auth.hint"
  | "auth.token-placeholder"
  | "auth.submit"
  | "auth.error"
  | "nav.all-files"
  | "nav.favorites"
  | "nav.search-placeholder"
  | "nav.file-count"
  | "nav.no-results"
  | "nav.empty-all"
  | "nav.empty-favorites"
  | "upload.button"
  | "upload.hint"
  | "upload.hint-mobile"
  | "upload.drop-overlay"
  | "upload.queue-title"
  | "upload.dock-collapse"
  | "upload.dock-expand"
  | "upload.dock-items"
  | "upload.status.pending"
  | "upload.status.hashing"
  | "upload.status.checking"
  | "upload.status.uploading"
  | "upload.status.instant"
  | "upload.status.done"
  | "upload.status.failed"
  | "upload.status.cancelled"
  | "upload.action.remove"
  | "upload.action.cancel"
  | "upload.action.retry"
  | "action.favorite"
  | "action.unfavorite"
  | "action.download"
  | "action.copy-link"
  | "action.copy-link-private"
  | "action.copied"
  | "action.delete"
  | "action.make-public"
  | "action.make-private"
  | "action.logout"
  | "badge.public"
  | "confirm.delete"
  | "meta.unknown-time"
  | "lang.label"
  | "state.loading"
  | "error.load"
  | "error.network";

interface Message {
  readonly zh: string;
  readonly en: string;
}

const messages: Record<MessageKey, Message> = {
  "app.title": { zh: "文件分享", en: "File Share" },
  "app.tagline": { zh: "上传、整理、分享，一个页面搞定", en: "Upload, organize and share in one place" },
  "auth.hero-title": { zh: "把文件分享变简单", en: "File sharing, simplified" },
  "auth.point.instant": { zh: "SHA-256 秒传与去重，不浪费空间", en: "SHA-256 instant uploads with dedupe" },
  "auth.point.share": { zh: "文件公开后即可用直链分享", en: "Share public files with a direct link" },
  "auth.point.private": { zh: "默认私有，单个访问令牌控制", en: "Private by default, guarded by one token" },
  "auth.title": { zh: "登录", en: "Sign in" },
  "auth.hint": { zh: "输入访问令牌进入你的文件空间", en: "Enter the access token to open your space" },
  "auth.token-placeholder": { zh: "访问令牌", en: "Access token" },
  "auth.submit": { zh: "进入", en: "Continue" },
  "auth.error": { zh: "登录失败，请检查令牌", en: "Sign-in failed, check the token" },
  "nav.all-files": { zh: "全部文件", en: "All files" },
  "nav.favorites": { zh: "收藏", en: "Favorites" },
  "nav.search-placeholder": { zh: "搜索文件名…", en: "Search files…" },
  "nav.file-count": { zh: "共 {count} 个文件", en: "{count} files" },
  "nav.no-results": { zh: "没有匹配的文件", en: "No matching files" },
  "nav.empty-all": { zh: "还没有文件，上传第一个吧", en: "No files yet — upload your first one" },
  "nav.empty-favorites": { zh: "还没有收藏的文件", en: "No favorites yet" },
  "upload.button": { zh: "上传文件", en: "Upload files" },
  "upload.hint": { zh: "也可以把文件拖到右侧区域", en: "or drag files to this side" },
  "upload.hint-mobile": { zh: "点击上方按钮选择文件", en: "Tap the upload button above" },
  "upload.drop-overlay": { zh: "松开即可上传", en: "Drop to upload" },
  "upload.queue-title": { zh: "上传队列", en: "Upload queue" },
  "upload.dock-collapse": { zh: "收起", en: "Collapse" },
  "upload.dock-expand": { zh: "展开", en: "Expand" },
  "upload.dock-items": { zh: "{count} 项", en: "{count} items" },
  "upload.status.pending": { zh: "等待中", en: "Waiting" },
  "upload.status.hashing": { zh: "计算指纹 {percent}%", en: "Checksum {percent}%" },
  "upload.status.checking": { zh: "检查服务器…", en: "Checking server…" },
  "upload.status.uploading": {
    zh: "{percent}% · {loaded} / {total}",
    en: "{percent}% · {loaded} / {total}",
  },
  "upload.status.instant": { zh: "秒传成功", en: "Instant upload" },
  "upload.status.done": { zh: "上传完成", en: "Uploaded" },
  "upload.status.failed": { zh: "上传失败", en: "Upload failed" },
  "upload.status.cancelled": { zh: "已取消", en: "Cancelled" },
  "upload.action.remove": { zh: "移除", en: "Remove" },
  "upload.action.cancel": { zh: "取消", en: "Cancel" },
  "upload.action.retry": { zh: "重试", en: "Retry" },
  "action.favorite": { zh: "收藏", en: "Favorite" },
  "action.unfavorite": { zh: "取消收藏", en: "Unfavorite" },
  "action.download": { zh: "下载", en: "Download" },
  "action.copy-link": { zh: "复制直链", en: "Copy link" },
  "action.copy-link-private": { zh: "公开后才能复制直链", en: "Make it public to copy the link" },
  "action.copied": { zh: "已复制", en: "Copied" },
  "action.delete": { zh: "删除", en: "Delete" },
  "action.make-public": { zh: "设为公开", en: "Make public" },
  "action.make-private": { zh: "设为私有", en: "Make private" },
  "action.logout": { zh: "退出登录", en: "Sign out" },
  "badge.public": { zh: "公开", en: "Public" },
  "confirm.delete": { zh: "确定要删除“{name}”吗？", en: 'Delete "{name}"?' },
  "meta.unknown-time": { zh: "时间未知", en: "Unknown time" },
  "lang.label": { zh: "语言", en: "Language" },
  "state.loading": { zh: "加载中…", en: "Loading…" },
  "error.load": { zh: "加载文件列表失败", en: "Failed to load the file list" },
  "error.network": { zh: "网络异常", en: "Network error" },
};

/** 浏览器语言 / 存下来的值归一化；认不出来返回空串。 */
function normalizeLocale(value: string | null | undefined): LocaleId | "" {
  const text = String(value ?? "").toLowerCase();
  if (text.startsWith("en")) return "en";
  if (text.startsWith("zh")) return "zh-Hans";
  return "";
}

function detectLocale(): LocaleId {
  try {
    const saved = normalizeLocale(localStorage.getItem(STORAGE_KEY));
    if (saved) return saved;
  } catch {
    /* 隐私模式可能禁写 localStorage，忽略，用浏览器语言兜底 */
  }
  return normalizeLocale(navigator.language) || "zh-Hans";
}

/** 当前语言（响应式）。 */
export const locale = ref<LocaleId>(detectLocale());

/** 当前页标题对应的键；setLocale 之后要跟着换。 */
let titleKey: MessageKey = "app.title";

export function getLocale(): LocaleId {
  return locale.value;
}

export function currentLanguage(): Language {
  return LANGUAGES.find((language) => language.id === locale.value) ?? LANGUAGES[0];
}

/** 切语言：写 localStorage + 同步 <html lang> 与标题。 */
export function setLocale(next: LocaleId): void {
  locale.value = next;
  try {
    localStorage.setItem(STORAGE_KEY, next);
  } catch {
    /* 存不下就算了，本次会话里照常生效 */
  }
  document.documentElement.lang = currentLanguage().htmlLang;
  document.title = t(titleKey);
}

/** 页面挂载时调一次：把标题与 <html lang> 换成当前语言。 */
export function applyDocumentLocale(key: MessageKey = "app.title"): void {
  titleKey = key;
  document.documentElement.lang = currentLanguage().htmlLang;
  document.title = t(key);
}

/** 取文案：`{name}` 占位符逐个替换；未知键原样返回键名。 */
export function t(key: MessageKey, vars?: Record<string, string | number>): string {
  const message = (messages as Record<string, Message | undefined>)[key];
  if (!message) return key;
  let out = locale.value === "en" ? message.en : message.zh;
  if (vars) {
    for (const [name, value] of Object.entries(vars)) {
      out = out.split(`{${name}}`).join(String(value));
    }
  }
  return out;
}
