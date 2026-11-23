<script setup lang="ts">
/**
 * 列表里的一行文件：类型图标 + 名称/大小/时间 + 操作区，单行排布。
 *
 * 文件名是详情入口：点开 FileDetailDialog 看大小、时间与所在目录并直接下载。
 * 行内只留「下载、分享链接、收藏、更多」四个按钮：分享链接单独开按钮（默认 10 分钟档），
 * 「更多」菜单里再给一档 24 小时有效的分享链接。
 *
 * 分享链接先向服务端签一个地址，再复制带签名的绝对地址：签名请求中按钮禁用，
 * 成功后短暂显示勾；签名失败交给父组件统一提示。
 * navigator.clipboard 在 HTTP 部署下不存在或不可写，所以有 textarea + execCommand 兜底。
 *
 * 菜单用 Teleport 挂到 body：列表卡片有 overflow 裁剪，绝对定位的下拉菜单会被切掉；
 * 菜单定位按按钮 rect 计算并夹在视口内，滚动 / 缩放 / Esc / 点外面都关闭（菜单自身滚动除外）。
 */
import { computed, nextTick, onBeforeUnmount, ref } from "vue";

import { createShareLink, type ShareLinkTTL } from "@/api";
import { formatSize, formatTime } from "@/format";
import { ICONS, typeIcons, type FileKind } from "@/icons";
import { t } from "@/i18n";
import type { FileItem } from "@/types";

import FileDetailDialog from "./FileDetailDialog.vue";

const props = defineProps<{
  file: FileItem;
  /** 在元信息行里额外显示所属目录（「最近」视图用）。 */
  showGroup?: boolean;
  /** 当前账号是否有收藏权限（fav）：没有就不显示星标按钮。 */
  canFavorite: boolean;
}>();

const emit = defineEmits<{
  favorite: [];
  download: [];
  error: [error: unknown];
}>();

/** 按扩展名挑选文件类型图标 + 配色，让长列表更容易用图标与颜色扫读。 */
const kind = computed<FileKind>(() => {
  const extension = props.file.filename.split(".").pop()?.toLowerCase() ?? "";
  if (["jpg", "jpeg", "png", "gif", "webp", "svg", "bmp", "heic", "avif", "ico", "tiff"].includes(extension)) {
    return "image";
  }
  if (["mp4", "mov", "mkv", "webm", "avi", "m4v", "mpeg", "mpg"].includes(extension)) return "video";
  if (["mp3", "wav", "flac", "aac", "ogg", "m4a", "opus", "wma"].includes(extension)) return "audio";
  if (["zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "xz", "zst"].includes(extension)) return "archive";
  if (extension === "pdf") return "pdf";
  if (["doc", "docx", "odt", "rtf", "pages"].includes(extension)) return "doc";
  if (["xls", "xlsx", "csv", "tsv", "ods", "numbers"].includes(extension)) return "sheet";
  if (["ppt", "pptx", "odp", "key"].includes(extension)) return "slides";
  if (
    [
      "json", "yaml", "yml", "toml", "ini", "conf", "js", "mjs", "cjs", "ts", "tsx", "jsx",
      "go", "py", "rs", "sh", "bash", "zsh", "css", "scss", "less", "html", "htm", "xml",
      "sql", "c", "cpp", "h", "java", "kt", "swift", "rb", "php", "lua",
    ].includes(extension)
  ) {
    return "code";
  }
  if (["txt", "md", "markdown", "log"].includes(extension)) return "text";
  return "other";
});

/* ---------- 详情面板 ---------- */

const detailOpen = ref(false);
const nameEl = ref<HTMLButtonElement | null>(null);

/** 关掉面板把焦点还给文件名，键盘用户不会掉回页面开头。 */
function closeDetail(): void {
  detailOpen.value = false;
  void nextTick(() => nameEl.value?.focus());
}

function downloadFromDetail(): void {
  detailOpen.value = false;
  emit("download");
}

/* ---------- 分享链接 ---------- */

const sharing = ref(false);
const shared = ref(false);
let sharedTimer = 0;

/** 复制成功返回 true；失败不弹窗，只是不显示勾。 */
async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* 非安全上下文（纯 HTTP）或权限被拒：走下面的兜底 */
  }
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.setAttribute("readonly", "");
    textarea.style.position = "fixed";
    textarea.style.top = "-1000px";
    textarea.style.opacity = "0";
    document.body.appendChild(textarea);
    textarea.select();
    textarea.setSelectionRange(0, text.length);
    const ok = document.execCommand("copy");
    textarea.remove();
    return ok;
  } catch {
    return false;
  }
}

