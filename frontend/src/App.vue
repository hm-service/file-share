<script setup lang="ts">
/**
 * 主页面（/）的界面壳：左侧导航（全部 / 收藏 / 分组） + 文件列表 + 浮动上传坞。
 *
 * 列表数据只在这里维护；每行的操作成功后重新拉一次列表，保持与服务端一致
 * （收藏 / 公开 / 分组是全局的，上传完成后也可能出现新记录）。
 *
 * 一级分组没有独立接口：分组名与段内顺序都来自每条文件的 group_name / sort_order，
 * 所以左栏的分组列表由文件集合现算。「全部」视图不分段、固定按上传时间倒序，不参与拖拽；
 * 收藏 / 单分组 / 未分组视图按 sort_order 排，可拖拽调整顺序（PUT /api/files/order）。
 */
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import Sortable from "sortablejs";

import FileList from "@/components/FileList.vue";
import LocaleSwitch from "@/components/LocaleSwitch.vue";
import UploadDock from "@/components/UploadDock.vue";
import {
  ApiError,
  deleteFile,
  downloadFile,
  listFiles,
  renameFile,
  renameGroup,
  reorderFiles,
  reorderGroups,
  toggleFavorite,
  togglePublic,
  updateFileGroup,
} from "@/api";
import { ICONS } from "@/icons";
import { t } from "@/i18n";
import { toLogin } from "@/session";
import type { FileItem } from "@/types";

const files = ref<FileItem[]>([]);
const loaded = ref(false);
const refreshing = ref(false);
let refreshSeq = 0;
const loadError = ref("");
const actionError = ref("");
let actionErrorTimer = 0;

/** 当前视图：全部 / 收藏 / 单个分组 / 未分组。 */
type ViewKind = "all" | "favorites" | "group" | "ungrouped";
const view = ref<{ kind: ViewKind; group?: string }>({ kind: "all" });
const query = ref("");

const uploadDock = ref<InstanceType<typeof UploadDock> | null>(null);

/* ---------- 分组 ---------- */

/** 分组名列表，顺序由服务端 group_order 决定（拖动侧栏 / chips 可调整）。 */
const groups = ref<string[]>([]);

const groupCounts = computed(() => {
  const counts = new Map<string, number>();
  for (const file of files.value) {
    if (file.group_name) counts.set(file.group_name, (counts.get(file.group_name) ?? 0) + 1);
  }
  return counts;
});

const ungroupedCount = computed(() => {
  let count = 0;
  for (const file of files.value) {
    if (!file.group_name) count += 1;
  }
  return count;
});

/** 段内顺序：sort_order 优先；同一序号（例如都还是初始值）时新的在前。 */
const sortedFiles = computed(() => {
  return [...files.value].sort((a, b) => {
    if (a.sort_order !== b.sort_order) return a.sort_order - b.sort_order;
    if (a.created_at !== b.created_at) return b.created_at - a.created_at;
    return a.id.localeCompare(b.id);
  });
});

const keyword = computed(() => query.value.trim().toLowerCase());
/** 搜索过滤中不允许拖拽：DOM 里只是子集，提交顺序会打乱整段。 */
const sortingDisabled = computed(() => keyword.value.length > 0);

function matchesQuery(file: FileItem): boolean {
  return !keyword.value || file.filename.toLowerCase().includes(keyword.value);
}

/** 「全部」视图的固定顺序：上传时间倒序，同一秒按 id 稳定排序；不参与拖拽。 */
const timeOrderedFiles = computed(() => {
  return [...files.value].sort((a, b) => {
    if (a.created_at !== b.created_at) return b.created_at - a.created_at;
    return a.id.localeCompare(b.id);
  });
});

