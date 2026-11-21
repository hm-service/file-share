<script setup lang="ts">
/**
 * 内容区表格：当前文件夹的「子文件夹行 + 文件行」。
 *
 * 列与表头由 CSS 变量对齐（--col-size / --col-time / --col-hash / --col-actions），
 * 文件行在自己的组件里用同一组变量排布。文件夹行与文件行各用一个 SortableJS 实例：
 * 文件夹只在文件夹之间排序（永远排在文件前），文件只在同一个文件夹内排序（0..n-1 的
 * sort_order）。两个列表互不连通，文件夹拖不到文件行里，文件也拖不进文件夹区。
 *
 * 文件夹行同时是拖放目标：内部文件拖过来表示「移到这个文件夹」，外部文件拖过来
 * 表示「上传到这个文件夹」。两种落点都高亮。
 */
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import Sortable from "sortablejs";

import FileRow from "@/components/FileRow.vue";
import { hasFiles } from "@/dnd";
import { t } from "@/i18n";
import { ICONS } from "@/icons";
import type { DirEntry, FileItem, GroupItem } from "@/types";

const props = defineProps<{
  dirs: DirEntry[];
  files: FileItem[];
  /** 全部分组：透传给文件行的「移动到文件夹」菜单。 */
  groups: readonly GroupItem[];
  /** 文件行是否可拖拽排序（搜索过滤时不提交顺序，禁用）。 */
  sortable: boolean;
  /** 文件行是否额外显示所属文件夹（最近 / 搜索结果里需要）。 */
  showGroup?: boolean;
  /** 正在拖拽的内部文件 id（单一事实来源在 App）：用来区分移动文件与上传外部文件。 */
  draggingFileId?: string | null;
  /** 当前账号是否有写权限：没有就藏掉收藏 / 移动 / 重命名 / 删除这些入口。 */
  canWrite: boolean;
}>();

const emit = defineEmits<{
  "open-folder": [path: string];
  /** 文件夹行的「⋯」：由父组件按点击位置打开菜单。 */
  "folder-menu": [event: MouseEvent, path: string];
  favorite: [file: FileItem];
  download: [file: FileItem];
  rename: [file: FileItem];
  delete: [file: FileItem];
  "move-group": [file: FileItem, path: string];
  "new-group": [file: FileItem];
  error: [error: unknown];
  reorder: [orderedIds: string[]];
  /** 文件夹行拖拽后的新顺序（完整路径数组，父组件取最后一段提交）。 */
  "reorder-dirs": [orderedPaths: string[]];
  /** 外部文件拖到某个文件夹行：上传到该文件夹。 */
  "drop-files": [files: FileList, path: string];
  /** 内部文件开始 / 结束拖拽（App 用来区分移动文件与上传外部文件）。 */
  "drag-start": [file: FileItem];
  "drag-end": [];
}>();

const filesEl = ref<HTMLElement | null>(null);
const dirsEl = ref<HTMLElement | null>(null);
const dropTarget = ref<string | null>(null);
let sorter: Sortable | null = null;
let sorterDirs: Sortable | null = null;

function destroySortable(): void {
  sorter?.destroy();
  sorter = null;
}

function setupSortable(): void {
  destroySortable();
  if (!props.sortable || !filesEl.value) return;
  sorter = Sortable.create(filesEl.value, {
    handle: ".drag-handle",
    animation: 150,
    ghostClass: "drag-ghost",
    chosenClass: "drag-chosen",
    onStart: (event) => {
      const id = (event.item as HTMLElement).dataset.id ?? "";
      const file = props.files.find((entry) => entry.id === id);
      if (file) emit("drag-start", file);
    },
    onEnd: () => {
      dropTarget.value = null;
      emit("drag-end");
      const ordered = Array.from(filesEl.value?.children ?? [])
        .map((el) => (el as HTMLElement).dataset.id ?? "")
        .filter((id) => id !== "");
      if (ordered.length === props.files.length) emit("reorder", ordered);
    },
  });
}

