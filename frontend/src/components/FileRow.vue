<script setup lang="ts">
/**
 * 列表里的一行文件：拖拽把手 + 类型图标块 + 名称/元信息 + 操作区。
 *
 * 行内只留「下载、分享链接、收藏、分组、更多」五个按钮：分享链接与分组都是高频操作，
 * 单独开按钮；重命名、复制 curl 命令、删除收进最右的「更多」菜单，避免一行里挤满图标。
 *
 * 分享链接与 curl 命令都先向服务端签一个 10 分钟有效的分享地址，再复制带签名的绝对地址：
 * 签名请求中按钮禁用，成功后短暂显示勾；签名失败交给父组件统一提示。
 * navigator.clipboard 在 HTTP 部署下不存在或不可写，所以有 textarea + execCommand 兜底。
 *
 * 菜单用 Teleport 挂到 body：列表卡片有 overflow 裁剪，绝对定位的下拉菜单会被切掉；
 * 菜单定位按按钮 rect 计算并夹在视口内，滚动/缩放/Esc/点外面都关闭（菜单自身滚动除外）。
 */
import { computed, nextTick, onBeforeUnmount, ref } from "vue";

import { createShareLink } from "@/api";
import { formatSize, formatTime } from "@/format";
import { ICONS, typeIcons, type FileKind } from "@/icons";
import { t } from "@/i18n";
import type { FileItem, GroupItem } from "@/types";

const props = defineProps<{
  file: FileItem;
  /** 全部分组（含空分组），用于「移动到分组」列表。 */
  groups: readonly GroupItem[];
  /** 可拖拽排序的列表才显示把手；搜索过滤时顺序不可提交，把手隐藏。 */
  draggable: boolean;
}>();

const emit = defineEmits<{
  favorite: [];
  download: [];
  rename: [];
  error: [error: unknown];
  delete: [];
  "move-group": [groupPath: string];
  /** 「移动到分组」菜单里新建分组：弹窗由父组件统一负责。 */
  "new-group": [];
}>();

/** 顶级分组，以及某个分组下的子分组：菜单按两级缩进展示。 */
const topGroups = computed(() => props.groups.filter((group) => group.parent === ""));
function childrenOf(path: string): GroupItem[] {
  return props.groups.filter((group) => group.parent === path);
}

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
      "json",
      "yaml",
      "yml",
      "toml",
      "ini",
      "conf",
      "js",
      "mjs",
      "cjs",
      "ts",
      "tsx",
      "jsx",
      "go",
      "py",
      "rs",
      "sh",
      "bash",
      "zsh",
      "css",
      "scss",
      "less",
      "html",
      "htm",
      "xml",
      "sql",
      "c",
      "cpp",
      "h",
      "java",
      "kt",
      "swift",
      "rb",
      "php",
      "lua",
    ].includes(extension)
  ) {
    return "code";
  }
  if (["txt", "md", "markdown", "log"].includes(extension)) return "text";
  return "other";
});

/* ---------- 分享链接 / curl 命令 ---------- */

/** 签名状态：请求中禁用行内按钮与 curl 菜单项，成功后短暂显示勾 / 已复制。 */
const sharing = ref(false);
const shared = ref(false);
let sharedTimer = 0;

/** 「更多」菜单里的 curl 项：复制成功后短暂显示「已复制」。 */
const curlCopied = ref(false);
let curlCopiedTimer = 0;

/** 复制成功返回 true；失败不弹窗，只是不显示勾 / 「已复制」。 */
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