/** 当前视图的可见文件：全部按时间倒序，其余按用户排定的 sort_order，再按关键词过滤。 */
const visibleFiles = computed(() => {
  const base = (isAll.value ? timeOrderedFiles.value : sortedFiles.value).filter(matchesQuery);
  switch (view.value.kind) {
    case "favorites":
      return base.filter((file) => file.is_favorite);
    case "group":
      return base.filter((file) => file.group_name === view.value.group);
    case "ungrouped":
      return base.filter((file) => !file.group_name);
    default:
      return base;
  }
});

const pageTitle = computed(() => {
  switch (view.value.kind) {
    case "favorites":
      return t("nav.favorites");
    case "ungrouped":
      return t("nav.ungrouped");
    case "group":
      return view.value.group ?? "";
    default:
      return t("nav.all-files");
  }
});
const pageCount = computed(() => visibleFiles.value.length);
const emptyKey = computed(() => {
  if (keyword.value) return "nav.no-results" as const;
  return view.value.kind === "favorites" ? ("nav.empty-favorites" as const) : ("nav.empty-all" as const);
});
const emptyIcon = computed(() => {
  if (view.value.kind === "favorites") return ICONS.star;
  if (view.value.kind === "group" || view.value.kind === "ungrouped") return ICONS.folder;
  return ICONS.file;
});

const isAll = computed(() => view.value.kind === "all");
const isFavorites = computed(() => view.value.kind === "favorites");
const isUngrouped = computed(() => view.value.kind === "ungrouped");
const isGroupView = computed(() => view.value.kind === "group");
const isGroupActive = (group: string): boolean =>
  view.value.kind === "group" && view.value.group === group;

/** 只有按 sort_order 排列的视图才开放拖拽；「全部」是固定时间序，搜索时 DOM 只是子集。 */
const canSort = computed(() => !sortingDisabled.value && !isAll.value);

/** 当前分组内的文件数（与搜索过滤无关）：为 0 时不显示「全部下载」。 */
const currentGroupCount = computed(() => {
  if (view.value.kind !== "group" || !view.value.group) return 0;
  return groupCounts.value.get(view.value.group) ?? 0;
});

function showAll(): void {
  view.value = { kind: "all" };
}

function showFavorites(): void {
  view.value = { kind: "favorites" };
}

function showGroup(group: string): void {
  // 拖拽排序松手后浏览器还会补一个 click，别让它顺手切换视图。
  if (Date.now() < groupClickGuardUntil) return;
  view.value = { kind: "group", group };
}

function showUngrouped(): void {
  view.value = { kind: "ungrouped" };
}

/** 文件被移走后当前分组可能消失：刷新后回退到「全部」，避免停在一个空视图。 */
function validateView(): void {
  if (view.value.kind === "group" && !groups.value.includes(view.value.group ?? "")) {
    view.value = { kind: "all" };
  }
  if (view.value.kind === "ungrouped" && ungroupedCount.value === 0) {
    view.value = { kind: "all" };
  }
}

/* ---------- 分组顺序拖拽 ---------- */

const navGroupsEl = ref<HTMLElement | null>(null);
const chipsGroupsEl = ref<HTMLElement | null>(null);
let navGroupSortable: Sortable | null = null;
let chipsGroupSortable: Sortable | null = null;
/** 拖拽结束后的短暂刻度：用来吞掉松手时补发的 click。 */
let groupClickGuardUntil = 0;

/** 侧栏与移动端 chips 共用一份排序配置，返回时把新顺序交给服务端。 */
function createGroupSortable(container: HTMLElement): Sortable {
  return Sortable.create(container, {
    animation: 150,
    draggable: "[data-group]",
    ghostClass: "group-drag-ghost",
    chosenClass: "group-drag-chosen",
    delay: 120,
    delayOnTouchOnly: true,
    touchStartThreshold: 4,
    onEnd: () => {
      groupClickGuardUntil = Date.now() + 250;
      const ordered = Array.from(container.querySelectorAll<HTMLElement>("[data-group]"))
        .map((node) => node.dataset.group ?? "")
        .filter((name) => name !== "");
      if (ordered.length !== groups.value.length) return;
      if (ordered.join("\n") === groups.value.join("\n")) return;
      void applyGroupOrder(ordered);
    },
  });
}

