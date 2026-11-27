<script setup lang="ts">
/**
 * 侧栏：快捷入口（最近 / 收藏） + 文件夹树（任意深度）。
 *
 * 树只负责展示与交互，不发请求：展开 / 折叠与导航都通过事件交给 App。
 * 目录深度不限制：把 groups 拍平成带缩进深度的可见行，折叠的子树不渲染。
 */
import { computed } from "vue";

import { t } from "@/i18n";
import { ICONS } from "@/icons";
import type { GroupItem } from "@/types";

const props = defineProps<{
  groups: readonly GroupItem[];
  /** 每个文件夹子树（含后代）的文件数。 */
  counts: ReadonlyMap<string, number>;
  /** 根目录入口（全部文件）的文件数。 */
  allCount: number;
  recentCount: number;
  favoritesCount: number;
  /** 当前账号是否有收藏权限（fav)：没有就不显示收藏入口。 */
  canFavorite: boolean;
  /** 当前视图：根目录 / 快捷入口 / 文件夹。 */
  viewKind: "root" | "recent" | "favorites" | "group";
  /** 当前文件夹路径（viewKind = group 时有效）。 */
  activePath: string;
  /** 已折叠的文件夹路径（折叠后不渲染子级）。 */
  folded: ReadonlySet<string>;
  /** 侧栏底部的存储提示文案（空串表示不显示）。 */
  storageText: string;
  storageHint: string;
  /** 只读数据目录与收藏元数据此刻是否可用：不可用时底部圆点变红。 */
  storageHealthy: boolean;
}>();

const emit = defineEmits<{
  "show-root": [];
  "show-recent": [];
  "show-favorites": [];
  "open-folder": [path: string];
  toggle: [path: string];
}>();

interface TreeRow {
  group: GroupItem;
  depth: number;
  hasChildren: boolean;
  folded: boolean;
}

/** 把分组树拍平成可见行：父级在前、子级紧随其后；折叠的子树整段跳过。 */
const rows = computed<TreeRow[]>(() => {
  const byParent = new Map<string, GroupItem[]>();
  for (const group of props.groups) {
    const list = byParent.get(group.parent);
    if (list) list.push(group);
    else byParent.set(group.parent, [group]);
  }
  const result: TreeRow[] = [];
  const walk = (parent: string, depth: number): void => {
    for (const group of byParent.get(parent) ?? []) {
      const children = byParent.get(group.path) ?? [];
      const folded = props.folded.has(group.path);
      result.push({ group, depth, hasChildren: children.length > 0, folded });
      if (children.length > 0 && !folded) walk(group.path, depth + 1);
    }
  };
  walk("", 0);
  return result;
});

function countOf(path: string): number {
  return props.counts.get(path) ?? 0;
}
</script>

<template>
  <aside class="tree">
    <nav class="quick">
      <button
        type="button"
        class="node"
        :class="{ active: viewKind === 'root' }"
        @click="emit('show-root')"
      >
        <span class="node-icon" v-html="ICONS.home"></span>
        <span class="node-label">{{ t("nav.all") }}</span>
        <span class="node-count">{{ allCount }}</span>
      </button>
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
        v-if="canFavorite"
        type="button"
        class="node"
        :class="{ active: viewKind === 'favorites' }"
        @click="emit('show-favorites')"
      >
        <span class="node-icon" v-html="ICONS.star"></span>
        <span class="node-label">{{ t("nav.favorites") }}</span>
        <span class="node-count">{{ favoritesCount }}</span>
      </button>
    </nav>

    <div class="section-head">
      <span>{{ t("nav.tree") }}</span>
    </div>

    <div class="nav-tree" role="tree">
      <div
        v-for="row in rows"
        :key="row.group.path"
        class="node is-group"
        :class="{ active: viewKind === 'group' && activePath === row.group.path }"
        :style="{ paddingLeft: `${2 + row.depth * 16}px` }"
        role="treeitem"
        :aria-expanded="row.hasChildren ? !row.folded : undefined"
      >
        <button
          type="button"
          class="chev"
          :class="{ blank: !row.hasChildren, folded: row.folded }"
          :aria-label="row.folded ? t('action.expand-group') : t('action.collapse-group')"
          @click.stop="row.hasChildren && emit('toggle', row.group.path)"
          v-html="row.hasChildren ? ICONS.chevron : ''"
        ></button>
        <button type="button" class="node-main" @click="emit('open-folder', row.group.path)">
          <span class="folder-icon" v-html="ICONS.folder"></span>
          <span class="node-label">{{ row.group.name }}</span>
          <span class="node-count">{{ countOf(row.group.path) }}</span>
        </button>
      </div>

      <p v-if="rows.length === 0" class="tree-empty">{{ t("nav.no-groups") }}</p>
    </div>

    <div v-if="storageText" class="storage" :title="storageHint">
      <span class="storage-dot" :class="{ 'is-bad': !storageHealthy }"></span>
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

/* 只读目录或收藏元数据不可用：圆点变红 */
.storage-dot.is-bad {
  background: var(--rose);
  box-shadow: 0 0 0 3px rgb(225 29 72 / 0.14);
}

.storage-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 窄屏（触屏）：18px 的箭头太小，手指点偏会落到文件夹名上，变成打开目录。
   视觉尺寸不变，只把按钮命中区放大到 32px（行高 34px 放得下）。 */
@media (max-width: 900px) {
  .chev {
    width: 32px;
    height: 32px;
    flex: 0 0 32px;
    margin-left: -7px;
  }
}
</style>
