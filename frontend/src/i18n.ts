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
  | "nav.no-groups"
  | "nav.ungrouped"
  | "nav.search-placeholder"
  | "nav.file-count"
  | "nav.no-results"
  | "nav.empty-recent"
  | "nav.empty-favorites"
  | "upload.button"
  | "upload.drop-overlay"
  | "upload.queue-title"
  | "upload.dock-collapse"
  | "upload.dock-expand"
  | "upload.dock-items"
  | "upload.status.pending"
  | "upload.status.hashing"
  | "upload.status.checking"
  | "upload.status.uploading"
  | "upload.status.uploading-speed"
  | "upload.status.instant"
  | "upload.status.done"
  | "upload.status.failed"
  | "upload.status.cancelled"
  | "upload.error.too-big"
  | "upload.action.remove"
  | "upload.action.cancel"
  | "upload.action.retry"
  | "action.favorite"
  | "action.unfavorite"
  | "action.download"
  | "action.download-group-size"
  | "action.download-group-hint"
  | "action.stats"
  | "action.rename"
  | "action.rename-group"
  | "action.copy-link"
  | "action.copy-link-24h"
  | "action.copy-curl"
  | "action.copied"
  | "action.delete"
  | "action.move-group"
  | "action.more"
  | "action.upload-menu"
  | "action.new-group"
  | "action.group-menu"
  | "action.create-subgroup"
  | "action.delete-group"
  | "action.create"
  | "action.expand-group"
  | "action.collapse-group"
  | "action.cancel"
  | "action.close"
  | "action.logout"
  | "file.sha256"
  | "file.detail.title"
  | "file.detail.size"
  | "file.detail.time"
  | "file.detail.group"
  | "group.new-title"
  | "group.new-hint"
  | "group.rename-title"
  | "group.new-subgroup-title"
  | "group.delete-title"
  | "group.delete-message"
  | "group.delete-empty"
  | "group.delete-files"
  | "group.delete-files-hint"
  | "group.stats.title"
  | "group.stats.hint"
  | "group.stats.files"
  | "group.stats.file-count"
  | "group.stats.total-size"
  | "group.stats.distribution"
  | "group.stats.empty"
  | "rename.title"
  | "confirm.delete"
  | "meta.unknown-time"
  | "lang.label"
  | "state.loading"
  | "error.load"
  | "error.copy"
  | "error.network"
  | "storage.s3-detail"
  | "storage.local"
  | "storage.s3-hint"
  | "storage.local-hint"
  | "storage.unreachable"
  | "user.readonly"
  | "user.readonly-hint"
  | "nav.all"
  | "nav.tree"
  | "nav.resize-sidebar"
  | "column.name"
  | "column.size"
  | "column.uploaded"
  | "column.hash"
  | "column.actions"
  | "content.folder-count"
  | "content.item-count"
  | "content.empty-folder"
  | "content.empty-hint"
  | "content.search-results"
  | "content.drop-hint"
  | "content.search-count"
  | "content.folder-empty";

interface Message {
  readonly zh: string;
  readonly en: string;
}

