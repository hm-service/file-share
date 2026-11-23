<script setup lang="ts">
/**
 * 主应用壳：顶栏（搜索 / 当前账号） + 左侧目录树 + 内容区表格（当前目录的子目录与文件）。
 *
 * 只读视图：数据源是 SMB 共享的只读目录，界面只提供浏览、搜索、收藏、下载与分享链接，
 * 没有上传、删除、重命名、移动与排序。
 *
 * 视图与地址栏一一对应：`/`「最近」、`/favorites`「收藏」、`/<任意深度路径>`「文件夹」。
 * 切换视图用 pushState 进历史，前进 / 后退与直接打开链接都通过 routes.ts 还原；搜索词写在 `?q=` 里。
 *
 * 列表数据只在这里维护；每次操作成功后重新拉一次列表，保持与服务端一致。
 * 目录树的展开 / 折叠状态存 localStorage，刷新后保持。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";

import ContentTable from "@/components/ContentTable.vue";
import DirTree from "@/components/DirTree.vue";
import LocaleSwitch from "@/components/LocaleSwitch.vue";
import { ApiError, downloadFile, getMe, getStorageInfo, listFiles, toggleFavorite } from "@/api";
import { formatSize } from "@/format";
import { ICONS } from "@/icons";
import { t } from "@/i18n";
import { viewFromPath, viewPath, type View } from "@/routes";
import { toLogin } from "@/session";
import type { DirEntry, FileItem, GroupItem, StorageInfo, UserInfo } from "@/types";

/** 「最近」最多列这么多条；搜索时不受限制。 */
const RECENT_LIMIT = 20;
const SEARCH_PARAM = "q";
const FOLDED_KEY = "file-share.folded-folders";

/* ---------- 数据 ---------- */

const files = ref<FileItem[]>([]);
const groups = ref<GroupItem[]>([]);
const loaded = ref(false);
const refreshing = ref(false);
const loadError = ref("");
const actionError = ref("");
let actionErrorTimer = 0;
let refreshSeq = 0;
const currentUser = ref<UserInfo | null>(null);
/** 当前账号能否收藏：没有 fav 权限时星标与收藏入口都不显示。 */
const canFavorite = computed(() => currentUser.value?.permissions.includes("fav") ?? false);
/** 顶部账号提示：受限账号直接列出可见目录，方便确认自己处在什么范围。 */
const userAccessHint = computed(() => {
  const user = currentUser.value;
  if (!user || (user.access.length === 1 && user.access[0] === "/")) return user?.name ?? "";
  return `${user.name} · ${user.access.join(", ")}`;
});
const storage = ref<StorageInfo | null>(null);
const query = ref("");
const view = ref<View>({ kind: "recent" });
/** 窄屏抽屉：侧栏在移动端默认收起。 */
const sidebarOpen = ref(false);

/* ---------- 视图与派生数据 ---------- */

/** 当前文件夹路径；非文件夹视图为空串。 */
const currentPath = computed(() => (view.value.kind === "group" ? view.value.group : ""));
const keyword = computed(() => query.value.trim().toLowerCase());
const searching = computed(() => keyword.value.length > 0);
const viewKind = computed(() => view.value.kind);

function subtreeFiles(path: string): FileItem[] {
  return files.value.filter(
    (file) => file.group_path === path || file.group_path.startsWith(`${path}/`),
  );
}

/** 每个文件夹子树（含后代）的文件数，给侧栏徽标与文件夹行用。 */
const counts = computed<Map<string, number>>(() => {
  const map = new Map<string, number>();
  for (const group of groups.value) map.set(group.path, subtreeFiles(group.path).length);
  return map;
});

/** 当前视图要列出的文件：搜索时跨全部目录，按时间倒序。 */
const visibleFiles = computed<FileItem[]>(() => {
  const all = files.value;
  if (searching.value) {
    return all
      .filter((file) => file.filename.toLowerCase().includes(keyword.value))
      .sort((a, b) => b.created_at - a.created_at);
  }
  switch (view.value.kind) {
    case "recent":
      return [...all].sort((a, b) => b.created_at - a.created_at).slice(0, RECENT_LIMIT);
    case "favorites":
      return all.filter((file) => file.is_favorite).sort((a, b) => b.created_at - a.created_at);
    case "group": {
      const current = view.value.group;
      return all.filter((file) => file.group_path === current).sort(byName);
    }
  }
});

