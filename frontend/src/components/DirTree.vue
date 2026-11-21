<script setup lang="ts">
/**
 * 侧栏：快捷入口（最近 / 收藏 / 未分组） + 文件夹树（最多三级）。
 *
 * 树只负责展示与交互，不发请求：展开 / 折叠、导航、右键菜单、拖放都通过事件交给 App。
 * 文件夹行同时是拖放目标：内部文件拖过来表示「移到这个文件夹」，外部文件拖过来表示
 * 「上传到这个文件夹」。
 */
import { computed, ref } from "vue";

import { hasFiles } from "@/dnd";
import { t } from "@/i18n";
import { ICONS } from "@/icons";
import type { GroupItem } from "@/types";

const props = defineProps<{
  groups: readonly GroupItem[];
  /** 每个文件夹子树（含后代）的文件数。 */
  counts: ReadonlyMap<string, number>;
  recentCount: number;
  favoritesCount: number;
  ungroupedCount: number;
  /** 当前视图：快捷入口或文件夹。 */
  viewKind: "recent" | "favorites" | "ungrouped" | "group";
  /** 当前文件夹路径（viewKind = group 时有效）。 */
  activePath: string;
  /** 已折叠的文件夹路径（折叠后不渲染子级）。 */
  folded: ReadonlySet<string>;
  /** 侧栏底部的存储提示文案（空串表示不显示）。 */
  storageText: string;
  storageHint: string;
  /** 是否有内部文件正在拖拽：内部拖拽的 dataTransfer 不带 Files 类型。 */
  internalDrag: boolean;
  /** 当前视图是否还能新建文件夹（三级文件夹下不能再建）。 */
  canCreate: boolean;
}>();

const emit = defineEmits<{
  "show-recent": [];
  "show-favorites": [];
  "show-ungrouped": [];
  "open-folder": [path: string];
  toggle: [path: string];
  /** 右键菜单：位置按点击点计算，由 App 打开菜单。 */
  menu: [event: MouseEvent, path: string];
  "new-folder": [];
  /** 拖到某个文件夹上（内部文件移动 / 外部文件上传，由 App 区分）。 */
  "drop-folder": [files: FileList | null, path: string];
}>();

const topGroups = computed(() => props.groups.filter((group) => group.parent === ""));
/** 拖拽悬停的文件夹：只在组件内部维护，用来点亮落点。 */
const hoverPath = ref<string | null>(null);

function childrenOf(path: string): GroupItem[] {
  return props.groups.filter((group) => group.parent === path);
}

function isFolded(path: string): boolean {
  return props.folded.has(path);
}

function countOf(path: string): number {
  return props.counts.get(path) ?? 0;
}

function hasChildren(path: string): boolean {
  return childrenOf(path).length > 0;
}

function open(path: string): void {
  emit("open-folder", path);
}

function onDragOver(event: DragEvent, path: string): void {
  if (!props.internalDrag && !hasFiles(event)) return;
  event.preventDefault();
  if (event.dataTransfer) {
    event.dataTransfer.dropEffect = props.internalDrag ? "move" : "copy";
  }
  hoverPath.value = path;
}

function onDragLeave(event: DragEvent, path: string): void {
  const current = event.currentTarget as HTMLElement;
  if (event.relatedTarget instanceof Node && current.contains(event.relatedTarget)) return;
  if (hoverPath.value === path) hoverPath.value = null;
}

function onDrop(event: DragEvent, path: string): void {
  event.preventDefault();
  event.stopPropagation();
  hoverPath.value = null;
  emit("drop-folder", event.dataTransfer?.files ?? null, path);
}
</script>

