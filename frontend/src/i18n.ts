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
  { id: "en", label: "English", htmlLang: "en" },
];

/** 所有界面文案的键，与实现契约的文案表逐键一致；两栏都缺一不可。 */
export type MessageKey =
  | "app.title"
  | "nav.favorites"
  | "nav.all-files"
  | "nav.empty-favorites"
  | "nav.empty-files"
  | "upload.drop-title"
  | "upload.drop-hint"
  | "upload.queue-title"
  | "upload.status.pending"
  | "upload.status.hashing"
  | "upload.status.checking"
  | "upload.status.uploading"
  | "upload.status.instant"
  | "upload.status.done"
  | "upload.status.failed"
  | "upload.action.remove"
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
  | "splitter.label"
  | "splitter.hint"
  | "confirm.delete"
  | "meta.unknown-time"
  | "auth.title"
  | "auth.hint"
  | "auth.token-placeholder"
  | "auth.submit"
  | "auth.error"
  | "lang.label"
  | "error.load"
  | "error.network";

interface Message {
  readonly zh: string;
  readonly en: string;
}

const messages: Record<MessageKey, Message> = {
  "app.title": { zh: "文件分享", en: "File Share" },
  "nav.favorites": { zh: "收藏", en: "Favorites" },
  "nav.all-files": { zh: "全部文件", en: "All Files" },
  "nav.empty-favorites": { zh: "暂无收藏", en: "No favorites yet" },
  "nav.empty-files": { zh: "库中暂无文件", en: "No files yet" },
  "upload.drop-title": { zh: "点击或将文件拖拽至此处", en: "Click or drag files here" },
  "upload.drop-hint": {
    zh: "支持超大文件，自动秒传校验",
    en: "Large files supported, instant upload checked",
  },
  "upload.queue-title": { zh: "上传队列", en: "Upload Queue" },
  "upload.status.pending": { zh: "等待中…", en: "Waiting…" },
  "upload.status.hashing": { zh: "本地计算指纹 {percent}%", en: "Computing checksum {percent}%" },
  "upload.status.checking": { zh: "校验服务器是否已存在…", en: "Checking the server…" },
  "upload.status.uploading": {
    zh: "上传中 {percent}% ({loaded} / {total})",
    en: "Uploading {percent}% ({loaded} / {total})",
  },
  "upload.status.instant": { zh: "秒传成功", en: "Instant upload succeeded" },
  "upload.status.done": { zh: "上传完成", en: "Upload complete" },
  "upload.status.failed": { zh: "上传失败", en: "Upload failed" },
  "upload.action.remove": { zh: "移除", en: "Remove" },
  "action.favorite": { zh: "收藏", en: "Favorite" },
  "action.unfavorite": { zh: "取消收藏", en: "Unfavorite" },
  "action.download": { zh: "下载", en: "Download" },
  "action.copy-link": { zh: "复制直链", en: "Copy link" },
  "action.copy-link-private": {
    zh: "公开后才能复制直链",
    en: "Make the file public to copy its link",
  },
  "action.copied": { zh: "已复制", en: "Copied" },
  "action.delete": { zh: "删除", en: "Delete" },
  "action.make-public": { zh: "设为公开", en: "Make public" },
  "action.make-private": { zh: "设为私有", en: "Make private" },
  "action.logout": { zh: "退出登录", en: "Sign out" },
  "splitter.label": { zh: "调整侧栏宽度", en: "Resize sidebar" },
  "splitter.hint": {
    zh: "拖拽或按左右方向键调整宽度，双击恢复默认",
    en: "Drag or use the arrow keys to resize; double-click to reset",
  },
  "confirm.delete": {
    zh: "确定要删除记录“{name}”吗？",
    en: "Delete the record “{name}”?",
  },
  "meta.unknown-time": { zh: "时间未知", en: "Unknown time" },
  "auth.title": { zh: "登录", en: "Sign in" },
  "auth.hint": { zh: "输入访问令牌以继续", en: "Enter the access token to continue" },
  "auth.token-placeholder": { zh: "访问令牌", en: "Access token" },
  "auth.submit": { zh: "登录", en: "Sign in" },
  "auth.error": { zh: "登录失败，请检查令牌", en: "Sign-in failed, check the token" },
  "lang.label": { zh: "语言", en: "Language" },
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