/** 单引号包住一个 shell 参数：值里的单引号写成 '\'' 收尾再续，其余字符原样保留。 */
function shellQuote(value: string): string {
  return `'${value.split("'").join(`'\\''`)}'`;
}

/**
 * 签一个 10 分钟有效的分享链接，返回可直接发给对方的绝对地址。
 * 接口给的是同源相对地址（/s/{id}?sig=…&exp=…），拼上 origin 复制给别人才能直接打开。
 */
async function shareUrl(): Promise<string> {
  const payload = await createShareLink(props.file.id);
  return new URL(payload.url, location.origin).href;
}

/** 签名失败交给父组件提示；签名成功后按钮立即恢复，不占着禁用态等剪贴板。 */
async function shareUrlOrReport(): Promise<string | null> {
  sharing.value = true;
  try {
    return await shareUrl();
  } catch (error) {
    emit("error", error);
    return null;
  } finally {
    sharing.value = false;
  }
}

/** 行内分享：签分享链接 → 复制绝对地址 → 短暂显示勾。 */
async function shareLink(): Promise<void> {
  if (sharing.value) return;
  const url = await shareUrlOrReport();
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

/**
 * curl 命令：`-f` 让 4xx / 5xx 直接失败（免得把错误 JSON 存成文件），
 * `-o` 把文件按原名落到当前目录。文件名与地址都套单引号，空格、$ 之类不用额外转义。
 * 地址是换来的签名 URL，对方免登录即可下载。
 */
function curlCommand(url: string): string {
  return `curl -f -o ${shellQuote(props.file.filename)} ${shellQuote(url)}`;
}

/** 复制成功后菜单保持打开，curl 项显示「已复制」。 */
async function copyCurl(): Promise<void> {
  if (sharing.value) return;
  const url = await shareUrlOrReport();
  if (url === null) return;
  if (!(await copyText(curlCommand(url)))) {
    emit("error", new Error(t("error.copy")));
    return;
  }
  curlCopied.value = true;
  window.clearTimeout(curlCopiedTimer);
  curlCopiedTimer = window.setTimeout(() => {
    curlCopied.value = false;
  }, 1200);
}

/* ---------- 行内菜单（分组 / 更多） ---------- */

/** 菜单种类：分组菜单只负责「移动到分组」，更多菜单放其余低频操作。 */
type MenuKind = "more" | "group";

// 宽高与 style.css 里 .row-menu 的 width / max-height 保持一致：这里先按上限估位置，
// 渲染完成后再用真实高度校正一次（见 adjustMenuPosition）。
const MENUS: Record<MenuKind, { width: number; maxHeight: number }> = {
  more: { width: 240, maxHeight: 440 },
  group: { width: 240, maxHeight: 440 },
};

/** 当前打开的菜单；null 表示都关着。两个菜单共用一套定位与关闭逻辑。 */
const openMenu = ref<MenuKind | null>(null);
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
 * 菜单内部的滚动不算：分组多时菜单自己要滚，capture 阶段能收到子元素的 scroll，
 * 直接 close 会让菜单一滚就消失。
 */
function onWindowScroll(event: Event): void {
  const target = event.target;
  if (target instanceof Node && menuEl.value?.contains(target)) return;
  closeMenu();
}

function closeMenu(): void {
  if (openMenu.value === null) return;
  openMenu.value = null;
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

/** 点同一个按钮收起，点另一个按钮直接切过去；菜单贴着触发按钮右下角，夹在视口内。 */
function toggleMenu(kind: MenuKind, event: MouseEvent): void {
  const trigger = event.currentTarget as HTMLElement;
  const sameTrigger = triggerEl === trigger;
  triggerEl = trigger;
  if (openMenu.value === kind && sameTrigger) {
    closeMenu();
    return;
  }
  unbindMenuEvents();

  const { width, maxHeight } = MENUS[kind];
  const rect = trigger.getBoundingClientRect();
  const left = Math.min(
    Math.max(8, rect.right - width),
    Math.max(8, window.innerWidth - width - 8),
  );
  let top = rect.bottom + 6;
  if (top + maxHeight > window.innerHeight - 8) {
    top = Math.max(8, window.innerHeight - maxHeight - 8);
  }
  menuStyle.value = { left: `${left}px`, top: `${top}px` };
  openMenu.value = kind;
  window.addEventListener("pointerdown", onOutsidePointer, true);
  window.addEventListener("keydown", onMenuKeydown, true);
  window.addEventListener("resize", closeMenu);
  window.addEventListener("scroll", onWindowScroll, true);
  void nextTick(adjustMenuPosition);
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

function chooseGroup(path: string): void {
  closeMenu();
  emit("move-group", path);
}

/** 菜单里的重命名：先关菜单再交给父组件，避免输入对话框压在下拉菜单上。 */
function chooseRename(): void {
  closeMenu();
  emit("rename");
}

/** 删除是不可逆操作：先关菜单，确认弹窗由父组件负责（和收藏保持同一处交互）。 */
function chooseDelete(): void {
  closeMenu();
  emit("delete");
}

/** 新建分组也是输入框：关菜单后交给父组件，和全局的新建分组共用一个对话框。 */
function chooseNewGroup(): void {
  closeMenu();
  emit("new-group");
}

onBeforeUnmount(() => {
  window.clearTimeout(sharedTimer);
  window.clearTimeout(curlCopiedTimer);
  closeMenu();
});
</script>

<template>
  <li class="file-row" :data-id="file.id">
    <span v-if="draggable" class="drag-handle" aria-hidden="true" v-html="ICONS.grip"></span>

    <span class="file-tile" :class="`kind-${kind}`" v-html="typeIcons[kind]"></span>

    <div class="file-body">
      <p class="file-name" :title="file.filename">{{ file.filename }}</p>
      <p class="file-meta">{{ formatTime(file.created_at) }} · {{ formatSize(file.size) }}</p>
      <p class="file-hash" :title="file.sha256">
        <span class="file-hash-label">{{ t("file.sha256") }}</span>
        <span class="file-hash-value">{{ file.sha256 }}</span>
      </p>
    </div>

    <div class="file-actions">
      <button
        type="button"
        class="icon-btn download"
        :title="t('action.download')"
        :aria-label="t('action.download')"
        v-html="ICONS.download"
        @click="emit('download')"
      ></button>

      <!-- 分享链接独立成按钮：签 10 分钟有效的分享地址并复制绝对地址，请求中禁用 -->
      <button
        type="button"
        class="icon-btn link"
        :class="{ on: shared }"
        :title="t('action.copy-link')"
        :aria-label="t('action.copy-link')"
        :disabled="sharing"
        v-html="shared ? ICONS.check : ICONS.link"
        @click="shareLink"
      ></button>

      <button
        type="button"
        class="icon-btn star"
        :class="{ on: file.is_favorite }"
        :title="file.is_favorite ? t('action.unfavorite') : t('action.favorite')"
        :aria-label="file.is_favorite ? t('action.unfavorite') : t('action.favorite')"
        v-html="ICONS.star"
        @click="emit('favorite')"
      ></button>

      <!-- 分组独立成按钮：高频操作，不藏进聚合菜单 -->
      <button
        type="button"
        class="icon-btn group"
        :class="{ on: openMenu === 'group' }"
        :title="t('action.move-group')"
        :aria-label="t('action.move-group')"
        aria-haspopup="menu"
        :aria-expanded="openMenu === 'group'"
        v-html="ICONS.folder"
        @click="toggleMenu('group', $event)"
      ></button>

      <button
        type="button"
        class="icon-btn more"
        :class="{ on: openMenu === 'more' }"
        :title="t('action.more')"
        :aria-label="t('action.more')"
        aria-haspopup="menu"
        :aria-expanded="openMenu === 'more'"
        v-html="ICONS.more"
        @click="toggleMenu('more', $event)"
      ></button>
    </div>

    <Teleport to="body">
      <!-- 移动到分组：两级缩进，分组多时整张菜单滚动 -->
      <div
        v-if="openMenu === 'group'"
        ref="menuEl"
        class="row-menu is-scroll"
        :style="menuStyle"
        role="menu"
        :aria-label="t('action.move-group')"
      >
        <button
          type="button"
          class="row-menu-item"
          role="menuitemradio"
          :aria-checked="file.group_path === ''"
          @click="chooseGroup('')"
        >
          <span class="row-menu-check">
            <span v-if="file.group_path === ''" v-html="ICONS.check"></span>
          </span>
          <span class="row-menu-icon" v-html="ICONS.inbox"></span>
          <span class="row-menu-label">{{ t("nav.ungrouped") }}</span>
        </button>

        <template v-for="group in topGroups" :key="group.path">
          <button
            type="button"
            class="row-menu-item"
            role="menuitemradio"
            :aria-checked="file.group_path === group.path"
            @click="chooseGroup(group.path)"
          >
            <span class="row-menu-check">
              <span v-if="file.group_path === group.path" v-html="ICONS.check"></span>
            </span>
            <span class="row-menu-icon" v-html="ICONS.folder"></span>
            <span class="row-menu-label">{{ group.name }}</span>
          </button>
          <button
            v-for="child in childrenOf(group.path)"
            :key="child.path"
            type="button"
            class="row-menu-item is-child"
            role="menuitemradio"
            :aria-checked="file.group_path === child.path"
            @click="chooseGroup(child.path)"
          >
            <span class="row-menu-check">
              <span v-if="file.group_path === child.path" v-html="ICONS.check"></span>
            </span>
            <span class="row-menu-icon" v-html="ICONS.folder"></span>
            <span class="row-menu-label">{{ child.name }}</span>
          </button>
        </template>

        <div class="row-menu-sep" role="separator"></div>

        <button type="button" class="row-menu-item is-new" role="menuitem" @click="chooseNewGroup">
          <span class="row-menu-check"></span>
          <span class="row-menu-icon" v-html="ICONS.plus"></span>
          <span class="row-menu-label">{{ t("action.new-group") }}</span>
        </button>
      </div>

      <!-- 其余低频操作 -->
      <div
        v-else-if="openMenu === 'more'"
        ref="menuEl"
        class="row-menu"
        :style="menuStyle"
        role="menu"
        :aria-label="t('action.more')"
      >
        <button type="button" class="row-menu-item" role="menuitem" @click="chooseRename">
          <span class="row-menu-icon" v-html="ICONS.pencil"></span>
          <span class="row-menu-label">{{ t("action.rename") }}</span>
        </button>

        <button
          type="button"
          class="row-menu-item"
          role="menuitem"
          :disabled="sharing"
          @click="copyCurl"
        >
          <span class="row-menu-icon" v-html="curlCopied ? ICONS.check : ICONS.terminal"></span>
          <span class="row-menu-label">
            {{ curlCopied ? t("action.copied") : t("action.copy-curl") }}
          </span>
        </button>

        <div class="row-menu-sep" role="separator"></div>

        <button type="button" class="row-menu-item is-danger" role="menuitem" @click="chooseDelete">
          <span class="row-menu-icon" v-html="ICONS.trash"></span>
          <span class="row-menu-label">{{ t("action.delete") }}</span>
        </button>
      </div>
    </Teleport>
  </li>
</template>

<style scoped>
.file-row {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 12px 16px;
  transition: background-color 0.15s;
}

.file-row + .file-row {
  border-top: 1px solid var(--line);
}

.file-row:hover {
  background: #fafbff;
}

/* 拖拽把手：常显低透明度，hover 加深；触屏长按 120ms 才进入拖拽 */
.drag-handle {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 16px;
  margin: 0 -6px 0 -4px;
  color: var(--ink-3);
  cursor: grab;
  opacity: 0.45;
  transition:
    opacity 0.15s,
    color 0.15s;
}

.drag-handle :deep(svg) {
  display: block;
  width: 15px;
  height: 15px;
}

.drag-handle:hover {
  color: var(--ink-2);
  opacity: 1;
}

.drag-handle:active {
  cursor: grabbing;
}

.file-row.drag-ghost {
  border-radius: 12px;
  background: var(--accent-soft);
  opacity: 0.6;
}

.file-row.drag-chosen {
  background: #fafbff;
}

.file-row.drag-dragging {
  background: var(--surface);
  box-shadow: var(--shadow-lg);
}

.file-tile {
  display: grid;
  flex: 0 0 auto;
  place-items: center;
  width: 42px;
  height: 42px;
  border-radius: 12px;
  background: var(--surface-2);
  color: var(--ink-3);
}

.file-tile :deep(svg) {
  width: 21px;
  height: 21px;
}

/* 文件类型配色：同一类型一个颜色，图标形状各不相同。 */
.kind-image {
  background: #e7f8f0;
  color: #0f9d63;
}

.kind-video {
  background: #fdeef3;
  color: #db2777;
}

.kind-audio {
  background: #e9f6ff;
  color: #0284c7;
}

.kind-archive {
  background: #fff5e6;
  color: #d97706;
}

.kind-pdf {
  background: #ffeef1;
  color: #e11d48;
}

.kind-doc {
  background: #eef0ff;
  color: #4f5bd5;
}

.kind-sheet {
  background: #e8f8f2;
  color: #0d9488;
}

.kind-slides {
  background: #fff1e4;
  color: #ea580c;
}

.kind-code {
  background: #f2ecff;
  color: #7c3aed;
}

.kind-text {
  background: #f1f3f9;
  color: #64748b;
}

.kind-other {
  background: #f1f2f8;
  color: #6b7280;
}

.file-body {
  flex: 1 1 auto;
  min-width: 0;
}

.file-name {
  overflow: hidden;
  color: var(--ink);
  font-size: 13.5px;
  font-weight: 620;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.file-meta {
  margin-top: 2px;
  color: var(--ink-3);
  font-size: 11.5px;
  font-variant-numeric: tabular-nums;
}

/* 文件指纹：等宽字体、单行省略；完整值放在 title 里，文本可选中复制 */
.file-hash {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
  margin-top: 1px;
  color: var(--ink-3);
  font-size: 10.5px;
  line-height: 1.4;
}

.file-hash-label {
  flex: 0 0 auto;
  font-size: 9.5px;
  font-weight: 700;
  letter-spacing: 0.04em;
}

.file-hash-value {
  overflow: hidden;
  font-family: ui-monospace, sfmono-regular, menlo, consolas, monospace;
  text-overflow: ellipsis;
  white-space: nowrap;
  user-select: text;
}

.file-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 1px;
}

.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: 9px;
  color: var(--ink-3);
  transition:
    color 0.15s,
    background-color 0.15s;
}

.icon-btn :deep(svg) {
  display: block;
  width: 17px;
  height: 17px;
}

.icon-btn:hover {
  background: var(--surface-2);
  color: var(--ink-2);
}

.icon-btn.star {
  color: #c4c8d6;
}

.icon-btn.star.on {
  color: var(--amber);
}

.icon-btn.star:hover {
  color: var(--amber);
}

.icon-btn.download:hover,
.icon-btn.link:hover {
  color: var(--accent);
}

.icon-btn.link.on {
  color: var(--green);
}

.icon-btn:disabled {
  opacity: 0.45;
  cursor: default;
}

.icon-btn.group:hover,
.icon-btn.group.on,
.icon-btn.more.on {
  background: var(--surface-2);
  color: var(--ink);
}

@container (max-width: 620px) {
/* 内容区很窄（手机 / 小窗口）时，操作按钮换到第二行，文件名拿回整行。 */
  .file-row {
    flex-wrap: wrap;
    align-items: flex-start;
    gap: 10px;
    padding: 12px;
  }

  .file-body {
    flex-basis: calc(100% - 54px);
  }

  .file-actions {
    flex-basis: 100%;
    justify-content: flex-end;
  }

  .icon-btn {
    width: 34px;
    height: 34px;
  }
}

@media (max-width: 560px) {
  .icon-btn {
    width: 34px;
    height: 34px;
  }

  .file-tile {
    width: 38px;
    height: 38px;
  }

  .file-tile :deep(svg) {
    width: 19px;
    height: 19px;
  }
}
</style>