function byName(a: FileItem, b: FileItem): number {
  return a.filename.localeCompare(b.filename, undefined, { sensitivity: "base" });
}

/** 内容区里的子文件夹行：只有目录视图（且不在搜索）才列子目录。 */
const dirs = computed<DirEntry[]>(() => {
  if (searching.value || view.value.kind !== "group") return [];
  const parent = view.value.group;
  return groups.value
    .filter((group) => group.parent === parent)
    .map((group) => ({ path: group.path, name: group.name, fileCount: counts.value.get(group.path) ?? 0 }));
});

const pageFilesSize = computed(() => visibleFiles.value.reduce((sum, file) => sum + file.size, 0));

/** 最近与搜索结果是跨目录的，行内要标出文件所在目录。 */
const showGroup = computed(() => searching.value || view.value.kind === "recent");

/** 搜索时面包屑只显示「搜索结果」，不显示当前目录路径。 */
const visibleCrumbs = computed(() => (searching.value ? [] : crumbs.value));

/** 面包屑：目录视图逐级展开；其它视图只有当前项。 */
const crumbs = computed<{ name: string; path: string }[]>(() => {
  if (view.value.kind !== "group") return [];
  const segments = view.value.group.split("/");
  return segments.map((name, index) => ({
    name,
    path: segments.slice(0, index + 1).join("/"),
  }));
});

/** 顶层面包屑文案：目录视图显示「全部」，其它视图直接显示视图名。 */
const rootLabel = computed(() => {
  if (searching.value) return t("content.search-results");
  if (view.value.kind === "recent") return t("nav.recent");
  if (view.value.kind === "favorites") return t("nav.favorites");
  return t("nav.all");
});

/** 当前视图的「全部下载」提示：下载当前可见文件（搜索结果 / 收藏 / 目录）。 */
const downloadAllCount = computed(() => visibleFiles.value.length);

/* ---------- 侧栏折叠状态 ---------- */

function loadFolded(): Set<string> | null {
  try {
    const raw = localStorage.getItem(FOLDED_KEY);
    if (raw === null) return null;
    const parsed: unknown = JSON.parse(raw);
    return new Set(Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === "string") : []);
  } catch {
    return null;
  }
}

function saveFolded(next: Set<string>): void {
  try {
    localStorage.setItem(FOLDED_KEY, JSON.stringify([...next]));
  } catch {
    /* 存不下就只在本次会话里生效 */
  }
}

const storedFolded = loadFolded();
const folded = ref<Set<string>>(storedFolded ?? new Set());
let foldedInitialized = storedFolded !== null;

/** 默认只展开到第二级：二级目录全部折叠，三级不占侧栏空间；用户展开后会记住自己的选择。 */
function applyDefaultFolded(): void {
  if (foldedInitialized) return;
  foldedInitialized = true;
  const next = new Set(
    groups.value.filter((group) => group.path.split("/").length === 2).map((group) => group.path),
  );
  folded.value = next;
  saveFolded(next);
}

function toggleFolder(path: string): void {
  const next = new Set(folded.value);
  if (next.has(path)) next.delete(path);
  else next.add(path);
  folded.value = next;
  saveFolded(next);
  sidebarOpen.value = false;
}

/* ---------- 地址栏 ---------- */

function syncLocation(mode: "push" | "replace"): void {
  const params = new URLSearchParams();
  const search = query.value.trim();
  if (search) params.set(SEARCH_PARAM, search);
  const queryString = params.toString();
  const url = `${viewPath(view.value)}${queryString ? `?${queryString}` : ""}`;
  if (location.pathname + location.search === url) return;
  if (mode === "push") history.pushState(null, "", url);
  else history.replaceState(null, "", url);
}