<template>
  <aside class="tree">
    <nav class="quick">
      <button
        type="button"
        class="node"
        :class="{ active: viewKind === 'recent' }"
        @click="emit('show-recent')"
      >
        <span class="node-icon" v-html="ICONS.refresh"></span>
        <span class="node-label">{{ t("nav.recent") }}</span>
        <span class="node-count">{{ recentCount }}</span>
      </button>
      <button
        type="button"
        class="node"
        :class="{ active: viewKind === 'favorites' }"
        @click="emit('show-favorites')"
      >
        <span class="node-icon" v-html="ICONS.star"></span>
        <span class="node-label">{{ t("nav.favorites") }}</span>
        <span class="node-count">{{ favoritesCount }}</span>
      </button>
      <button
        type="button"
        class="node"
        :class="{ active: viewKind === 'ungrouped' }"
        @click="emit('show-ungrouped')"
      >
        <span class="node-icon" v-html="ICONS.inbox"></span>
        <span class="node-label">{{ t("nav.ungrouped") }}</span>
        <span class="node-count">{{ ungroupedCount }}</span>
      </button>
    </nav>

    <div class="section-head">
      <span>{{ t("nav.tree") }}</span>
      <button type="button" class="section-add" :title="t('action.new-group')" @click="emit('new-folder')">
        <span v-html="ICONS.plus"></span>
      </button>
    </div>

    <div class="nav-tree" role="tree">
      <template v-for="group in topGroups" :key="group.path">
        <div
          class="node is-group"
          :class="{
            active: viewKind === 'group' && activePath === group.path,
            'is-drop': hoverPath === group.path,
          }"
          role="treeitem"
          :aria-expanded="hasChildren(group.path) ? !isFolded(group.path) : undefined"
          @contextmenu.prevent="emit('menu', $event, group.path)"
          @dragenter="onDragOver($event, group.path)"
          @dragover="onDragOver($event, group.path)"
          @dragleave="onDragLeave($event, group.path)"
          @drop="onDrop($event, group.path)"
        >
          <button
            type="button"
            class="chev"
            :class="{ blank: !hasChildren(group.path), folded: isFolded(group.path) }"
            :aria-label="isFolded(group.path) ? t('action.expand-group') : t('action.collapse-group')"
            @click.stop="hasChildren(group.path) && emit('toggle', group.path)"
            v-html="hasChildren(group.path) ? ICONS.chevron : ''"
          ></button>
          <button type="button" class="node-main" @click="open(group.path)">
            <span class="folder-icon" v-html="ICONS.folder"></span>
            <span class="node-label">{{ group.name }}</span>
            <span class="node-count">{{ countOf(group.path) }}</span>
          </button>
        </div>

        <template v-if="!isFolded(group.path)">
          <template v-for="child in childrenOf(group.path)" :key="child.path">
            <div
              class="node is-group is-child"
              :class="{
                active: viewKind === 'group' && activePath === child.path,
                'is-drop': hoverPath === child.path,
              }"
              role="treeitem"
              :aria-expanded="hasChildren(child.path) ? !isFolded(child.path) : undefined"
              @contextmenu.prevent="emit('menu', $event, child.path)"
              @dragenter="onDragOver($event, child.path)"
              @dragover="onDragOver($event, child.path)"
              @dragleave="onDragLeave($event, child.path)"
              @drop="onDrop($event, child.path)"
            >
              <button
                type="button"
                class="chev"
                :class="{ blank: !hasChildren(child.path), folded: isFolded(child.path) }"
                @click.stop="hasChildren(child.path) && emit('toggle', child.path)"
                v-html="hasChildren(child.path) ? ICONS.chevron : ''"
              ></button>
              <button type="button" class="node-main" @click="open(child.path)">
                <span class="folder-icon" v-html="ICONS.folder"></span>
                <span class="node-label">{{ child.name }}</span>
                <span class="node-count">{{ countOf(child.path) }}</span>
              </button>
            </div>

            <template v-if="!isFolded(child.path)">
              <div
                v-for="grandchild in childrenOf(child.path)"
                :key="grandchild.path"
                class="node is-group is-grandchild"
                :class="{
                  active: viewKind === 'group' && activePath === grandchild.path,
                  'is-drop': hoverPath === grandchild.path,
                }"
                role="treeitem"
                @contextmenu.prevent="emit('menu', $event, grandchild.path)"
                @dragenter="onDragOver($event, grandchild.path)"
                @dragover="onDragOver($event, grandchild.path)"
                @dragleave="onDragLeave($event, grandchild.path)"
                @drop="onDrop($event, grandchild.path)"
              >
                <span class="chev blank"></span>
                <button type="button" class="node-main" @click="open(grandchild.path)">
                  <span class="folder-icon" v-html="ICONS.folder"></span>
                  <span class="node-label">{{ grandchild.name }}</span>
                  <span class="node-count">{{ countOf(grandchild.path) }}</span>
                </button>
              </div>
            </template>
          </template>
        </template>
      </template>

      <p v-if="topGroups.length === 0" class="tree-empty">{{ t("nav.no-groups") }}</p>
    </div>

    <button
      type="button"
      class="new-folder"
      :disabled="!canCreate"
      :title="canCreate ? t('action.new-group') : t('group.new-subgroup-title')"
      @click="emit('new-folder')"
    >
      <span v-html="ICONS.plus"></span>
      {{ t("action.new-group") }}
    </button>

    <div v-if="storageText" class="storage" :title="storageHint">
      <span class="storage-dot"></span>
      <span class="storage-text">{{ storageText }}</span>
    </div>
  </aside>
