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
  | "auth.username-placeholder"
  | "auth.password-placeholder"
  | "auth.submit"
  | "auth.error"
  | "nav.recent"
  | "nav.favorites"
  | "nav.all"
  | "nav.root"
  | "nav.tree"
  | "nav.resize-sidebar"
  | "nav.search-placeholder"
  | "nav.file-count"
  | "nav.no-results"
  | "nav.empty-recent"
  | "nav.empty-favorites"
  | "nav.no-groups"
  | "action.favorite"
  | "action.unfavorite"
  | "action.download"
  | "action.download-group-size"
  | "action.download-group-hint"
  | "action.copy-link"
  | "action.copy-link-24h"
  | "action.copied"
  | "action.more"
  | "action.expand-group"
  | "action.collapse-group"
  | "action.cancel"
  | "action.close"
  | "action.logout"
  | "column.name"
  | "column.size"
  | "column.uploaded"
  | "column.actions"
  | "file.detail.title"
  | "file.detail.size"
  | "file.detail.time"
  | "file.detail.group"
  | "file.detail.path"
  | "meta.unknown-time"
  | "lang.label"
  | "state.loading"
  | "state.pull-down"
  | "state.pull-release"
  | "state.refreshing"
  | "error.load"
  | "error.copy"
  | "error.network"
  | "storage.readonly"
  | "storage.readonly-hint"
  | "storage.unreachable"
  | "user.readonly"
  | "content.folder-count"
  | "content.item-count"
  | "content.empty-folder"
  | "content.empty-hint"
  | "content.search-results"
  | "content.search-count";

interface Message {
  readonly zh: string;
  readonly en: string;
}

const messages: Record<MessageKey, Message> = {
  "app.title": { zh: "文件分享", en: "File Share" },
  "app.tagline": { zh: "浏览、搜索、下载，一个页面搞定", en: "Browse, search and download in one place" },
  "auth.hero-title": { zh: "文件随取随用", en: "Your files, always at hand" },
  "auth.point.instant": { zh: "按目录整理，找文件更快", en: "Organized by folders for quick browsing" },
  "auth.point.share": {
    zh: "临时签名链接，10 分钟内下载免登录",
    en: "Signed links valid for 10 minutes, no sign-in to download",
  },
  "auth.point.private": {
    zh: "只读访问，浏览与下载都安全",
    en: "Read-only access keeps browsing and downloads safe",
  },
  "auth.title": { zh: "登录", en: "Sign in" },
  "auth.hint": { zh: "输入用户名与密码查看文件", en: "Sign in with your username and password" },
  "auth.username-placeholder": { zh: "用户名", en: "Username" },
  "auth.password-placeholder": { zh: "密码", en: "Password" },
  "auth.submit": { zh: "进入", en: "Continue" },
  "auth.error": { zh: "登录失败，请检查用户名与密码", en: "Sign-in failed, check your username and password" },
  "nav.recent": { zh: "最近", en: "Recent" },
  "nav.favorites": { zh: "收藏", en: "Favorites" },
  "nav.all": { zh: "全部", en: "All" },
  "nav.root": { zh: "根目录", en: "Root" },
  "nav.tree": { zh: "文件夹", en: "Folders" },
  "nav.resize-sidebar": { zh: "拖动调整侧栏宽度", en: "Drag to resize sidebar" },
  "nav.search-placeholder": { zh: "搜索文件名…", en: "Search files…" },
  "nav.file-count": { zh: "共 {count} 个文件", en: "{count} files" },
  "nav.no-results": { zh: "没有匹配的文件", en: "No matching files" },
  "nav.empty-recent": { zh: "还没有文件", en: "No files yet" },
  "nav.empty-favorites": { zh: "还没有收藏的文件", en: "No favorites yet" },
  "nav.no-groups": { zh: "暂无文件夹", en: "No folders yet" },
  "action.favorite": { zh: "收藏", en: "Favorite" },
  "action.unfavorite": { zh: "取消收藏", en: "Unfavorite" },
  "action.download": { zh: "下载", en: "Download" },
  "action.download-group-size": { zh: "下载全部（{size}）", en: "Download all ({size})" },
  "action.download-group-hint": {
    zh: "下载当前视图里的 {count} 个文件",
    en: "Download the {count} files in this view",
  },
  "action.copy-link": { zh: "复制分享链接（10 分钟）", en: "Copy share link (10 minutes)" },
  "action.copy-link-24h": { zh: "复制分享链接（24 小时）", en: "Copy share link (24 hours)" },
  "action.copied": { zh: "已复制", en: "Copied" },
  "action.more": { zh: "更多操作", en: "More actions" },
  "action.expand-group": { zh: "展开子文件夹", en: "Expand sub-folders" },
  "action.collapse-group": { zh: "折叠子文件夹", en: "Collapse sub-folders" },
  "action.cancel": { zh: "取消", en: "Cancel" },
  "action.close": { zh: "关闭", en: "Close" },
  "action.logout": { zh: "退出登录", en: "Sign out" },
  "column.name": { zh: "名称", en: "Name" },
  "column.size": { zh: "大小", en: "Size" },
  "column.uploaded": { zh: "修改时间", en: "Modified" },
  "column.actions": { zh: "操作", en: "Actions" },
  "file.detail.title": { zh: "文件详情", en: "File details" },
  "file.detail.size": { zh: "大小", en: "Size" },
  "file.detail.time": { zh: "修改时间", en: "Modified" },
  "file.detail.group": { zh: "所在目录", en: "Folder" },
  "file.detail.path": { zh: "相对路径", en: "Relative path" },
  "meta.unknown-time": { zh: "时间未知", en: "Unknown time" },
  "lang.label": { zh: "语言", en: "Language" },
  "state.loading": { zh: "加载中…", en: "Loading…" },
  "state.pull-down": { zh: "下拉刷新", en: "Pull to refresh" },
  "state.pull-release": { zh: "松开刷新", en: "Release to refresh" },
  "state.refreshing": { zh: "正在刷新…", en: "Refreshing…" },
  "error.load": { zh: "加载文件列表失败", en: "Failed to load the file list" },
  "error.copy": {
    zh: "复制失败，请检查浏览器的剪贴板权限",
    en: "Failed to copy; check the browser clipboard permission",
  },
  "error.network": { zh: "网络异常", en: "Network error" },
  "storage.readonly": { zh: "只读目录", en: "Read-only folder" },
  "storage.readonly-hint": {
    zh: "文件内容来自 SMB 共享的只读目录",
    en: "File contents come from a read-only SMB share",
  },
  "storage.unreachable": { zh: "（无法访问）", en: " (unreachable)" },
  "user.readonly": { zh: "只读", en: "read-only" },
  "content.folder-count": { zh: "{count} 个文件夹", en: "{count} folders" },
  "content.item-count": { zh: "{count} 项", en: "{count} items" },
  "content.empty-folder": { zh: "这个文件夹是空的", en: "This folder is empty" },
  "content.empty-hint": { zh: "换成别的目录或搜索文件名试试", en: "Try another folder or search for a file name" },
  "content.search-results": { zh: "搜索结果", en: "Search results" },
  "content.search-count": { zh: "{count} 个结果", en: "{count} results" },
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