function destroySortableDirs(): void {
  sorterDirs?.destroy();
  sorterDirs = null;
}

/** 文件夹行只在自己的列表里排序：不同容器、不共享 group，落点自然进不了文件区。 */
function setupSortableDirs(): void {
  destroySortableDirs();
  if (!props.sortable || !dirsEl.value) return;
  sorterDirs = Sortable.create(dirsEl.value, {
    handle: ".drag-handle",
    animation: 150,
    ghostClass: "drag-ghost",
    chosenClass: "drag-chosen",
    onEnd: () => {
      const ordered = Array.from(dirsEl.value?.children ?? [])
        .map((el) => (el as HTMLElement).dataset.path ?? "")
        .filter((path) => path !== "");
      if (ordered.length === props.dirs.length) emit("reorder-dirs", ordered);
    },
  });
}

function onDirDragOver(event: DragEvent, path: string): void {
  if (props.draggingFileId) {
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
    dropTarget.value = path;
    return;
  }
  if (!hasFiles(event)) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
  dropTarget.value = path;
}

function onDirDragLeave(event: DragEvent, path: string): void {
  const current = event.currentTarget as HTMLElement;
  if (event.relatedTarget instanceof Node && current.contains(event.relatedTarget)) return;
  if (dropTarget.value === path) dropTarget.value = null;
}

function onDirDrop(event: DragEvent, path: string): void {
  event.preventDefault();
  event.stopPropagation();
  dropTarget.value = null;
  if (props.draggingFileId) {
    const file = props.files.find((entry) => entry.id === props.draggingFileId);
    if (file && file.group_path !== path) emit("move-group", file, path);
    return;
  }
  const dropped = event.dataTransfer?.files;
  if (dropped && dropped.length > 0) emit("drop-files", dropped, path);
}

watch(
  () => props.sortable,
  () => {
    setupSortable();
    setupSortableDirs();
  },
);
watch(
  () => props.files.length,
  () => {
    // 行数变化后 Sortable 仍可用；这里只为过滤视图切换时重建 DOM 顺序
    setupSortable();
  },
);
watch(
  () => props.dirs.length,
  () => setupSortableDirs(),
);

onMounted(() => {
  setupSortable();
  setupSortableDirs();
});
onBeforeUnmount(() => {
  destroySortable();
  destroySortableDirs();
});
</script>

<template>
  <div class="table">
    <div class="table-head" role="row">
      <span class="head-name">{{ t("column.name") }}</span>
      <span>{{ t("column.size") }}</span>
      <span>{{ t("column.uploaded") }}</span>
      <span>{{ t("column.hash") }}</span>
      <span class="head-actions">{{ t("column.actions") }}</span>
    </div>

    <ul ref="dirsEl" class="dir-list">
      <li
        v-for="dir in dirs"
        :key="dir.path"
        :data-path="dir.path"
        class="dir-row"
        :class="{ 'is-drop': dropTarget === dir.path }"
        @contextmenu.prevent="emit('folder-menu', $event, dir.path)"
        @dragover="onDirDragOver($event, dir.path)"
        @dragleave="onDirDragLeave($event, dir.path)"
        @drop="onDirDrop($event, dir.path)"
      >
        <div class="cell-name">
          <span v-if="sortable" class="drag-handle" aria-hidden="true" v-html="ICONS.grip"></span>
          <span class="folder-tile" v-html="ICONS.folder"></span>
          <button type="button" class="dir-name" :title="dir.name" @click="emit('open-folder', dir.path)">
            {{ dir.name }}
          </button>
        </div>
        <span class="cell-size">{{ t("content.item-count", { count: dir.fileCount }) }}</span>
        <span class="cell-time">—</span>
        <span class="cell-hash"></span>
        <div class="cell-actions">
          <button
            type="button"
            class="icon-btn folder-more"
            :title="t('action.group-menu')"
            :aria-label="t('action.group-menu')"
            aria-haspopup="menu"
            v-html="ICONS.more"
            @click="emit('folder-menu', $event, dir.path)"
          ></button>
        </div>
      </li>
    </ul>

    <ul ref="filesEl" class="file-list" :class="{ 'after-dirs': dirs.length > 0 }">
      <FileRow
        v-for="file in files"
        :key="file.id"
        :file="file"
        :groups="groups"
        :draggable="sortable"
        :can-write="canWrite"
        :show-group="showGroup"
        @favorite="emit('favorite', file)"
        @download="emit('download', file)"
        @rename="emit('rename', file)"
        @delete="emit('delete', file)"
        @move-group="(path) => emit('move-group', file, path)"
        @new-group="emit('new-group', file)"
        @error="emit('error', $event)"
      />
    </ul>
  </div>