/**
 * 签一个 ttl 内有效的分享链接，返回可直接发给对方的绝对地址。
 * 接口给的是同源相对地址（/s/<path>?sig=…&exp=…），拼上 origin 复制给别人才能直接打开。
 */
async function shareUrlOrReport(ttl: ShareLinkTTL): Promise<string | null> {
  sharing.value = true;
  try {
    const payload = await createShareLink(props.file.id, ttl);
    return new URL(payload.url, location.origin).href;
  } catch (error) {
    emit("error", error);
    return null;
  } finally {
    sharing.value = false;
  }
}

/** 行内分享：签 10 分钟档 → 复制绝对地址 → 短暂显示勾。 */
async function shareLink(ttl: ShareLinkTTL = 600): Promise<void> {
  if (sharing.value) return;
  const url = await shareUrlOrReport(ttl);
  if (url === null) return;
  if (!(await copyText(url))) {
    emit("error", new Error(t("error.copy")));
    return;
  }
  shared.value = true;
  window.clearTimeout(sharedTimer);
  sharedTimer = window.setTimeout(() => {
    shared.value = false;
  }, 1200);
}

/* ---------- 「更多」菜单 ---------- */

// 宽高与 style.css 里 .row-menu 的 width / max-height 保持一致：这里先按上限估位置，
// 渲染完成后再用真实高度校正一次（见 adjustMenuPosition）。
const MENU = { width: 264, maxHeight: 440 };

const menuOpen = ref(false);
const menuEl = ref<HTMLElement | null>(null);
const menuStyle = ref<{ left: string; top: string }>({ left: "0px", top: "0px" });
let triggerEl: HTMLElement | null = null;

function unbindMenuEvents(): void {
  window.removeEventListener("pointerdown", onOutsidePointer, true);
  window.removeEventListener("keydown", onMenuKeydown, true);
  window.removeEventListener("resize", closeMenu);
  window.removeEventListener("scroll", onWindowScroll, true);
}

/**
 * 页面或列表滚动时关掉菜单（菜单是 fixed 定位，锚点会跑偏）。
 * 菜单内部的滚动不算：capture 阶段能收到子元素的 scroll，直接 close 会让菜单一滚就消失。
 */
function onWindowScroll(event: Event): void {
  const target = event.target;
  if (target instanceof Node && menuEl.value?.contains(target)) return;
  closeMenu();
}

function closeMenu(): void {
  if (!menuOpen.value) return;
  menuOpen.value = false;
  unbindMenuEvents();
}

function onOutsidePointer(event: PointerEvent): void {
  const target = event.target;
  if (!(target instanceof Node)) return;
  if (menuEl.value?.contains(target) || triggerEl?.contains(target)) return;
  closeMenu();
}

function onMenuKeydown(event: KeyboardEvent): void {
  if (event.key !== "Escape") return;
  event.stopPropagation();
  closeMenu();
}

/** 菜单按上限估的 top 可能偏高（内容比上限矮时），渲染后按真实高度夹回视口内。 */
function adjustMenuPosition(): void {
  const el = menuEl.value;
  if (!el) return;
  const rect = el.getBoundingClientRect();
  const overflow = rect.bottom - (window.innerHeight - 8);
  if (overflow <= 0) return;
  menuStyle.value = {
    left: menuStyle.value.left,
    top: `${Math.max(8, rect.top - overflow)}px`,
  };
}

