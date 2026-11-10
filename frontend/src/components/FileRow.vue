<script setup lang="ts">
/**
 * 列表里的一行文件：拖拽把手 + 类型图标块 + 名称/元信息 + 操作区。
 *
 * 行内只留「下载、收藏、分组、更多」四个按钮：分组是高频操作，单独开「移动到分组」菜单；
 * 重命名、复制直链、公开/私有、删除收进最右的「更多」菜单，避免一行里挤满七个图标。
 *
 * 复制直链只对公开文件可用（私有文件的 /s/{id} 会 404，复制了也没用）；
 * navigator.clipboard 在 HTTP 部署下不存在或不可写，所以有 textarea + execCommand 兜底。
 *
 * 菜单用 Teleport 挂到 body：列表卡片有 overflow 裁剪，绝对定位的下拉菜单会被切掉；
 * 菜单定位按按钮 rect 计算并夹在视口内，滚动/缩放/Esc/点外面都关闭（菜单自身滚动除外）。
 */
import { computed, nextTick, onBeforeUnmount, ref } from "vue";

import { publicLink } from "@/api";
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
  visibility: [];
  delete: [];
  "move-group": [groupPath: string];
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

/* ---------- 复制直链 ---------- */

const copied = ref(false);
let copiedTimer = 0;

/** 复制成功返回 true；失败不弹窗，只是不显示「已复制」。 */
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

async function copyLink(): Promise<void> {
  if (!props.file.is_public) return;
  const ok = await copyText(publicLink(props.file.id));
  if (!ok) return;
  copied.value = true;
  window.clearTimeout(copiedTimer);
  copiedTimer = window.setTimeout(() => {
    copied.value = false;
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

/** 菜单里的重命名 / 公开私有：先关菜单再交给父组件，避免弹窗压在下拉菜单上。 */
function chooseRename(): void {
  closeMenu();
  emit("rename");
}

function chooseVisibility(): void {
  closeMenu();
  emit("visibility");
}

/** 删除是不可逆操作：先关菜单，确认弹窗由父组件负责（和收藏 / 公开保持同一处交互）。 */
function chooseDelete(): void {
  closeMenu();
  emit("delete");
}

async function chooseCopyLink(): Promise<void> {
  if (!props.file.is_public) return;
  closeMenu();
  await copyLink();
}

function promptNewGroup(): void {
  closeMenu();
  const input = window.prompt(t("group.new-prompt"))?.trim();
  if (input) emit("move-group", input);
}

onBeforeUnmount(() => {
  window.clearTimeout(copiedTimer);
  closeMenu();
});
</script>

<template>
  <li class="file-row" :data-id="file.id">
    <span v-if="draggable" class="drag-handle" aria-hidden="true" v-html="ICONS.grip"></span>

    <span class="file-tile" :class="`kind-${kind}`" v-html="typeIcons[kind]"></span>

    <div class="file-body">
      <div class="file-title-line">
        <p class="file-name" :title="file.filename">{{ file.filename }}</p>
        <span v-if="file.is_public" class="badge badge-public">{{ t("badge.public") }}</span>
      </div>
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

        <button type="button" class="row-menu-item is-new" role="menuitem" @click="promptNewGroup">
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
          :disabled="!file.is_public"
          :title="file.is_public ? undefined : t('action.copy-link-private')"
          @click="chooseCopyLink"
        >
          <span class="row-menu-icon" v-html="copied ? ICONS.check : ICONS.link"></span>
          <span class="row-menu-label">{{ copied ? t("action.copied") : t("action.copy-link") }}</span>
        </button>

        <button type="button" class="row-menu-item" role="menuitem" @click="chooseVisibility">
          <span class="row-menu-icon" v-html="file.is_public ? ICONS.eyeOff : ICONS.eye"></span>
          <span class="row-menu-label">
            {{ file.is_public ? t("action.make-private") : t("action.make-public") }}
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