</template>

<style scoped>
.table {
  --col-size: 110px;
  --col-time: 140px;
  --col-hash: 96px;
  --col-actions: 190px;

  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
  box-shadow: var(--shadow-sm);
}

.table-head,
.dir-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) var(--col-size) var(--col-time) var(--col-hash)
    var(--col-actions);
  align-items: center;
  padding: 0 14px 0 6px;
}

.table-head {
  height: 40px;
  padding-top: 2px;
  font-size: 12px;
  color: var(--ink-3);
  background: var(--surface-2);
  border-bottom: 1px solid var(--line);
}

.table-head .head-name {
  padding-left: 28px;
}

.table-head .head-actions {
  text-align: right;
  padding-right: 4px;
}

.dir-list,
.file-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.dir-row {
  min-height: 52px;
  border-top: 1px solid var(--line);
  cursor: default;
  transition: background 0.1s ease;
}

.dir-list .dir-row:first-child {
  border-top: 0;
}

/* 文件夹区与文件区的分界：文件行的首行边框在自己的列表里被去掉，有文件夹时补回来 */
.file-list.after-dirs :deep(.file-row:first-child) {
  border-top: 1px solid var(--line);
}

.dir-row:hover {
  background: var(--surface-2);
}

.dir-row.is-drop {
  background: var(--accent-soft);
  box-shadow: inset 0 0 0 1.5px var(--accent);
}

.dir-row .cell-name {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

/* 拖拽把手与文件行一致：平时透明，悬停行或聚焦时才显形 */
.dir-row .drag-handle {
  width: 18px;
  flex: 0 0 18px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--ink-3);
  opacity: 0;
  cursor: grab;
  transition: opacity 0.12s ease;
}

.dir-row .drag-handle:deep(svg) {
  width: 16px;
  height: 16px;
}

.dir-row:hover .drag-handle,
.dir-row .drag-handle:focus-visible {
  opacity: 1;
}

.dir-row.drag-ghost {
  opacity: 0.35;
}

.dir-row.drag-chosen {
  cursor: grabbing;
}

.folder-tile {
  width: 30px;
  height: 30px;
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  background: #fff7e6;
  color: #e09b2d;
}

.folder-tile :deep(svg) {
  width: 18px;
  height: 18px;
}

.dir-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  border: 0;
  background: none;
  color: var(--ink);
  font: inherit;
  font-weight: 600;
  text-align: left;
  cursor: pointer;
}

.dir-name:hover {
  color: var(--accent);
  text-decoration: underline;
}

.dir-name:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
  border-radius: 4px;
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
}

.icon-btn {
  width: 30px;
  height: 30px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  color: var(--ink-3);
}

.icon-btn :deep(svg) {
  width: 17px;
  height: 17px;
}

.icon-btn:hover {
  background: var(--accent-soft);
  color: var(--accent);
}

@media (max-width: 900px) {
  .table {
    --col-size: 0px;
    --col-time: 0px;
    --col-hash: 0px;
    --col-actions: 150px;
  }

  .table-head,
  .dir-row {
    grid-template-columns: minmax(0, 1fr) var(--col-actions);
    padding-right: 10px;
  }

  .table-head > span:nth-child(2),
  .table-head > span:nth-child(3),
  .table-head > span:nth-child(4),
  .dir-row .cell-size,
  .dir-row .cell-time,
  .dir-row .cell-hash {
    display: none;
  }
}
</style>