</template>

<style scoped>
.tree {
  width: var(--sidebar-width, 248px);
  flex: 0 0 var(--sidebar-width, 248px);
  display: flex;
  flex-direction: column;
  min-height: 0;
  padding: 10px 12px 0 10px;
  background: var(--surface);
  border-right: 1px solid var(--line);
}

.quick {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.node {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 34px;
  padding: 0 8px;
  border-radius: 9px;
  color: var(--ink-2);
  cursor: pointer;
  user-select: none;
}

.node:hover {
  background: var(--surface-2);
}

.node.active {
  background: var(--accent-soft);
  color: var(--accent-strong);
}

.node-icon {
  width: 18px;
  height: 18px;
  flex: 0 0 18px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--ink-3);
}

.node-icon:deep(svg) {
  width: 17px;
  height: 17px;
}

.node.active .node-icon {
  color: var(--accent);
}

.node-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13.5px;
}

.node-count {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--ink-3);
  font-variant-numeric: tabular-nums;
}

.node.active .node-count {
  color: var(--accent);
}

.section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 14px;
  padding: 0 8px 6px;
  font-size: 11.5px;
  letter-spacing: 0.4px;
  color: var(--ink-3);
}

.section-add {
  width: 22px;
  height: 22px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  color: var(--ink-3);
}

.section-add:deep(svg) {
  width: 14px;
  height: 14px;
}

.section-add:hover {
  background: var(--surface-2);
  color: var(--accent);
}

.nav-tree {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding-bottom: 8px;
  /* 滚动条挪进侧栏的内边距里：负 margin 把它推到条目右侧留白处，
     padding 再把内容拉回原位，滚动条就不会压住文件夹名与计数 */
  margin-right: -12px;
  padding-right: 12px;
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: rgb(20 23 44 / 0.16) transparent;
}

/* 细滚动条：轨道透明，拇指内缩成圆角胶囊 */
.nav-tree::-webkit-scrollbar {
  width: 12px;
}

.nav-tree::-webkit-scrollbar-track {
  background: transparent;
}

.nav-tree::-webkit-scrollbar-thumb {
  border: 3px solid transparent;
  border-radius: 999px;
  background-clip: padding-box;
  background-color: rgb(20 23 44 / 0.16);
}

.nav-tree::-webkit-scrollbar-thumb:hover {
  background-color: rgb(20 23 44 / 0.28);
}

.node.is-group {
  gap: 4px;
  padding: 0 6px 0 2px;
}

.node.is-group.is-child {
  padding-left: 16px;
}

.node.is-group.is-grandchild {
  padding-left: 34px;
}

.node.is-drop {
  background: var(--accent-soft);
  box-shadow: inset 0 0 0 1.5px var(--accent);
}

.chev {
  width: 18px;
  height: 18px;
  flex: 0 0 18px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 5px;
  color: var(--ink-3);
}

.chev:deep(svg) {
  width: 14px;
  height: 14px;
  transition: transform 0.15s ease;
}

.chev.folded:deep(svg) {
  transform: rotate(-90deg);
}

.chev.blank {
  visibility: hidden;
}

.chev:hover {
  background: var(--surface-2);
}

.node-main {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  height: 34px;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.folder-icon {
  width: 18px;
  height: 18px;
  flex: 0 0 18px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: #e09b2d;
}

.folder-icon:deep(svg) {
  width: 17px;
  height: 17px;
}

.new-folder {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 32px;
  margin: 6px 2px 8px;
  border: 1px dashed var(--line);
  border-radius: 8px;
  color: var(--ink-3);
  font-size: 12.5px;
}

.new-folder:deep(svg) {
  width: 14px;
  height: 14px;
}

.new-folder:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}

.new-folder:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.tree-empty {
  margin: 4px 8px;
  color: var(--ink-3);
  font-size: 12.5px;
}

.storage {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 12px 8px;
  border-top: 1px solid var(--line);
  color: var(--ink-3);
  font-size: 11.5px;
}

.storage-dot {
  width: 6px;
  height: 6px;
  flex: 0 0 6px;
  border-radius: 50%;
  background: var(--green);
  box-shadow: 0 0 0 3px rgb(14 159 110 / 0.14);
}

.storage-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>