/** 点按钮打开菜单：贴着触发按钮右下角，夹在视口内。 */
function toggleMenu(event: MouseEvent): void {
  if (menuOpen.value) {
    closeMenu();
    return;
  }
  unbindMenuEvents();
  const trigger = event.currentTarget as HTMLElement;
  triggerEl = trigger;
  const rect = trigger.getBoundingClientRect();
  const left = Math.min(
    Math.max(8, rect.right - MENU.width),
    Math.max(8, window.innerWidth - MENU.width - 8),
  );
  let top = rect.bottom + 6;
  if (top + MENU.maxHeight > window.innerHeight - 8) {
    top = Math.max(8, window.innerHeight - MENU.maxHeight - 8);
  }
  menuStyle.value = { left: `${left}px`, top: `${top}px` };
  menuOpen.value = true;
  window.addEventListener("pointerdown", onOutsidePointer, true);
  window.addEventListener("keydown", onMenuKeydown, true);
  window.addEventListener("resize", closeMenu);
  window.addEventListener("scroll", onWindowScroll, true);
  void nextTick(adjustMenuPosition);
}

/** 「更多」菜单里的分享链接：关菜单后走与行内按钮同一个签名复制流程，可选 10 分钟或 24 小时档。 */
function chooseShareLink(ttl: ShareLinkTTL): void {
  closeMenu();
  void shareLink(ttl);
}

onBeforeUnmount(() => {
  window.clearTimeout(sharedTimer);
  closeMenu();
});
</script>

<template>
  <li class="file-row" :data-id="file.id">
    <div class="cell-name">
      <span class="file-tile" :class="`kind-${kind}`" v-html="typeIcons[kind]"></span>
      <button
        ref="nameEl"
        type="button"
        class="file-name"
        :title="file.filename"
        aria-haspopup="dialog"
        @click="detailOpen = true"
      >
        {{ file.filename }}
      </button>
      <!-- 「最近」视图跨目录混排，行内补一个目录标签，免得看不出归属 -->
      <span v-if="showGroup" class="file-group" :title="file.group_path || t('nav.root')">
        <span class="file-group-icon" v-html="ICONS.folder"></span>
        <span class="file-group-name">{{ file.group_path || t("nav.root") }}</span>
      </span>
    </div>

    <span class="cell-size">{{ formatSize(file.size) }}</span>
    <span class="cell-time">{{ formatTime(file.created_at) }}</span>

    <div class="cell-actions">
      <button
        type="button"
        class="icon-btn download"
        :title="t('action.download')"
        :aria-label="t('action.download')"
        v-html="ICONS.download"
        @click="emit('download')"
      ></button>

      <!-- 分享链接独立成按钮：签 10 分钟档的分享地址并复制绝对地址，请求中禁用 -->
      <button
        type="button"
        class="icon-btn link"
        :class="{ on: shared }"
        :title="t('action.copy-link')"
        :aria-label="t('action.copy-link')"
        :disabled="sharing"
        v-html="shared ? ICONS.check : ICONS.link"
        @click="shareLink(600)"
      ></button>

      <button
        v-if="canFavorite"
        type="button"
        class="icon-btn star"
        :class="{ on: file.is_favorite }"
        :title="file.is_favorite ? t('action.unfavorite') : t('action.favorite')"
        :aria-label="file.is_favorite ? t('action.unfavorite') : t('action.favorite')"
        v-html="ICONS.star"
        @click="emit('favorite')"
      ></button>

      <button
        type="button"
        class="icon-btn more"
        :class="{ on: menuOpen }"
        :title="t('action.more')"
        :aria-label="t('action.more')"
        aria-haspopup="menu"
        :aria-expanded="menuOpen"
        v-html="ICONS.more"
        @click="toggleMenu"
      ></button>
    </div>

    <Teleport to="body">
      <div
        v-if="menuOpen"
        ref="menuEl"
        class="row-menu"
        :style="menuStyle"
        role="menu"
        :aria-label="t('action.more')"
      >
        <button
          type="button"
          class="row-menu-item"
          role="menuitem"
          :disabled="sharing"
          @click="chooseShareLink(600)"
        >
          <span class="row-menu-icon" v-html="shared ? ICONS.check : ICONS.link"></span>
          <span class="row-menu-label">{{ shared ? t("action.copied") : t("action.copy-link") }}</span>
        </button>

        <button
          type="button"
          class="row-menu-item"
          role="menuitem"
          :disabled="sharing"
          @click="chooseShareLink(86400)"
        >
          <span class="row-menu-icon" v-html="ICONS.clock"></span>
          <span class="row-menu-label">{{ t("action.copy-link-24h") }}</span>
        </button>
      </div>
    </Teleport>

    <FileDetailDialog
      v-if="detailOpen"
      :file="file"
      @close="closeDetail"
      @download="downloadFromDetail"
    />
  </li>