/** 切换视图并同步地址栏：点导航用 push，回退 / 修正失效链接用 replace。 */
function applyView(next: View, mode: "push" | "replace" = "push"): void {
  view.value = next;
  sidebarOpen.value = false;
  syncLocation(mode);
}

/** 按地址栏还原视图与搜索词（首次加载与前进 / 后退都走这里）。 */
function syncViewFromLocation(): void {
  const params = new URLSearchParams(location.search);
  query.value = params.get(SEARCH_PARAM) ?? "";
  const next = viewFromPath(location.pathname);
  if (!next) {
    view.value = { kind: "recent" };
    sidebarOpen.value = false;
    syncLocation("replace");
    return;
  }
  view.value = next;
  sidebarOpen.value = false;
  syncLocation("replace");
  if (loaded.value) validateView();
}

/** 目录不存在（或账号没有收藏权限）时回退「最近」，免得刷新后停在空视图。 */
function validateView(): void {
  const current = view.value;
  if (current.kind === "group" && !groups.value.some((group) => group.path === current.group)) {
    applyView({ kind: "recent" }, "replace");
  }
  if (current.kind === "favorites" && !canFavorite.value) {
    applyView({ kind: "recent" }, "replace");
  }
}

watch([query, view], () => syncLocation("replace"));

function showRecent(): void {
  applyView({ kind: "recent" });
}

function showFavorites(): void {
  if (!canFavorite.value) return;
  applyView({ kind: "favorites" });
}

function openFolder(path: string): void {
  applyView({ kind: "group", group: path });
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
  // 操作失败可能是只读目录 / 收藏元数据不可用：顺手刷新一次状态，让侧栏提示跟上。
  void loadStorage();
}

/** 拉当前用户；401 由 api 客户端跳登录页。 */
async function loadMe(): Promise<void> {
  try {
    currentUser.value = await getMe();
  } catch {
    currentUser.value = null;
  }
}

/** 拉存储状态给侧栏提示用；失败只影响这行提示，不影响列表与操作。 */
async function loadStorage(): Promise<void> {
  try {
    storage.value = await getStorageInfo();
  } catch {
    storage.value = null;
  }
}

const storageText = computed(() => {
  const info = storage.value;
  if (!info) return "";
  const detail = t("storage.readonly");
  return info.healthy ? detail : `${detail}${t("storage.unreachable")}`;
});

const storageHint = computed(() => {
  const info = storage.value;
  if (!info) return "";
  if (!info.healthy && info.error) return info.error;
  return t("storage.readonly-hint");
});

const storageHealthy = computed(() => storage.value?.healthy !== false);

/**
 * 拉列表；401 由 api 客户端处理，其余错误在内容区显示 error.load。
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
    applyDefaultFolded();
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
    const next = await toggleFavorite(file.id, !file.is_favorite);
    const target = files.value.find((entry) => entry.id === file.id);
    if (target) target.is_favorite = next.is_favorite;
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

/** 逐个签下载地址并触发下载；浏览器可能询问是否允许下载多个文件。 */
async function onDownloadAll(): Promise<void> {
  const targets = visibleFiles.value;
  try {
    for (const file of targets) await downloadFile(file.id);
  } catch (error) {
    showActionError(error);
  }
}

function logout(): void {
  toLogin();
}

/* ---------- 侧栏宽度与窗口尺寸 ---------- */

const SIDEBAR_DEFAULT_WIDTH = 248;
const SIDEBAR_MIN_WIDTH = 200;
const SIDEBAR_MAX_RATIO = 0.5;

function maxSidebarWidth(): number {
  return Math.max(SIDEBAR_MIN_WIDTH, Math.floor(window.innerWidth * SIDEBAR_MAX_RATIO));
}

function clampSidebarWidth(width: number): number {
  return Math.min(Math.max(Math.round(width), SIDEBAR_MIN_WIDTH), maxSidebarWidth());
}

/** 侧栏宽度只存内存：刷新或重开页面回到默认宽度。 */
const sidebarWidth = ref(SIDEBAR_DEFAULT_WIDTH);
const resizingSidebar = ref(false);
let stopSidebarResize: (() => void) | null = null;