async function applyGroupOrder(ordered: string[]): Promise<void> {
  groups.value = ordered;
  try {
    await reorderGroups(ordered);
  } catch (error) {
    showActionError(error);
    await refresh();
  }
}

function mountGroupSortables(): void {
  if (navGroupsEl.value && !navGroupSortable) {
    navGroupSortable = createGroupSortable(navGroupsEl.value);
  }
  if (chipsGroupsEl.value && !chipsGroupSortable) {
    chipsGroupSortable = createGroupSortable(chipsGroupsEl.value);
  }
}

function destroyGroupSortables(): void {
  navGroupSortable?.destroy();
  navGroupSortable = null;
  chipsGroupSortable?.destroy();
  chipsGroupSortable = null;
}

/* ---------- 列表数据与操作 ---------- */

function isUnauthorized(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401;
}

function errorText(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  if (error instanceof Error && error.message) return error.message;
  return t("error.network");
}

function showActionError(error: unknown): void {
  if (isUnauthorized(error)) return; // api 客户端已经清会话跳登录页
  actionError.value = errorText(error);
  window.clearTimeout(actionErrorTimer);
  actionErrorTimer = window.setTimeout(() => {
    actionError.value = "";
  }, 5000);
}

/**
 * 拉列表；401 由 api 客户端处理，其余错误在内容区显示 error.load。
 *
 * 上传每完成一个文件就会触发一次刷新，手动刷新按钮也可能和它们撞在一起：
 * 用递增序号只采用最后一次请求的结果，避免旧响应后到把新列表覆盖回去。
 */
async function refresh(): Promise<void> {
  const seq = ++refreshSeq;
  refreshing.value = true;
  try {
    const payload = await listFiles();
    if (seq !== refreshSeq) return;
    files.value = payload.files;
    groups.value = payload.groups;
    loadError.value = "";
    validateView();
  } catch (error) {
    if (seq !== refreshSeq || isUnauthorized(error)) return;
    loadError.value = t("error.load");
  } finally {
    if (seq === refreshSeq) {
      refreshing.value = false;
      loaded.value = true;
    }
  }
}