const messages: Record<MessageKey, Message> = {
  "app.title": { zh: "文件分享", en: "File Share" },
  "app.tagline": { zh: "上传、整理、分享，一个页面搞定", en: "Upload, organize and share in one place" },
  "auth.hero-title": { zh: "把文件分享变简单", en: "File sharing, simplified" },
  "auth.point.instant": { zh: "SHA-256 秒传与去重，不浪费空间", en: "SHA-256 instant uploads with dedupe" },
  "auth.point.share": {
    zh: "临时签名链接，10 分钟内下载免登录",
    en: "Signed links valid for 10 minutes, no sign-in to download",
  },
  "auth.point.private": { zh: "默认私有，按用户分配读写权限", en: "Private by default with per-user read/write permissions" },
  "auth.title": { zh: "登录", en: "Sign in" },
  "auth.hint": { zh: "输入用户名与密码进入文件空间", en: "Sign in with your username and password" },
  "auth.username-placeholder": { zh: "用户名", en: "Username" },
  "auth.password-placeholder": { zh: "密码", en: "Password" },
  "auth.submit": { zh: "进入", en: "Continue" },
  "auth.error": { zh: "登录失败，请检查用户名与密码", en: "Sign-in failed, check your username and password" },
  "nav.recent": { zh: "最近", en: "Recent" },
  "nav.favorites": { zh: "收藏", en: "Favorites" },
  "nav.no-groups": { zh: "暂无文件夹", en: "No folders yet" },
  "nav.ungrouped": { zh: "未分组", en: "Ungrouped" },
  "nav.search-placeholder": { zh: "搜索文件名…", en: "Search files…" },
  "nav.file-count": { zh: "共 {count} 个文件", en: "{count} files" },
  "nav.no-results": { zh: "没有匹配的文件", en: "No matching files" },
  "nav.empty-recent": { zh: "还没有文件，上传第一个吧", en: "No files yet — upload your first one" },
  "nav.empty-favorites": { zh: "还没有收藏的文件", en: "No favorites yet" },
  "upload.button": { zh: "上传文件", en: "Upload files" },
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
  "upload.status.uploading-speed": {
    zh: "{percent}% · {loaded} / {total} · {speed}",
    en: "{percent}% · {loaded} / {total} · {speed}",
  },
  "upload.status.instant": { zh: "秒传成功", en: "Instant upload" },
  "upload.status.done": { zh: "上传完成", en: "Uploaded" },
  "upload.status.failed": { zh: "上传失败", en: "Upload failed" },
  "upload.status.cancelled": { zh: "已取消", en: "Cancelled" },
  "upload.error.too-big": {
    zh: "超过单文件上限 {max}",
    en: "Exceeds the {max} per-file limit",
  },
  "upload.action.remove": { zh: "移除", en: "Remove" },
  "upload.action.cancel": { zh: "取消", en: "Cancel" },
  "upload.action.retry": { zh: "重试", en: "Retry" },
  "action.favorite": { zh: "收藏", en: "Favorite" },
  "action.unfavorite": { zh: "取消收藏", en: "Unfavorite" },
  "action.download": { zh: "下载", en: "Download" },
  "action.download-group-size": { zh: "下载本目录文件（{size}）", en: "Download files here ({size})" },
  "action.download-group-hint": {
    zh: "只下载当前文件夹里的 {count} 个文件，不含子文件夹",
    en: "Only the {count} files directly in this folder, not subfolders",
  },
  "action.stats": { zh: "统计信息", en: "Statistics" },
  "action.rename": { zh: "重命名", en: "Rename" },
  "action.rename-group": { zh: "重命名文件夹", en: "Rename folder" },
  "action.copy-link": { zh: "复制分享链接（10 分钟）", en: "Copy share link (10 minutes)" },
  "action.copy-link-24h": { zh: "复制分享链接（24 小时）", en: "Copy share link (24 hours)" },
  "action.copy-curl": { zh: "复制 curl 命令", en: "Copy curl command" },
  "action.copied": { zh: "已复制", en: "Copied" },
  "action.delete": { zh: "删除", en: "Delete" },
  "action.move-group": { zh: "移动到文件夹", en: "Move to folder" },
  "action.more": { zh: "更多操作", en: "More actions" },
  "action.upload-menu": { zh: "上传与下载", en: "Upload and download" },
  "action.new-group": { zh: "新建文件夹", en: "New folder" },
  "action.group-menu": { zh: "文件夹操作", en: "Folder actions" },
  "action.create-subgroup": { zh: "创建子文件夹", en: "New sub-folder" },
  "action.delete-group": { zh: "删除文件夹", en: "Delete folder" },
  "action.create": { zh: "创建", en: "Create" },
  "action.expand-group": { zh: "展开子文件夹", en: "Expand sub-folders" },
  "action.collapse-group": { zh: "折叠子文件夹", en: "Collapse sub-folders" },
  "action.cancel": { zh: "取消", en: "Cancel" },
  "action.close": { zh: "关闭", en: "Close" },
  "action.logout": { zh: "退出登录", en: "Sign out" },
  "file.sha256": { zh: "SHA-256", en: "SHA-256" },
  "file.detail.title": { zh: "文件详情", en: "File details" },
  "file.detail.size": { zh: "大小", en: "Size" },
  "file.detail.time": { zh: "上传时间", en: "Uploaded" },
  "file.detail.group": { zh: "所在文件夹", en: "Folder" },
  "group.new-title": { zh: "新建文件夹", en: "New folder" },
  "group.new-hint": {
    zh: "输入文件夹名；用 父/子/孙 一次建多层，最多三级",
    en: "Folder name; use parent/child/grandchild for deeper levels",
  },
  "group.rename-title": { zh: "重命名文件夹", en: "Rename folder" },
  "group.new-subgroup-title": { zh: "新建子文件夹", en: "New sub-folder" },
  "group.delete-title": { zh: "删除文件夹「{name}」？", en: 'Delete folder "{name}"?' },
  "group.delete-empty": { zh: "这个文件夹里没有文件。", en: "This folder has no files." },
  "group.delete-message": {
    zh: "该文件夹及子文件夹共 {count} 个文件；不勾选时，文件会移到「{target}」。",
    en: '{count} files in this folder and its sub-folders. Leave unchecked to move them to "{target}".',
  },
  "group.delete-files": { zh: "同时删除这 {count} 个文件", en: "Also delete these {count} files" },
  "group.delete-files-hint": {
    zh: "这些文件会被永久删除，无法恢复。",
    en: "These files will be permanently deleted.",
  },
  "group.stats.title": { zh: "统计信息", en: "Statistics" },
  "group.stats.hint": { zh: "{path} · 含全部子文件夹", en: "{path} · including all sub-folders" },
  "group.stats.files": { zh: "文件数量", en: "Files" },
  "group.stats.file-count": { zh: "{count} 个", en: "{count}" },
  "group.stats.total-size": { zh: "总文件大小", en: "Total size" },
  "group.stats.distribution": { zh: "文件大小分布", en: "Size distribution" },
  "group.stats.empty": { zh: "该文件夹下没有文件", en: "No files in this folder" },
  "rename.title": { zh: "重命名文件", en: "Rename file" },
  "confirm.delete": { zh: "确定要删除“{name}”吗？", en: 'Delete "{name}"?' },
  "meta.unknown-time": { zh: "时间未知", en: "Unknown time" },
  "lang.label": { zh: "语言", en: "Language" },
  "state.loading": { zh: "加载中…", en: "Loading…" },
  "error.load": { zh: "加载文件列表失败", en: "Failed to load the file list" },
  "error.copy": {
    zh: "复制失败，请检查浏览器的剪贴板权限",
    en: "Failed to copy; check the browser clipboard permission",
  },
  "error.network": { zh: "网络异常", en: "Network error" },
  "storage.s3-detail": {
    zh: "对象存储 · {bucket}/{prefix}",
    en: "Object storage · {bucket}/{prefix}",
  },
  "storage.local": { zh: "本地磁盘", en: "Local disk" },
  "user.readonly": { zh: "只读", en: "read-only" },
  "user.readonly-hint": { zh: "当前账号只能浏览和下载", en: "This account can only browse and download" },
  "storage.s3-hint": {
    zh: "文件内容存放在 S3 对象存储中",
    en: "File contents are stored in S3 object storage",
  },
  "storage.unreachable": { zh: "（无法连接）", en: " (unreachable)" },
  "storage.local-hint": {
    zh: "文件内容存放在服务器本地磁盘",
    en: "File contents are stored on the server's local disk",
  },
  "nav.all": { zh: "全部", en: "All" },
  "nav.tree": { zh: "文件夹", en: "Folders" },
  "nav.resize-sidebar": { zh: "拖动调整侧栏宽度", en: "Drag to resize sidebar" },
  "column.name": { zh: "名称", en: "Name" },
  "column.size": { zh: "大小", en: "Size" },
  "column.uploaded": { zh: "上传时间", en: "Uploaded" },
  "column.hash": { zh: "指纹", en: "Hash" },
  "column.actions": { zh: "操作", en: "Actions" },
  "content.folder-count": { zh: "{count} 个文件夹", en: "{count} folders" },
  "content.item-count": { zh: "{count} 项", en: "{count} items" },
  "content.empty-folder": { zh: "这个文件夹是空的", en: "This folder is empty" },
  "content.empty-hint": {
    zh: "把文件拖到这里上传，或点右上角「上传文件」",
    en: "Drop files here, or use Upload files at the top right",
  },
  "content.search-results": { zh: "搜索结果", en: "Search results" },
  "content.drop-hint": {
    zh: "把文件拖到这里上传到当前文件夹；拖到左侧文件夹上则直接传入该文件夹",
    en: "Drop files here to upload into this folder, or onto a folder on the left",
  },
  "content.search-count": { zh: "{count} 个结果", en: "{count} results" },
  "content.folder-empty": { zh: "这个文件夹里还没有文件", en: "No files in this folder yet" },
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