/**
 * 按住两栏边界的 grip 拖动：宽度实时跟随指针，只在本次会话内生效。
 * 移动 / 抬手监听挂在 window 上，指针滑出 grip 也能继续拖。
 */
function startSidebarResize(event: PointerEvent): void {
  if (event.button !== 0 || resizingSidebar.value) return;
  event.preventDefault();
  const startX = event.clientX;
  const startWidth = sidebarWidth.value;
  resizingSidebar.value = true;

  const onMove = (moveEvent: PointerEvent): void => {
    sidebarWidth.value = clampSidebarWidth(startWidth + moveEvent.clientX - startX);
  };
  const finish = (): void => {
    resizingSidebar.value = false;
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", finish);
    window.removeEventListener("pointercancel", finish);
    stopSidebarResize = null;
  };
  stopSidebarResize = finish;
  window.addEventListener("pointermove", onMove);
  window.addEventListener("pointerup", finish);
  window.addEventListener("pointercancel", finish);
}

/** 窄屏断点与样式里的 @media (max-width: 900px) 保持一致。 */
const NARROW_QUERY = window.matchMedia("(max-width: 900px)");
const isNarrow = ref(NARROW_QUERY.matches);

/** 窗口变窄时把侧栏夹回上限以内（宽度只存内存，不覆盖任何持久值）。 */
function onWindowResize(): void {
  sidebarWidth.value = clampSidebarWidth(sidebarWidth.value);
  isNarrow.value = NARROW_QUERY.matches;
}

onMounted(() => {
  window.addEventListener("popstate", syncViewFromLocation);
  window.addEventListener("resize", onWindowResize);
  syncViewFromLocation();
  void loadMe();
  void refresh();
  void loadStorage();
  // 定时重新探测存储状态：SMB 卷恢复后侧栏提示能自己变回绿色，不用等用户操作。
  storageTimer = window.setInterval(() => {
    if (document.visibilityState === "visible") void loadStorage();
  }, 30000);
});

let storageTimer = 0;

onBeforeUnmount(() => {
  window.removeEventListener("popstate", syncViewFromLocation);
  window.removeEventListener("resize", onWindowResize);
  window.clearInterval(storageTimer);
  stopSidebarResize?.();
  window.clearTimeout(actionErrorTimer);
});
</script>