async function onFavorite(file: FileItem): Promise<void> {
  try {
    await toggleFavorite(file.id);
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

async function onVisibility(file: FileItem): Promise<void> {
  try {
    await togglePublic(file.id);
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

async function onDownload(file: FileItem): Promise<void> {
  try {
    await downloadFile(file.id);
  } catch (error) {
    showActionError(error);
  }
}

/**
 * 分组「全部下载」：按当前分组顺序逐个换票据并触发下载，每个文件是独立的一次下载。
 * 浏览器可能询问「是否允许下载多个文件」，允许后即可一次保存整组。
 */
async function onDownloadGroup(): Promise<void> {
  if (view.value.kind !== "group") return;
  const groupName = view.value.group ?? "";
  const targets = sortedFiles.value.filter((file) => file.group_name === groupName);
  if (!targets.length) return;
  try {
    for (const file of targets) {
      await downloadFile(file.id);
    }
  } catch (error) {
    showActionError(error);
  }
}

/** 重命名：用 prompt 保持与「新建分组」一致的最小交互；取消 / 空名 / 同名都不发请求。 */
async function onRename(file: FileItem): Promise<void> {
  const input = window.prompt(t("rename.prompt"), file.filename);
  if (input === null) return;
  const filename = input.trim();
  if (!filename || filename === file.filename) return;
  try {
    await renameFile(file.id, filename);
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

async function onDelete(file: FileItem): Promise<void> {
  if (!window.confirm(t("confirm.delete", { name: file.filename }))) return;
  try {
    await deleteFile(file.id);
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

/** 重命名当前分组：批量改写组内文件的 group_name，成功后留在新分组视图。 */
async function onRenameGroup(): Promise<void> {
  const groupName = view.value.group;
  if (!groupName) return;
  const input = window.prompt(t("group.rename-prompt"), groupName);
  if (input === null) return;
  const newName = input.trim();
  if (!newName || newName === groupName) return;
  try {
    await renameGroup(groupName, newName);
    view.value = { kind: "group", group: newName };
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

/** 移动到分组（空串 = 未分组；新分组名会随着文件出现自动出现在左栏）。 */
async function onMoveGroup(file: FileItem, groupName: string): Promise<void> {
  if (file.group_name === groupName) return;
  try {
    await updateFileGroup(file.id, groupName);
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

/* ---------- 把文件拖到左栏分组标签 ---------- */

/**
 * FileList 在拖拽开始/结束时通过事件告知当前文件 id —— 比 dataTransfer 的类型更可靠，
 * SortableJS 的原生/降级模式都能用。groupDropTarget 用于落点高亮。
 */
const groupDropTarget = ref<string | null>(null);
const draggingFileId = ref<string | null>(null);

function startFileDrag(file: FileItem): void {
  draggingFileId.value = file.id;
}

function endFileDrag(): void {
  draggingFileId.value = null;
  groupDropTarget.value = null;
}

function onGroupDragEnter(event: DragEvent, groupName: string): void {
  if (draggingFileId.value === null) return;
  event.preventDefault();
  groupDropTarget.value = groupName;
}

function onGroupDragOver(event: DragEvent, groupName: string): void {
  if (draggingFileId.value === null) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
  groupDropTarget.value = groupName;
}

function onGroupDragLeave(event: DragEvent, groupName: string): void {
  const current = event.currentTarget as HTMLElement;
  if (event.relatedTarget instanceof Node && current.contains(event.relatedTarget)) return;
  if (groupDropTarget.value === groupName) groupDropTarget.value = null;
}

/** 松手即改分组；空串表示移入未分组。 */
async function onGroupDrop(event: DragEvent, groupName: string): Promise<void> {
  event.preventDefault();
  const fileId = draggingFileId.value ?? "";
  draggingFileId.value = null;
  groupDropTarget.value = null;
  const file = files.value.find((entry) => entry.id === fileId);
  if (!file || file.group_name === groupName) return;
  try {
    await updateFileGroup(fileId, groupName);
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

/** 拖拽后的新顺序先落到本地（Vue 重新渲染时保持 DOM 已有顺序），再提交服务端。 */
function applyOrder(orderedIds: string[]): void {
  const nextOrder = new Map(orderedIds.map((id, index) => [id, index] as const));
  for (const file of files.value) {
    const order = nextOrder.get(file.id);
    if (order !== undefined) file.sort_order = order;
  }
}

async function onReorder(orderedIds: string[]): Promise<void> {
  applyOrder(orderedIds);
  try {
    await reorderFiles(orderedIds.map((id, index) => ({ id, sort_order: index })));
  } catch (error) {
    showActionError(error);
    // 回滚：重新拉服务端顺序（刷新也会恢复分组计数等派生数据）
    await refresh();
  }
}

function logout(): void {
  toLogin();
}

function pickFiles(): void {
  uploadDock.value?.pick();
}

/* ---------- 整页拖放 ---------- */

const dropDepth = ref(0);
const dropActive = computed(() => dropDepth.value > 0);

function hasFiles(event: DragEvent): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes("Files");
}

function onDragEnter(event: DragEvent): void {
  if (!hasFiles(event)) return;
  event.preventDefault();
  dropDepth.value += 1;
}

function onDragOver(event: DragEvent): void {
  if (!hasFiles(event)) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
}

function onDragLeave(): void {
  if (dropDepth.value > 0) dropDepth.value -= 1;
}

function onDrop(event: DragEvent): void {
  event.preventDefault();
  dropDepth.value = 0;
  uploadDock.value?.addFiles(event.dataTransfer?.files);
}

/** 拖到页面任何地方都吞掉默认行为，免得浏览器直接打开文件、把页面顶掉。 */
function swallowDrop(event: DragEvent): void {
  if (hasFiles(event)) event.preventDefault();
}

onMounted(() => {
  document.addEventListener("dragover", swallowDrop);
  document.addEventListener("drop", swallowDrop);
  document.addEventListener("dragend", endFileDrag);
  mountGroupSortables();
  void refresh();
});

onBeforeUnmount(() => {
  document.removeEventListener("dragover", swallowDrop);
  document.removeEventListener("drop", swallowDrop);
  document.removeEventListener("dragend", endFileDrag);
  destroyGroupSortables();
  window.clearTimeout(actionErrorTimer);
});
</script>

<template>
  <div class="shell">
    <!-- 桌面端侧栏：品牌 / 上传入口 / 视图切换 / 分组 / 语言与退出 -->
    <aside class="sidebar">
      <div class="brand">
        <span class="brand-mark" v-html="ICONS.upload"></span>
        <div class="brand-text">
          <p class="brand-name">{{ t("app.title") }}</p>
          <p class="brand-sub">{{ t("app.tagline") }}</p>
        </div>
      </div>

      <button type="button" class="upload-primary" @click="pickFiles">
        <span class="upload-primary-icon" v-html="ICONS.upload"></span>
        {{ t("upload.button") }}
      </button>
      <p class="upload-hint">{{ t("upload.hint") }}</p>

      <nav class="nav" :aria-label="t('nav.all-files')">
        <button type="button" class="nav-item" :class="{ active: isAll }" :aria-current="isAll ? 'page' : undefined" @click="showAll">
          <span class="nav-icon" v-html="ICONS.file"></span>
          <span class="nav-label">{{ t("nav.all-files") }}</span>
          <span class="nav-count">{{ files.length }}</span>
        </button>
        <button
          type="button"
          class="nav-item"
          :class="{ active: isFavorites }"
          :aria-current="isFavorites ? 'page' : undefined"
          @click="showFavorites"
        >
          <span class="nav-icon" v-html="ICONS.star"></span>
          <span class="nav-label">{{ t("nav.favorites") }}</span>
          <span class="nav-count">{{ files.filter((file) => file.is_favorite).length }}</span>
        </button>
      </nav>

      <div class="nav-sep"></div>
      <p class="nav-section-title">{{ t("nav.groups") }}</p>

      <!-- 分组可拖拽排序：未分组不是真实分组，放在排序容器外，固定排在最后 -->
      <div ref="navGroupsEl" class="nav nav-groups">
        <button
          v-for="group in groups"
          :key="group"
          type="button"
          class="nav-item nav-item-group"
          :data-group="group"
          :class="{ active: isGroupActive(group), 'is-drop-target': groupDropTarget === group }"
          :aria-current="isGroupActive(group) ? 'page' : undefined"
          @click="showGroup(group)"
          @dragenter="onGroupDragEnter($event, group)"
          @dragover="onGroupDragOver($event, group)"
          @dragleave="onGroupDragLeave($event, group)"
          @drop="onGroupDrop($event, group)"
        >
          <span class="nav-icon" v-html="ICONS.folder"></span>
          <span class="nav-label">{{ group }}</span>
          <span class="nav-count">{{ groupCounts.get(group) ?? 0 }}</span>
        </button>
        <p v-if="!groups.length" class="nav-empty">{{ t("nav.no-groups") }}</p>
      </div>
      <div v-if="ungroupedCount > 0" class="nav nav-ungrouped">
        <button
          type="button"
          class="nav-item nav-item-group"
          :class="{ active: isUngrouped, 'is-drop-target': groupDropTarget === '' }"
          :aria-current="isUngrouped ? 'page' : undefined"
          @click="showUngrouped"
          @dragenter="onGroupDragEnter($event, '')"
          @dragover="onGroupDragOver($event, '')"
          @dragleave="onGroupDragLeave($event, '')"
          @drop="onGroupDrop($event, '')"
        >
          <span class="nav-icon" v-html="ICONS.inbox"></span>
          <span class="nav-label">{{ t("nav.ungrouped") }}</span>
          <span class="nav-count">{{ ungroupedCount }}</span>
        </button>
      </div>

      <div class="sidebar-foot">
        <LocaleSwitch />
        <button type="button" class="logout-btn" @click="logout">
          <span class="logout-icon" v-html="ICONS.logout"></span>
          {{ t("action.logout") }}
        </button>
      </div>
    </aside>

    <main
      class="main"
      :class="{ 'is-drop-target': dropActive }"
      @dragenter="onDragEnter"
      @dragover="onDragOver"
      @dragleave="onDragLeave"
      @drop="onDrop"
    >
      <!-- 窄屏顶栏：品牌 + 语言 + 上传 + 退出 -->
      <header class="mobile-bar">
        <div class="brand brand-mobile">
          <span class="brand-mark" v-html="ICONS.upload"></span>
          <p class="brand-name">{{ t("app.title") }}</p>
        </div>
        <div class="mobile-actions">
          <LocaleSwitch />
          <button
            type="button"
            class="mobile-upload"
            :title="t('upload.button')"
            :aria-label="t('upload.button')"
            v-html="ICONS.upload"
            @click="pickFiles"
          ></button>
          <button
            type="button"
            class="mobile-logout"
            :title="t('action.logout')"
            :aria-label="t('action.logout')"
            v-html="ICONS.logout"
            @click="logout"
          ></button>
        </div>
      </header>

      <header class="page-head">
        <div class="page-titles">
          <div class="page-title-line">
            <h1 class="page-title">{{ pageTitle }}</h1>
            <button
              v-if="isGroupView"
              type="button"
              class="title-action"
              :title="t('action.rename-group')"
              :aria-label="t('action.rename-group')"
              @click="onRenameGroup"
            >
              <span v-html="ICONS.pencil"></span>
            </button>
          </div>
          <p class="page-sub">{{ t("nav.file-count", { count: pageCount }) }}</p>
        </div>
        <div class="page-actions">
          <!-- 只看单个分组时提供「全部下载」；「全部 / 收藏 / 未分组」没有稳定的集合边界，不提供。 -->
          <button
            v-if="isGroupView && currentGroupCount > 0"
            type="button"
            class="group-download"
            :title="t('action.download-group')"
            @click="onDownloadGroup"
          >
            <span class="group-download-icon" v-html="ICONS.download"></span>
            {{ t("action.download-group") }}
          </button>
          <label class="search">
            <span class="search-icon" v-html="ICONS.search"></span>
            <input
              v-model="query"
              type="search"
              class="search-input"
              :placeholder="t('nav.search-placeholder')"
              :aria-label="t('nav.search-placeholder')"
            />
          </label>
          <button
            type="button"
            class="refresh-btn"
            :class="{ spinning: refreshing }"
            :title="t('action.refresh')"
            :aria-label="t('action.refresh')"
            :disabled="refreshing"
            @click="refresh"
          >
            <span v-html="ICONS.refresh"></span>
          </button>
        </div>
      </header>

      <!-- 窄屏用标签页切换视图；桌面端用侧栏导航 -->
      <div class="tabs" role="tablist" :aria-label="t('nav.all-files')">
        <button
          type="button"
          class="tab"
          role="tab"
          :class="{ active: isAll }"
          :aria-selected="isAll"
          @click="showAll"
        >
          {{ t("nav.all-files") }}
          <span class="tab-count">{{ files.length }}</span>
        </button>
        <button
          type="button"
          class="tab"
          role="tab"
          :class="{ active: isFavorites }"
          :aria-selected="isFavorites"
          @click="showFavorites"
        >
          {{ t("nav.favorites") }}
          <span class="tab-count">{{ files.filter((file) => file.is_favorite).length }}</span>
        </button>
      </div>

      <!-- 窄屏分组 chips：分组横向滚动、可拖拽排序；未分组固定在右侧 -->
      <div
        v-show="groups.length || ungroupedCount > 0"
        class="chips"
        role="tablist"
        :aria-label="t('nav.groups')"
      >
        <div ref="chipsGroupsEl" class="chips-groups">
          <button
            v-for="group in groups"
            :key="group"
            type="button"
            class="chip"
            role="tab"
            :data-group="group"
            :class="{ active: isGroupActive(group), 'is-drop-target': groupDropTarget === group }"
            :aria-selected="isGroupActive(group)"
            @click="showGroup(group)"
            @dragenter="onGroupDragEnter($event, group)"
            @dragover="onGroupDragOver($event, group)"
            @dragleave="onGroupDragLeave($event, group)"
            @drop="onGroupDrop($event, group)"
          >
            <span class="chip-icon" v-html="ICONS.folder"></span>
            <span class="chip-label">{{ group }}</span>
          </button>
        </div>
        <button
          v-if="ungroupedCount > 0"
          type="button"
          class="chip chip-ungrouped"
          role="tab"
          :class="{ active: isUngrouped, 'is-drop-target': groupDropTarget === '' }"
          :aria-selected="isUngrouped"
          @click="showUngrouped"
          @dragenter="onGroupDragEnter($event, '')"
          @dragover="onGroupDragOver($event, '')"
          @dragleave="onGroupDragLeave($event, '')"
          @drop="onGroupDrop($event, '')"
        >
          <span class="chip-icon" v-html="ICONS.inbox"></span>
          <span class="chip-label">{{ t("nav.ungrouped") }}</span>
        </button>
      </div>

      <section class="content">
        <div v-if="loadError || actionError" class="banners">
          <p v-if="loadError" class="banner" role="alert">{{ loadError }}</p>
          <p v-if="actionError" class="banner" role="alert">{{ actionError }}</p>
        </div>

        <!-- 全部视图按上传时间倒序平铺；收藏 / 分组 / 未分组按 sort_order 平铺 -->
        <FileList
          v-if="visibleFiles.length"
          :files="visibleFiles"
          :groups="groups"
          :sortable="canSort"
          @favorite="onFavorite"
          @download="onDownload"
          @rename="onRename"
          @visibility="onVisibility"
          @delete="onDelete"
          @move-group="onMoveGroup"
          @reorder="onReorder"
          @drag-start="startFileDrag"
          @drag-end="endFileDrag"
        />
        <div v-else class="empty">
          <span class="empty-icon" v-html="emptyIcon"></span>
          <p class="empty-title">{{ loaded ? t(emptyKey) : t("state.loading") }}</p>
          <template v-if="loaded && !query.trim() && (isAll || isFavorites)">
            <p v-if="isAll" class="empty-text">
              <span class="desktop-only">{{ t("upload.hint") }}</span>
              <span class="mobile-only">{{ t("upload.hint-mobile") }}</span>
            </p>
            <button type="button" class="upload-secondary" @click="pickFiles">
              <span class="upload-secondary-icon" v-html="ICONS.upload"></span>
              {{ t("upload.button") }}
            </button>
          </template>
        </div>
      </section>

      <div v-if="dropActive" class="drop-overlay" aria-hidden="true">
        <span class="drop-overlay-icon" v-html="ICONS.upload"></span>
        <p class="drop-overlay-text">{{ t("upload.drop-overlay") }}</p>
      </div>
    </main>

    <UploadDock ref="uploadDock" @uploaded="refresh" />
  </div>
</template>
