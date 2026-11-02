<script setup lang="ts">
/**
 * 列表里的一行文件：拖拽把手 + 类型图标块 + 名称/元信息 + 常驻操作按钮。
 *
 * 复制直链只对公开文件可用（私有文件的 /s/{id} 会 404，复制了也没用）；
 * navigator.clipboard 在 HTTP 部署下不存在或不可写，所以有 textarea + execCommand 兜底。
 *
 * 「移动到分组」菜单用 Teleport 挂到 body：列表卡片有 overflow 裁剪，绝对定位的
 * 下拉菜单会被切掉；菜单定位按按钮 rect 计算并夹在视口内，滚动/缩放/Esc/点外面都关闭。
 */
import { computed, onBeforeUnmount, ref } from "vue";

import { publicLink } from "@/api";
import { formatSize, formatTime } from "@/format";
import { ICONS, typeIcons, type FileKind } from "@/icons";
import { t } from "@/i18n";
import type { FileItem } from "@/types";

const props = defineProps<{
  file: FileItem;
  /** 全部分组名（用于「移动到分组」菜单）。 */
  groups: readonly string[];
  /** 可拖拽排序的列表才显示把手；搜索过滤时顺序不可提交，把手隐藏。 */
  draggable: boolean;
}>();

const emit = defineEmits<{
  favorite: [];
  download: [];
  rename: [];
  visibility: [];
  delete: [];
  "move-group": [groupName: string];
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

/* ---------- 「移动到分组」菜单 ---------- */

const MENU_WIDTH = 216;
const MENU_MAX_HEIGHT = 328;

const menuOpen = ref(false);
const menuEl = ref<HTMLElement | null>(null);
const menuStyle = ref<{ left: string; top: string }>({ left: "0px", top: "0px" });
let triggerEl: HTMLElement | null = null;

function closeMenu(): void {
  if (!menuOpen.value) return;
  menuOpen.value = false;
  window.removeEventListener("pointerdown", onOutsidePointer, true);
  window.removeEventListener("keydown", onMenuKeydown, true);
  window.removeEventListener("resize", closeMenu);
  window.removeEventListener("scroll", closeMenu, true);
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

function toggleGroupMenu(event: MouseEvent): void {
  triggerEl = event.currentTarget as HTMLElement;
  if (menuOpen.value) {
    closeMenu();
    return;
  }
  const rect = triggerEl.getBoundingClientRect();
  const left = Math.min(
    Math.max(8, rect.right - MENU_WIDTH),
    Math.max(8, window.innerWidth - MENU_WIDTH - 8),
  );
  let top = rect.bottom + 6;
  if (top + MENU_MAX_HEIGHT > window.innerHeight - 8) {
    top = Math.max(8, window.innerHeight - MENU_MAX_HEIGHT - 8);
  }
  menuStyle.value = { left: `${left}px`, top: `${top}px` };
  menuOpen.value = true;
  window.addEventListener("pointerdown", onOutsidePointer, true);
  window.addEventListener("keydown", onMenuKeydown, true);
  window.addEventListener("resize", closeMenu);
  window.addEventListener("scroll", closeMenu, true);
}

function chooseGroup(groupName: string): void {
  closeMenu();
  emit("move-group", groupName);
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
    </div>

    <div class="file-actions">
      <button
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
        class="icon-btn download"
        :title="t('action.download')"
        :aria-label="t('action.download')"
        v-html="ICONS.download"
        @click="emit('download')"
      ></button>

      <button
        type="button"
        class="icon-btn rename"
        :title="t('action.rename')"
        :aria-label="t('action.rename')"
        v-html="ICONS.pencil"
        @click="emit('rename')"
      ></button>

      <button
        type="button"
        class="icon-btn link"
        :class="{ 'is-copied': copied, 'is-disabled': !file.is_public }"
        :disabled="!file.is_public"
        :title="file.is_public ? (copied ? t('action.copied') : t('action.copy-link')) : t('action.copy-link-private')"
        :aria-label="file.is_public ? t('action.copy-link') : t('action.copy-link-private')"
        @click="copyLink"
        v-html="copied ? ICONS.check : ICONS.link"
      ></button>

      <button
        type="button"
        class="icon-btn visibility"
        :class="{ on: file.is_public }"
        :title="file.is_public ? t('action.make-private') : t('action.make-public')"
        :aria-label="file.is_public ? t('action.make-private') : t('action.make-public')"
        v-html="file.is_public ? ICONS.eye : ICONS.eyeOff"
        @click="emit('visibility')"
      ></button>

      <button
        type="button"
        class="icon-btn group"
        :title="t('action.move-group')"
        :aria-label="t('action.move-group')"
        aria-haspopup="menu"
        :aria-expanded="menuOpen"
        v-html="ICONS.folder"
        @click="toggleGroupMenu"
      ></button>

      <button
        type="button"
        class="icon-btn delete"
        :title="t('action.delete')"
        :aria-label="t('action.delete')"
        v-html="ICONS.trash"
        @click="emit('delete')"
      ></button>
    </div>

    <Teleport to="body">
      <div
        v-if="menuOpen"
        ref="menuEl"
        class="group-menu"
        :style="menuStyle"
        role="menu"
        :aria-label="t('action.move-group')"
      >
        <button
          type="button"
          class="group-menu-item"
          role="menuitemradio"
          :aria-checked="file.group_name === ''"
          @click="chooseGroup('')"
        >
          <span class="group-menu-check">
            <span v-if="file.group_name === ''" v-html="ICONS.check"></span>
          </span>
          <span class="group-menu-icon" v-html="ICONS.inbox"></span>
          <span class="group-menu-label">{{ t("nav.ungrouped") }}</span>
        </button>

        <button
          v-for="group in groups"
          :key="group"
          type="button"
          class="group-menu-item"
          role="menuitemradio"
          :aria-checked="file.group_name === group"
          @click="chooseGroup(group)"
        >
          <span class="group-menu-check">
            <span v-if="file.group_name === group" v-html="ICONS.check"></span>
          </span>
          <span class="group-menu-icon" v-html="ICONS.folder"></span>
          <span class="group-menu-label">{{ group }}</span>
        </button>

        <div class="group-menu-sep" role="separator"></div>

        <button type="button" class="group-menu-item is-new" role="menuitem" @click="promptNewGroup">
          <span class="group-menu-check"></span>
          <span class="group-menu-icon" v-html="ICONS.plus"></span>
          <span class="group-menu-label">{{ t("action.new-group") }}</span>
        </button>
      </div>
    </Teleport>
  </li>
</template>