<template>
  <div class="app">
    <header class="topbar">
      <button
        type="button"
        class="hamburger"
        :aria-label="t('nav.tree')"
        aria-haspopup="dialog"
        @click="sidebarOpen = true"
        v-html="ICONS.menu"
      ></button>

      <label class="search">
        <span class="search-icon" v-html="ICONS.search"></span>
        <input
          v-model="query"
          type="search"
          :placeholder="t('nav.search-placeholder')"
          :aria-label="t('nav.search-placeholder')"
        />
      </label>

      <div class="top-actions">
        <LocaleSwitch class="lang" />
        <!-- 当前账号：右上角显示名字；只读视图没有写操作 -->
        <div v-if="currentUser" class="user-chip" :title="userAccessHint">
          <span class="user-avatar" v-html="ICONS.user"></span>
          <span class="user-name">{{ currentUser.name }}</span>
          <span class="user-badge">{{ t("user.readonly") }}</span>
        </div>
        <button
          type="button"
          class="icon-btn logout"
          :title="t('action.logout')"
          :aria-label="t('action.logout')"
          v-html="ICONS.logout"
          @click="logout"
        ></button>
      </div>
    </header>

    <div
      class="body"
      :class="{ 'is-resizing': resizingSidebar }"
      :style="{ '--sidebar-width': `${sidebarWidth}px` }"
    >
      <div class="sidebar-wrap" :class="{ 'is-open': sidebarOpen }">
        <DirTree
          :groups="groups"
          :counts="counts"
          :recent-count="files.length"
          :favorites-count="files.filter((file) => file.is_favorite).length"
          :can-favorite="canFavorite"
          :view-kind="viewKind"
          :active-path="currentPath"
          :folded="folded"
          :storage-text="storageText"
          :storage-hint="storageHint"
          :storage-healthy="storageHealthy"
          @show-recent="showRecent"
          @show-favorites="showFavorites"
          @open-folder="openFolder"
          @toggle="toggleFolder"
        />
      </div>

      <!-- 两栏边界的 grip：只覆盖边界几像素，按住左右拖动调侧栏宽度 -->
      <div
        class="sidebar-grip"
        :class="{ 'is-active': resizingSidebar }"
        role="separator"
        aria-orientation="vertical"
        :aria-label="t('nav.resize-sidebar')"
        :title="t('nav.resize-sidebar')"
        @pointerdown="startSidebarResize"
      ></div>

      <div v-if="sidebarOpen" class="drawer-mask" @click="sidebarOpen = false"></div>

      <main class="content">
        <div class="toolbar">
          <div class="crumbs">
            <button
              type="button"
              class="crumb"
              :class="{ current: viewKind !== 'group' }"
              @click="searching ? (query = '') : showRecent()"
            >
              {{ rootLabel }}
            </button>
            <template v-for="(crumb, index) in visibleCrumbs" :key="crumb.path">
              <span class="crumb-sep" v-html="ICONS.chevron"></span>
              <button
                type="button"
                class="crumb"
                :class="{ current: index === visibleCrumbs.length - 1 }"
                @click="openFolder(crumb.path)"
              >
                {{ crumb.name }}
              </button>
            </template>
          </div>

          <div class="toolbar-right">
            <span class="meta">
              <template v-if="searching">{{ t("content.search-count", { count: visibleFiles.length }) }}</template>
              <template v-else-if="viewKind === 'group'">
                {{ t("content.folder-count", { count: dirs.length }) }} ·
                {{ t("nav.file-count", { count: visibleFiles.length }) }}
              </template>
              <template v-else>{{ t("nav.file-count", { count: visibleFiles.length }) }}</template>
            </span>

            <button
              v-if="visibleFiles.length"
              type="button"
              class="btn download-all"
              :disabled="refreshing"
              :title="t('action.download-group-hint', { count: visibleFiles.length })"
              @click="onDownloadAll"
            >
              <span class="btn-icon" v-html="ICONS.download"></span>
              {{ t("action.download-group-size", { size: formatSize(pageFilesSize) }) }}
            </button>
          </div>
        </div>

        <ContentTable
          v-if="dirs.length || visibleFiles.length"
          :dirs="dirs"
          :files="visibleFiles"
          :show-group="showGroup"
          :can-favorite="canFavorite"
          @open-folder="openFolder"
          @favorite="onFavorite"
          @download="onDownload"
          @error="showActionError"
        />

        <div v-else class="empty">
          <span class="empty-icon" v-html="searching ? ICONS.search : ICONS.folder"></span>
          <p class="empty-title">
            <template v-if="!loaded">{{ t("state.loading") }}</template>
            <template v-else-if="loadError">{{ loadError }}</template>
            <template v-else-if="searching">{{ t("nav.no-results") }}</template>
            <template v-else-if="viewKind === 'favorites'">{{ t("nav.empty-favorites") }}</template>
            <template v-else-if="viewKind === 'group'">{{ t("content.empty-folder") }}</template>
            <template v-else>{{ t("nav.empty-recent") }}</template>
          </p>
          <p v-if="loaded && !searching" class="empty-text">{{ t("content.empty-hint") }}</p>
        </div>

        <div v-if="actionError" class="toast" role="status">{{ actionError }}</div>
      </main>
    </div>

    <!-- 窄屏：没有工具栏的空间放「下载全部」，改成右下角悬浮按钮 -->
    <button
      v-if="isNarrow && visibleFiles.length"
      type="button"
      class="fab"
      :title="t('action.download-group-hint', { count: downloadAllCount })"
      :aria-label="t('action.download-group-size', { size: formatSize(pageFilesSize) })"
      @click="onDownloadAll"
    >
      <span class="btn-icon" v-html="ICONS.download"></span>
    </button>
  </div>
</template>

<style scoped>
.app {
  display: flex;
  flex-direction: column;
  height: 100vh;
  overflow: hidden;
}

