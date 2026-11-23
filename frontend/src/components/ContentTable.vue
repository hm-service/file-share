<script setup lang="ts">
/**
 * 内容区表格：当前目录的「子目录行 + 文件行」。
 *
 * 列与表头由 CSS 变量对齐（--col-size / --col-time / --col-actions），
 * 文件行在自己的组件里用同一组变量排布。目录深度不限制：子目录行直接进入下一层。
 *
 * 只读视图没有排序、拖拽与右键菜单：目录与文件都按名称排序展示。
 */
import { t } from "@/i18n";
import { ICONS } from "@/icons";
import type { DirEntry, FileItem } from "@/types";

import FileRow from "./FileRow.vue";

defineProps<{
  dirs: DirEntry[];
  files: FileItem[];
  /** 文件行是否额外显示所属目录（最近 / 搜索结果里需要）。 */
  showGroup?: boolean;
  /** 当前账号是否有收藏权限（fav）：没有就不显示星标按钮。 */
  canFavorite: boolean;
}>();

const emit = defineEmits<{
  "open-folder": [path: string];
  favorite: [file: FileItem];
  download: [file: FileItem];
  error: [error: unknown];
}>();
</script>

<template>
  <div class="table">
    <div class="table-head" role="row">
      <span class="head-name">{{ t("column.name") }}</span>
      <span>{{ t("column.size") }}</span>
      <span>{{ t("column.uploaded") }}</span>
      <span class="head-actions">{{ t("column.actions") }}</span>
    </div>

    <ul v-if="dirs.length" class="dir-list">
      <li v-for="dir in dirs" :key="dir.path" :data-path="dir.path" class="dir-row">
        <div class="cell-name">
          <span class="folder-tile" v-html="ICONS.folder"></span>
          <button type="button" class="dir-name" :title="dir.name" @click="emit('open-folder', dir.path)">
            {{ dir.name }}
          </button>
        </div>
        <span class="cell-size">{{ t("content.item-count", { count: dir.fileCount }) }}</span>
        <span class="cell-time">—</span>
        <span class="cell-actions"></span>
      </li>
    </ul>

    <ul v-if="files.length" class="file-list" :class="{ 'after-dirs': dirs.length > 0 }">
      <FileRow
        v-for="file in files"
        :key="file.id"
        :file="file"
        :show-group="showGroup"
        :can-favorite="canFavorite"
        @favorite="emit('favorite', file)"
        @download="emit('download', file)"
        @error="emit('error', $event)"
      />
    </ul>
  </div>
</template>

<style scoped>
.table {
  --col-size: 110px;
  --col-time: 140px;
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
  grid-template-columns: minmax(0, 1fr) var(--col-size) var(--col-time) var(--col-actions);
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
  padding-left: 36px;
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

/* 目录区与文件区的分界：文件行的首行边框在自己的列表里被去掉，有目录时补回来 */
.file-list.after-dirs :deep(.file-row:first-child) {
  border-top: 1px solid var(--line);
}

.dir-row:hover {
  background: var(--surface-2);
}

.dir-row .cell-name {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  padding-left: 6px;
}

.folder-tile {
  width: 30px;
  height: 30px;
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  color: var(--amber);
  background: #fff7ed;
}

.folder-tile:deep(svg) {
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

.dir-row .cell-size {
  color: var(--ink-3);
  font-size: 12.5px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.dir-row .cell-time {
  color: var(--ink-3);
  font-size: 13px;
}

@media (max-width: 900px) {
  .table {
    --col-actions: 100px;
  }

  .table-head,
  .dir-row {
    grid-template-columns: minmax(0, 1fr) var(--col-actions);
  }

  .dir-row .cell-size,
  .dir-row .cell-time,
  .table-head > span:nth-child(2),
  .table-head > span:nth-child(3) {
    display: none;
  }

  .table-head .head-name {
    padding-left: 6px;
  }
}
</style>