</template>

<style scoped>
/* 文件行：与表头 / 文件夹行共用同一套列宽变量（由 ContentTable 在 .table 上给出）。
   列：名称 | 大小 | 修改时间 | 操作 */
.file-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) var(--col-size, 110px) var(--col-time, 140px)
    var(--col-actions, 190px);
  align-items: center;
  min-height: 52px;
  padding: 0 14px 0 6px;
  border-top: 1px solid var(--line);
  background: var(--surface);
  list-style: none;
}

.file-row:first-child {
  border-top: 0;
}

.file-row:hover {
  background: var(--surface-2);
}

.cell-name {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  padding-left: 6px;
}

.file-tile {
  width: 30px;
  height: 30px;
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  background: var(--surface-2);
  color: var(--ink-2);
}

.file-tile:deep(svg) {
  width: 18px;
  height: 18px;
}

.kind-image { color: #8b5cf6; background: #f3efff; }
.kind-video { color: #e11d48; background: #ffeef2; }
.kind-audio { color: #db2777; background: #fdf2f8; }
.kind-archive { color: #d97706; background: #fff7ed; }
.kind-pdf { color: #dc2626; background: #fef2f2; }
.kind-doc { color: #2563eb; background: #eff6ff; }
.kind-sheet { color: #059669; background: #ecfdf5; }
.kind-slides { color: #ea580c; background: #fff7ed; }
.kind-code { color: #4f46e5; background: #eef2ff; }
.kind-text { color: #475569; background: #f1f5f9; }
.kind-other { color: #64748b; background: #f1f5f9; }

.file-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  border: 0;
  background: none;
  color: var(--ink);
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.file-name:hover {
  color: var(--accent);
  text-decoration: underline;
}

.file-name:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
  border-radius: 4px;
}

.file-group {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex: 0 0 auto;
  max-width: 200px;
  padding: 2px 7px;
  border-radius: 999px;
  background: var(--surface-2);
  color: var(--ink-3);
  font-size: 11.5px;
}

.file-group-icon {
  display: inline-flex;
}

.file-group-icon:deep(svg) {
  width: 12px;
  height: 12px;
}

.file-group-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cell-size,
.cell-time {
  color: var(--ink-3);
  font-size: 13px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.cell-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 1px;
}

.icon-btn {
  width: 30px;
  height: 30px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  color: var(--ink-3);
  transition: background 0.12s ease, color 0.12s ease;
}

.icon-btn:deep(svg) {
  width: 17px;
  height: 17px;
}

.icon-btn:hover {
  background: var(--accent-soft);
  color: var(--accent);
}

.icon-btn.star.on {
  color: var(--amber);
}

.icon-btn.link.on {
  color: var(--green);
}

.icon-btn.more.on {
  background: var(--accent-soft);
  color: var(--accent);
}

.icon-btn:disabled {
  opacity: 0.45;
}

@media (max-width: 900px) {
  .file-row {
    grid-template-columns: minmax(0, 1fr) var(--col-actions, 100px);
  }

  .cell-size,
  .cell-time {
    display: none;
  }

  .file-group {
    display: none;
  }

  /* 竖屏把分享入口收进「更多」菜单，操作列只留下载 / 收藏 / 更多 */
  .cell-actions .icon-btn.link {
    display: none;
  }
}
</style>