/* ---------- 顶栏 ---------- */

.topbar {
  flex: 0 0 58px;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 0 16px;
  background: var(--surface);
  border-bottom: 1px solid var(--line);
  z-index: 20;
}

.hamburger {
  display: none;
  width: 34px;
  height: 34px;
  align-items: center;
  justify-content: center;
  border-radius: 9px;
  color: var(--ink-2);
}

.hamburger:deep(svg) {
  width: 19px;
  height: 19px;
}

.search {
  position: relative;
  width: 320px;
  margin-left: 6px;
}

.search input {
  width: 100%;
  height: 34px;
  padding: 0 12px 0 34px;
  border: 1px solid var(--line);
  border-radius: 9px;
  background: var(--surface-2);
  color: var(--ink);
  font: inherit;
  font-size: 13px;
  outline: none;
}

.search input::placeholder {
  color: var(--ink-3);
}

.search input:focus {
  border-color: var(--accent);
  background: var(--surface);
  box-shadow: 0 0 0 3px var(--accent-soft);
}

.search-icon {
  position: absolute;
  left: 11px;
  top: 9px;
  color: var(--ink-3);
  pointer-events: none;
}

.search-icon:deep(svg) {
  width: 16px;
  height: 16px;
}

.top-actions {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
}

/* 当前账号：右上角的小胶囊，右侧挂「只读」徽标 */
.user-chip {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  max-width: 200px;
  height: 30px;
  padding: 0 10px 0 4px;
  border: 1px solid var(--line);
  border-radius: 999px;
  background: var(--surface-2);
  color: var(--ink-2);
  font-size: 12.5px;
}

.user-avatar {
  display: inline-flex;
  width: 22px;
  height: 22px;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  background: var(--surface);
  color: var(--ink-3);
}

.user-avatar:deep(svg) {
  width: 15px;
  height: 15px;
}

.user-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.user-badge {
  flex: 0 0 auto;
  padding: 1px 6px;
  border-radius: 999px;
  background: var(--surface);
  color: var(--ink-3);
  font-size: 10.5px;
}

/* 工具栏按钮（下载全部）与顶栏图标按钮的共用外观 */
.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 34px;
  padding: 0 13px;
  border: 1px solid var(--line);
  border-radius: 9px;
  background: var(--surface);
  color: var(--ink);
  font: inherit;
  font-size: 13px;
  white-space: nowrap;
  transition: background 0.12s ease, border-color 0.12s ease;
}

.btn:hover {
  background: var(--surface-2);
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-icon {
  display: inline-flex;
}

.btn-icon:deep(svg) {
  width: 15px;
  height: 15px;
}

.icon-btn {
  width: 32px;
  height: 32px;
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
  background: var(--surface-2);
  color: var(--ink);
}

/* ---------- 两栏布局 ---------- */

.body {
  flex: 1;
  display: flex;
  min-height: 0;
}

.body.is-resizing {
  user-select: none;
  cursor: col-resize;
}

.sidebar-wrap {
  display: flex;
}

.sidebar-grip {
  position: relative;
  width: 6px;
  flex: 0 0 6px;
  cursor: col-resize;
}

.sidebar-grip::after {
  content: "";
  position: absolute;
  inset: 0 2px;
  border-radius: 999px;
  background: transparent;
  transition: background 0.15s ease;
}

.sidebar-grip:hover::after,
.sidebar-grip.is-active::after {
  background: var(--accent-soft);
}

.content {
  flex: 1;
  min-width: 0;
  min-height: 0;
  overflow-y: auto;
  padding: 16px 18px 28px;
  background: var(--surface-2);
}

/* 细滚动条：轨道透明，拇指内缩成圆角胶囊 */
.content::-webkit-scrollbar {
  width: 12px;
}

.content::-webkit-scrollbar-track {
  background: transparent;
}

.content::-webkit-scrollbar-thumb {
  border: 3px solid transparent;
  border-radius: 999px;
  background-clip: padding-box;
  background-color: rgb(20 23 44 / 0.16);
}

.content::-webkit-scrollbar-thumb:hover {
  background-color: rgb(20 23 44 / 0.28);
}

/* ---------- 工具栏 / 面包屑 ---------- */

.toolbar {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 14px;
  min-height: 34px;
}

.crumbs {
  display: flex;
  align-items: center;
  gap: 2px;
  min-width: 0;
  flex-wrap: wrap;
}

.crumb {
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding: 4px 7px;
  border-radius: 7px;
  color: var(--ink-3);
  font-size: 13.5px;
}

.crumb:hover {
  background: var(--surface);
  color: var(--ink);
}

.crumb.current {
  color: var(--ink);
  font-size: 16px;
  font-weight: 650;
  cursor: default;
}

.crumb.current:hover {
  background: none;
}

.crumb-sep {
  display: inline-flex;
  color: var(--ink-3);
}

.crumb-sep:deep(svg) {
  width: 13px;
  height: 13px;
  transform: rotate(-90deg);
}

.toolbar-right {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 0 0 auto;
}

.meta {
  color: var(--ink-3);
  font-size: 12px;
  white-space: nowrap;
}

/* ---------- 空状态 ---------- */

.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 72px 20px;
  text-align: center;
  color: var(--ink-3);
  background: var(--surface);
  border: 1px dashed var(--line);
  border-radius: var(--radius);
}

.empty-icon {
  display: inline-flex;
  color: var(--ink-3);
  opacity: 0.75;
}

.empty-icon:deep(svg) {
  width: 42px;
  height: 42px;
}

.empty-title {
  margin: 0;
  color: var(--ink-2);
  font-size: 15px;
}

.empty-text {
  margin: 0;
  font-size: 13px;
}

.toast {
  position: fixed;
  left: 50%;
  bottom: 28px;
  z-index: 90;
  max-width: 80vw;
  transform: translateX(-50%);
  padding: 10px 16px;
  border-radius: 10px;
  background: rgb(22 25 38 / 0.92);
  color: #fff;
  font-size: 13px;
  box-shadow: var(--shadow-md);
}

.drawer-mask {
  display: none;
}

/* 窄屏右下角「下载全部」悬浮按钮 */
.fab {
  position: fixed;
  right: 16px;
  bottom: calc(16px + env(safe-area-inset-bottom, 0px));
  z-index: 60;
  width: 54px;
  height: 54px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  background: var(--accent);
  color: #fff;
  box-shadow: var(--shadow-lg);
}

.fab:deep(svg) {
  width: 22px;
  height: 22px;
}

@media (max-width: 900px) {
  .hamburger {
    display: inline-flex;
  }

  .search {
    width: auto;
    flex: 1;
    margin-left: 0;
  }

  .top-actions :deep(.locale-switch) {
    display: none;
  }

  /* 窄屏顶栏只留一个头像，名字和只读徽标收进 title */
  .user-chip {
    height: 34px;
    padding: 0 2px;
    border-color: transparent;
    background: transparent;
  }

  .user-chip .user-name,
  .user-chip .user-badge {
    display: none;
  }

  .sidebar-wrap {
    position: fixed;
    top: 0;
    bottom: 0;
    left: 0;
    z-index: 85;
    transform: translateX(-100%);
    transition: transform 0.2s ease;
    box-shadow: var(--shadow-lg);
  }

  .sidebar-wrap.is-open {
    transform: translateX(0);
  }

  /* 窄屏没有两栏布局，边界 grip 一并隐藏 */
  .sidebar-grip {
    display: none;
  }

  .drawer-mask {
    display: block;
    position: fixed;
    inset: 0;
    z-index: 84;
    background: rgb(22 25 38 / 0.4);
  }

  .content {
    /* 底部留出 FAB 的空间，避免挡住列表最后一行 */
    padding: 14px 14px 92px;
  }

  .toolbar {
    flex-wrap: wrap;
    gap: 8px;
  }

  .meta {
    display: none;
  }

  .crumb {
    max-width: 140px;
  }

  /* 「下载全部」在窄屏收进右下角悬浮按钮 */
  .toolbar-right .download-all {
    display: none;
  }
}
</style>