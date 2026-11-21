<script setup lang="ts">
/**
 * 主应用壳：顶栏（搜索 / 上传） + 左侧文件夹树 + 内容区表格（当前文件夹的子文件夹与文件）。
 *
 * 视图与地址栏一一对应：`/`「最近」、`/favorites`「收藏」、`/ungrouped`「未分组」、
 * `/<一级>`、`/<一级>/<二级>`、`/<一级>/<二级>/<三级>`「文件夹」。切换视图用 pushState 进历史，
 * 前进 / 后退与直接打开链接都通过 routes.ts 还原；搜索词写在 `?q=` 里。
 *
 * 列表数据只在这里维护；每次操作成功后重新拉一次列表，保持与服务端一致。
 * 文件夹树的展开 / 折叠状态存 localStorage，刷新后保持。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";

import ConfirmDialog from "@/components/ConfirmDialog.vue";
import ContentTable from "@/components/ContentTable.vue";
import DirTree from "@/components/DirTree.vue";
import GroupStatsDialog from "@/components/GroupStatsDialog.vue";
import LocaleSwitch from "@/components/LocaleSwitch.vue";
import PromptDialog from "@/components/PromptDialog.vue";
import UploadDock from "@/components/UploadDock.vue";
import { hasFiles } from "@/dnd";
import {
  ApiError,
  createGroup,
  deleteFile,
  deleteGroup,
  downloadFile,
  getStorageInfo,
  listFiles,
  renameFile,
  renameGroup,
  reorderFiles,
  reorderGroups,
  toggleFavorite,
  updateFileGroup,
} from "@/api";
import { formatSize } from "@/format";
import { ICONS } from "@/icons";
import { t } from "@/i18n";
import { viewFromPath, viewPath, type View } from "@/routes";
import { toLogin } from "@/session";
import type { DirEntry, FileItem, GroupItem, StorageInfo } from "@/types";

/** 「最近」最多列这么多条；搜索时不受限制。 */
const RECENT_LIMIT = 20;
const SEARCH_PARAM = "q";
const MAX_GROUP_DEPTH = 3;
const FOLDED_KEY = "file-share.folded-folders";
const FOLDER_MENU_WIDTH = 220;

/* ---------- 数据 ---------- */

const files = ref<FileItem[]>([]);
const groups = ref<GroupItem[]>([]);
const loaded = ref(false);
const refreshing = ref(false);
const loadError = ref("");
const actionError = ref("");
let actionErrorTimer = 0;
let refreshSeq = 0;

/** 当前视图：最近 / 收藏 / 未分组 / 文件夹（1~3 级路径）。 */
const view = ref<View>({ kind: "recent" });
const query = ref("");

const uploadDock = ref<InstanceType<typeof UploadDock> | null>(null);
/** 定时探测存储状态的定时器（页面可见时才真正请求）。 */
let storageTimer = 0;
const maxFileSize = ref(0);
const storage = ref<StorageInfo | null>(null);
/** 窄屏抽屉：侧栏在移动端默认收起。 */
const sidebarOpen = ref(false);

/* ---------- 侧栏宽度（桌面端拖右缘调节） ---------- */

const SIDEBAR_DEFAULT_WIDTH = 248;
const SIDEBAR_MIN_WIDTH = 200;
/** 上限取视口一半：目录列表最多占半屏，内容区至少留一半。 */
const SIDEBAR_MAX_RATIO = 0.5;
const SIDEBAR_WIDTH_KEY = "file-share.sidebar-width";

function maxSidebarWidth(): number {
  return Math.max(SIDEBAR_MIN_WIDTH, Math.floor(window.innerWidth * SIDEBAR_MAX_RATIO));
}

function clampSidebarWidth(width: number): number {
  return Math.min(Math.max(Math.round(width), SIDEBAR_MIN_WIDTH), maxSidebarWidth());
}

function loadSidebarWidth(): number {
  try {
    const stored = Number.parseInt(localStorage.getItem(SIDEBAR_WIDTH_KEY) ?? "", 10);
    return Number.isFinite(stored) ? clampSidebarWidth(stored) : SIDEBAR_DEFAULT_WIDTH;
  } catch {
    /* 隐私模式下 localStorage 不可用，用默认宽度 */
    return SIDEBAR_DEFAULT_WIDTH;
  }
}

/** 侧栏宽度只存在本机：刷新、重启后保持用户拖出来的宽度。 */
const sidebarWidth = ref(loadSidebarWidth());
const resizingSidebar = ref(false);
let stopSidebarResize: (() => void) | null = null;

/**
 * 按住两栏边界的 grip 拖动：宽度实时跟随指针，放手后落盘。
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
    try {
      localStorage.setItem(SIDEBAR_WIDTH_KEY, String(sidebarWidth.value));
    } catch {
      /* 存不下就只能本次有效 */
    }
  };
  stopSidebarResize = finish;
  window.addEventListener("pointermove", onMove);
  window.addEventListener("pointerup", finish);
  window.addEventListener("pointercancel", finish);
}

/** 窗口变窄时把侧栏夹回上限以内（用户存下的宽度不覆盖）。 */
function onWindowResize(): void {
  sidebarWidth.value = clampSidebarWidth(sidebarWidth.value);
}

/* ---------- 视图与派生数据 ---------- */

/** 当前文件夹路径；非文件夹视图为空串（上传落到未分组）。 */
const currentPath = computed(() => (view.value.kind === "group" ? view.value.group : ""));

const keyword = computed(() => query.value.trim().toLowerCase());
const searching = computed(() => keyword.value.length > 0);

const viewKind = computed(() => view.value.kind);

const ungroupedCount = computed(() => files.value.filter((file) => file.group_path === "").length);
const favoritesCount = computed(() => files.value.filter((file) => file.is_favorite).length);
const recentCount = computed(() => Math.min(files.value.length, RECENT_LIMIT));

/** 某个文件夹（含全部后代）下的文件。 */
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

function byOrder(a: FileItem, b: FileItem): number {
  return a.sort_order - b.sort_order || a.filename.localeCompare(b.filename);
}

/** 当前视图要列出的文件：搜索时跨全部文件夹，按时间倒序。 */
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
    case "ungrouped":
      return all.filter((file) => file.group_path === "").sort(byOrder);
    case "group": {
      const current = view.value.kind === "group" ? view.value.group : "";
      return all.filter((file) => file.group_path === current).sort(byOrder);
    }
  }
});

/** 内容区里的子文件夹行：只有文件夹视图（且不在搜索）才列子文件夹。 */
const dirs = computed<DirEntry[]>(() => {
  if (searching.value || view.value.kind !== "group") return [];
  const parent = view.value.group;
  return groups.value
    .filter((group) => group.parent === parent)
    .map((group) => ({ path: group.path, name: group.name, fileCount: counts.value.get(group.path) ?? 0 }));
});

const pageFilesSize = computed(() => visibleFiles.value.reduce((sum, file) => sum + file.size, 0));

/** 文件行是否可拖拽排序：只有文件夹视图、且没有搜索过滤时提交顺序才有意义。 */
const sortable = computed(() => view.value.kind === "group" && !searching.value);

/** 最近与搜索结果是跨文件夹的，行内要标出文件所在文件夹。 */
const showGroup = computed(() => searching.value || view.value.kind === "recent");

/** 搜索时面包屑只显示「搜索结果」，不显示当前文件夹路径。 */
const visibleCrumbs = computed(() => (searching.value ? [] : crumbs.value));

/** 面包屑：文件夹视图逐级展开；其它视图只有当前项。 */
const crumbs = computed<{ name: string; path: string }[]>(() => {
  if (view.value.kind !== "group") return [];
  const segments = view.value.group.split("/");
  return segments.map((name, index) => ({
    name,
    path: segments.slice(0, index + 1).join("/"),
  }));
});

/** 顶层面包屑文案：文件夹视图显示「全部」，其它视图直接显示视图名。 */
const rootLabel = computed(() => {
  if (view.value.kind === "recent") return t("nav.recent");
  if (view.value.kind === "favorites") return t("nav.favorites");
  if (view.value.kind === "ungrouped") return t("nav.ungrouped");
  return searching.value ? t("content.search-results") : t("nav.all");
});

/** 文件夹是否还能再建子文件夹（三级是上限）。 */
function canCreateSubfolder(path: string): boolean {
  return path.split("/").length < MAX_GROUP_DEPTH;
}

/** 当前视图是否还能新建文件夹：非文件夹视图建顶级，三级文件夹下不能再建。 */
const canCreateFolder = computed(() =>
  view.value.kind === "group" ? canCreateSubfolder(view.value.group) : true,
);

/* ---------- 侧栏折叠状态 ---------- */

function loadFolded(): Set<string> {
  try {
    const raw = localStorage.getItem(FOLDED_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === "string") : []);
  } catch {
    return new Set();
  }
}

const folded = ref<Set<string>>(loadFolded());

function toggleFolder(path: string): void {
  const next = new Set(folded.value);
  if (next.has(path)) next.delete(path);
  else next.add(path);
  folded.value = next;
  try {
    localStorage.setItem(FOLDED_KEY, JSON.stringify([...next]));
  } catch {
    /* 存不下就只在本次会话里生效 */
  }
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

/** 文件夹不存在时回退「最近」，免得刷新后停在空视图。 */
function validateView(): void {
  const current = view.value;
  if (current.kind === "group" && !groups.value.some((group) => group.path === current.group)) {
    applyView({ kind: "recent" }, "replace");
  }
}

watch([query, view], () => syncLocation("replace"));

function showRecent(): void {
  applyView({ kind: "recent" });
}

function showFavorites(): void {
  applyView({ kind: "favorites" });
}

function showUngrouped(): void {
  applyView({ kind: "ungrouped" });
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
  // 操作失败可能是存储后端连不上：顺手刷新一次状态，让侧栏的存储提示跟上。
  void loadStorage();
}

/** 拉存储方案给侧栏提示用；失败只影响这行提示，不影响列表与操作。 */
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
  const detail =
    info.backend === "s3"
      ? t("storage.s3-detail", { bucket: info.bucket ?? "", prefix: info.prefix ?? "" })
      : t("storage.local");
  // 后端已连不上时不隐藏这行提示，而是标出来：用户点下载 / 上传前就知道会失败。
  return info.healthy ? detail : `${detail}${t("storage.unreachable")}`;
});

const storageHint = computed(() => {
  const info = storage.value;
  if (!info) return "";
  if (!info.healthy && info.error) return info.error;
  return info.backend === "s3" ? t("storage.s3-hint") : t("storage.local-hint");
});

/** 未知（还没拉到）时按健康处理，避免页面一闪就红点。 */
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
    maxFileSize.value = payload.max_file_size;
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

async function onDownload(file: FileItem): Promise<void> {
  try {
    await downloadFile(file.id);
  } catch (error) {
    showActionError(error);
  }
}

/** 逐个签下载地址并触发下载；浏览器可能询问是否允许下载多个文件。 */
async function onDownloadFiles(targets: FileItem[]): Promise<void> {
  if (!targets.length) return;
  try {
    for (const file of targets) await downloadFile(file.id);
  } catch (error) {
    showActionError(error);
  }
}

/** 当前视图的「全部下载」：下载当前可见的文件。 */
async function onDownloadAll(): Promise<void> {
  await onDownloadFiles(visibleFiles.value);
}

/** 拖拽排序后的顺序先落到本地（Vue 重新渲染时保持 DOM 已有顺序），再提交服务端。 */
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
    // 回滚：重新拉服务端顺序
    await refresh();
  }
}

/**
 * 文件夹行拖拽后的顺序：只重排「当前文件夹下」的兄弟文件夹。
 * 先把本地顺序改掉（避免刷新前闪回旧顺序），再提交 `PUT /api/groups/order`。
 */
function applyGroupOrder(parent: string, names: string[]): void {
  const rank = new Map(names.map((name, index) => [name, index] as const));
  const siblings = groups.value
    .filter((group) => group.parent === parent)
    .sort(
      (a, b) =>
        (rank.get(a.name) ?? Number.MAX_SAFE_INTEGER) -
        (rank.get(b.name) ?? Number.MAX_SAFE_INTEGER),
    );
  // 只替换目标父级那几个位置，其它层级的顺序原样保留（比较器不再依赖「同父级连续」的隐含前提）。
  let next = 0;
  groups.value = groups.value.map((group) =>
    group.parent === parent ? (siblings[next++] ?? group) : group,
  );
}

async function onReorderDirs(orderedPaths: string[]): Promise<void> {
  const parent = currentPath.value;
  const names = orderedPaths.map((path) => path.split("/").pop() ?? "");
  applyGroupOrder(parent, names);
  try {
    await reorderGroups(parent, names);
  } catch (error) {
    showActionError(error);
    // 回滚：重新拉服务端顺序
    await refresh();
  }
}

/** 移动到某个文件夹（空串 = 未分组）。 */
async function onMoveGroup(file: FileItem, path: string): Promise<void> {
  if (file.group_path === path) return;
  try {
    await updateFileGroup(file.id, path);
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

/* ---------- 输入 / 确认对话框 ---------- */

type PromptState =
  | { kind: "rename-file"; file: FileItem }
  /** 在 parent 下新建文件夹（空串 = 顶级）。 */
  | { kind: "new-folder"; parent: string }
  | { kind: "rename-folder"; path: string; name: string }
  /** 「移动到文件夹」菜单里新建文件夹，随后把文件移进去。 */
  | { kind: "move-file-new-folder"; file: FileItem };

const promptState = ref<PromptState | null>(null);

const promptDialog = computed(() => {
  const state = promptState.value;
  if (!state) return null;
  switch (state.kind) {
    case "rename-file":
      return {
        title: t("rename.title"),
        hint: "",
        value: state.file.filename,
        confirmLabel: t("action.rename"),
      };
    case "new-folder":
      return {
        title: state.parent ? t("group.new-subgroup-title") : t("group.new-title"),
        hint: state.parent
          ? state.parent
          : t("group.new-hint"),
        value: "",
        confirmLabel: t("action.create"),
      };
    case "rename-folder":
      return {
        title: t("group.rename-title"),
        hint: "",
        value: state.name,
        confirmLabel: t("action.rename"),
      };
    case "move-file-new-folder":
      return {
        title: t("group.new-title"),
        hint: t("group.new-hint"),
        value: "",
        confirmLabel: t("action.move-group"),
      };
  }
});

function groupPathOf(state: PromptState, value: string): string {
  if (state.kind === "rename-folder") {
    const slash = state.path.lastIndexOf("/");
    const parent = slash < 0 ? "" : state.path.slice(0, slash);
    return parent ? `${parent}/${value}` : value;
  }
  const parent = state.kind === "new-folder" ? state.parent : "";
  return parent ? `${parent}/${value}` : value;
}

async function onPromptConfirm(value: string): Promise<void> {
  const state = promptState.value;
  if (!state) return;
  promptState.value = null;
  switch (state.kind) {
    case "rename-file": {
      if (value === state.file.filename) return;
      try {
        await renameFile(state.file.id, value);
        await refresh();
      } catch (error) {
        showActionError(error);
      }
      return;
    }
    case "new-folder": {
      try {
        const created = await createGroup(groupPathOf(state, value));
        await refresh();
        applyView({ kind: "group", group: created.path });
      } catch (error) {
        showActionError(error);
      }
      return;
    }
    case "rename-folder": {
      if (value === state.name) return;
      const nextPath = groupPathOf(state, value);
      try {
        await renameGroup(state.path, value);
        const current = view.value;
        if (
          current.kind === "group" &&
          (current.group === state.path || current.group.startsWith(`${state.path}/`))
        ) {
          applyView({ kind: "group", group: nextPath + current.group.slice(state.path.length) }, "replace");
        }
        await refresh();
      } catch (error) {
        showActionError(error);
      }
      return;
    }
    case "move-file-new-folder": {
      try {
        const created = await createGroup(value);
        const file = state.file;
        if (file.group_path !== created.path) await updateFileGroup(file.id, created.path);
        await refresh();
      } catch (error) {
        showActionError(error);
      }
      return;
    }
  }
}

function onRename(file: FileItem): void {
  promptState.value = { kind: "rename-file", file };
}

const deleteFileTarget = ref<FileItem | null>(null);

function onDelete(file: FileItem): void {
  deleteFileTarget.value = file;
}

async function confirmDeleteFile(): Promise<void> {
  const file = deleteFileTarget.value;
  if (!file) return;
  deleteFileTarget.value = null;
  try {
    await deleteFile(file.id);
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

/** 新建文件夹：默认建在当前文件夹下；非文件夹视图建顶级文件夹。 */
function onCreateFolder(): void {
  if (!canCreateFolder.value) return;
  promptState.value = { kind: "new-folder", parent: currentPath.value };
}

/* ---------- 文件夹菜单（统计 / 重命名 / 新建子文件夹 / 删除） ---------- */

const folderMenu = ref<{ path: string; name: string; x: number; y: number } | null>(null);

function openFolderMenuAt(x: number, y: number, path: string): void {
  const group = groups.value.find((entry) => entry.path === path);
  if (!group) return;
  folderMenu.value = {
    path,
    name: group.name,
    x: Math.min(x, Math.max(8, window.innerWidth - FOLDER_MENU_WIDTH - 8)),
    y: Math.min(y, Math.max(8, window.innerHeight - 180)),
  };
}

function closeFolderMenu(): void {
  folderMenu.value = null;
}

function onFolderMenuFromNode(event: MouseEvent, path: string): void {
  event.preventDefault();
  event.stopPropagation();
  openFolderMenuAt(event.clientX, event.clientY, path);
}

function onFolderMenuFromRow(event: MouseEvent, path: string): void {
  event.preventDefault();
  event.stopPropagation();
  openFolderMenuAt(event.clientX, event.clientY, path);
}

function onFolderMenuFromTitle(event: MouseEvent): void {
  if (view.value.kind !== "group") return;
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  openFolderMenuAt(rect.left, rect.bottom + 6, view.value.group);
}

function onFolderMenuPointerDown(event: PointerEvent): void {
  const target = event.target;
  if (target instanceof Node && (target as Element).closest?.(".folder-menu-pop")) return;
  closeFolderMenu();
}

watch(folderMenu, (value) => {
  if (value) window.addEventListener("pointerdown", onFolderMenuPointerDown, true);
  else window.removeEventListener("pointerdown", onFolderMenuPointerDown, true);
});

const statsTarget = ref<{ path: string } | null>(null);

function onShowStats(): void {
  const target = folderMenu.value;
  closeFolderMenu();
  if (!target) return;
  statsTarget.value = { path: target.path };
}

function onRenameFolderFromMenu(): void {
  const target = folderMenu.value;
  closeFolderMenu();
  if (!target) return;
  promptState.value = { kind: "rename-folder", path: target.path, name: target.name };
}

function onCreateSubfolderFromMenu(): void {
  const target = folderMenu.value;
  closeFolderMenu();
  if (!target) return;
  promptState.value = { kind: "new-folder", parent: target.path };
}

const deleteFolderTarget = ref<{ path: string; name: string } | null>(null);

function onDeleteFolderFromMenu(): void {
  const target = folderMenu.value;
  closeFolderMenu();
  if (!target) return;
  deleteFolderTarget.value = { path: target.path, name: target.name };
}

const deleteFolderFileCount = computed(() => {
  const target = deleteFolderTarget.value?.path;
  if (!target) return 0;
  return subtreeFiles(target).length;
});

const deleteFolderFallback = computed(() => {
  const path = deleteFolderTarget.value?.path ?? "";
  const slash = path.lastIndexOf("/");
  if (slash < 0) return t("nav.ungrouped");
  const parent = path.slice(0, slash);
  return groups.value.find((group) => group.path === parent)?.name ?? t("nav.ungrouped");
});

async function confirmDeleteFolder(deleteFiles: boolean): Promise<void> {
  const target = deleteFolderTarget.value;
  if (!target) return;
  deleteFolderTarget.value = null;
  try {
    await deleteGroup(target.path, deleteFiles);
    const current = view.value;
    if (
      current.kind === "group" &&
      (current.group === target.path || current.group.startsWith(`${target.path}/`))
    ) {
      const slash = target.path.lastIndexOf("/");
      applyView(slash < 0 ? { kind: "recent" } : { kind: "group", group: target.path.slice(0, slash) }, "replace");
    }
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

/* ---------- 上传 ---------- */

function pickFiles(): void {
  uploadDock.value?.pick();
}

/** 上传目标：当前文件夹；非文件夹视图落到未分组。 */
const uploadTarget = computed(() => currentPath.value);

/* ---------- 整页拖放 / 文件拖到文件夹 ---------- */

const dropActive = ref(false);
/** 正在拖拽的文件 id（内部拖拽）：用来区分「移动文件」与「上传外部文件」。 */
const draggingFileId = ref<string | null>(null);

function resetDropState(): void {
  dropActive.value = false;
  draggingFileId.value = null;
}

function onFileDragStart(file: FileItem): void {
  draggingFileId.value = file.id;
}

function onFileDragEnd(): void {
  draggingFileId.value = null;
  dropActive.value = false;
}

/** 拖到文件夹树 / 文件夹行：内部文件移动，外部文件上传到该文件夹。 */
async function onDropOnFolder(dropped: FileList | null, path: string): Promise<void> {
  const draggedId = draggingFileId.value;
  draggingFileId.value = null;
  dropActive.value = false;
  if (draggedId) {
    const file = files.value.find((entry) => entry.id === draggedId);
    if (file) await onMoveGroup(file, path);
    return;
  }
  if (dropped && dropped.length > 0) uploadDock.value?.addFiles(dropped, path);
}

function onMainDragEnter(event: DragEvent): void {
  if (draggingFileId.value) return;
  if (!hasFiles(event)) return;
  event.preventDefault();
  dropActive.value = true;
}

function onMainDragOver(event: DragEvent): void {
  if (draggingFileId.value) {
    if (view.value.kind !== "group") return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
    return;
  }
  if (!hasFiles(event)) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
  dropActive.value = true;
}

function onMainDragLeave(event: DragEvent): void {
  if (draggingFileId.value) return;
  const current = event.currentTarget as HTMLElement;
  if (event.relatedTarget instanceof Node && current.contains(event.relatedTarget)) return;
  dropActive.value = false;
}

async function onMainDrop(event: DragEvent): Promise<void> {
  event.preventDefault();
  if (draggingFileId.value) {
    const draggedId = draggingFileId.value;
    draggingFileId.value = null;
    if (view.value.kind !== "group") return;
    const file = files.value.find((entry) => entry.id === draggedId);
    if (file) await onMoveGroup(file, view.value.group);
    return;
  }
  if (!hasFiles(event)) return;
  const dropped = event.dataTransfer?.files;
  dropActive.value = false;
  if (dropped && dropped.length > 0) uploadDock.value?.addFiles(dropped, uploadTarget.value);
}

/** 拖到页面任何地方都吞掉默认行为，免得浏览器直接打开文件、把页面顶掉。 */
function swallowDrop(event: DragEvent): void {
  if (!hasFiles(event)) return;
  event.preventDefault();
  if (event.type === "drop") resetDropState();
}

function logout(): void {
  toLogin();
}

onMounted(() => {
  document.addEventListener("dragover", swallowDrop);
  document.addEventListener("drop", swallowDrop);
  document.addEventListener("dragend", onFileDragEnd);
  window.addEventListener("popstate", syncViewFromLocation);
  window.addEventListener("resize", onWindowResize);
  syncViewFromLocation();
  void refresh();
  void loadStorage();
  // 定时重新探测存储状态：S3 恢复后侧栏的提示能自己变回绿色，不用等用户操作。
  storageTimer = window.setInterval(() => {
    if (document.visibilityState === "visible") void loadStorage();
  }, 30000);
});

onBeforeUnmount(() => {
  document.removeEventListener("dragover", swallowDrop);
  document.removeEventListener("drop", swallowDrop);
  document.removeEventListener("dragend", onFileDragEnd);
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

      <div class="brand">
        <span class="brand-mark" v-html="ICONS.upload"></span>
        <span class="brand-name">{{ t("app.title") }}</span>
      </div>

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
        <button
          type="button"
          class="btn"
          :disabled="!canCreateFolder"
          @click="onCreateFolder"
        >
          <span class="btn-icon" v-html="ICONS.plus"></span>
          {{ t("action.new-group") }}
        </button>
        <button type="button" class="btn primary" @click="pickFiles">
          <span class="btn-icon" v-html="ICONS.upload"></span>
          {{ t("upload.button") }}
        </button>
        <LocaleSwitch class="lang" />
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
          :recent-count="recentCount"
          :favorites-count="favoritesCount"
          :ungrouped-count="ungroupedCount"
          :view-kind="viewKind"
          :active-path="currentPath"
          :folded="folded"
          :storage-text="storageText"
          :storage-hint="storageHint"
          :storage-healthy="storageHealthy"
          :internal-drag="draggingFileId !== null"
        :can-create="canCreateFolder"
          @show-recent="showRecent"
          @show-favorites="showFavorites"
          @show-ungrouped="showUngrouped"
          @open-folder="openFolder"
          @toggle="toggleFolder"
          @menu="onFolderMenuFromNode"
          @new-folder="onCreateFolder"
          @drop-folder="onDropOnFolder"
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

      <main
        class="content"
        @dragenter="onMainDragEnter"
        @dragover="onMainDragOver"
        @dragleave="onMainDragLeave"
        @drop="onMainDrop"
      >
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
              class="btn"
              :disabled="refreshing"
              @click="onDownloadAll"
            >
              <span class="btn-icon" v-html="ICONS.download"></span>
              {{ t("action.download-group-size", { size: formatSize(pageFilesSize) }) }}
            </button>

            <button
              v-if="viewKind === 'group' && canCreateSubfolder(currentPath)"
              type="button"
              class="btn"
              @click="promptState = { kind: 'new-folder', parent: currentPath }"
            >
              <span class="btn-icon" v-html="ICONS.plus"></span>
              {{ t("action.new-folder") }}
            </button>

            <button
              v-if="viewKind === 'group'"
              type="button"
              class="icon-btn"
              :title="t('action.group-menu')"
              :aria-label="t('action.group-menu')"
              aria-haspopup="menu"
              v-html="ICONS.more"
              @click="onFolderMenuFromTitle"
            ></button>
          </div>
        </div>

        <ContentTable
          v-if="dirs.length || visibleFiles.length"
          :dirs="dirs"
          :files="visibleFiles"
          :groups="groups"
          :sortable="sortable"
          :show-group="showGroup"
          :dragging-file-id="draggingFileId"
          @open-folder="openFolder"
          @folder-menu="onFolderMenuFromRow"
          @favorite="onFavorite"
          @download="onDownload"
          @rename="onRename"
          @delete="onDelete"
          @move-group="onMoveGroup"
          @new-group="(file) => (promptState = { kind: 'move-file-new-folder', file })"
          @error="showActionError"
          @reorder="onReorder"
          @reorder-dirs="onReorderDirs"
          @drag-start="onFileDragStart"
          @drag-end="onFileDragEnd"
          @drop-files="onDropOnFolder"
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
          <button
            v-if="loaded && !searching"
            type="button"
            class="btn primary"
            @click="pickFiles"
          >
            <span class="btn-icon" v-html="ICONS.upload"></span>
            {{ t("upload.button") }}
          </button>
        </div>

        <p v-if="dirs.length && !visibleFiles.length && loaded" class="folder-empty">
          {{ t("content.folder-empty") }}
        </p>

        <p v-if="loaded && (dirs.length || visibleFiles.length)" class="drop-hint">
          <span class="drop-hint-icon" v-html="ICONS.upload"></span>
          {{ t("content.drop-hint") }}
        </p>

        <div v-if="dropActive" class="drop-overlay" aria-hidden="true">
          <span class="drop-overlay-icon" v-html="ICONS.upload"></span>
          <p class="drop-overlay-text">{{ t("upload.drop-overlay") }}</p>
        </div>
      </main>
    </div>

    <!-- 文件夹菜单 -->
    <Teleport to="body">
      <div
        v-if="folderMenu"
        class="row-menu folder-menu-pop"
        :style="{ left: `${folderMenu.x}px`, top: `${folderMenu.y}px` }"
        role="menu"
        :aria-label="folderMenu.name"
      >
        <button type="button" class="row-menu-item" role="menuitem" @click="onShowStats">
          <span class="row-menu-icon" v-html="ICONS.stats"></span>
          <span class="row-menu-label">{{ t("action.stats") }}</span>
        </button>
        <button type="button" class="row-menu-item" role="menuitem" @click="onRenameFolderFromMenu">
          <span class="row-menu-icon" v-html="ICONS.pencil"></span>
          <span class="row-menu-label">{{ t("action.rename-group") }}</span>
        </button>
        <button
          v-if="canCreateSubfolder(folderMenu.path)"
          type="button"
          class="row-menu-item"
          role="menuitem"
          @click="onCreateSubfolderFromMenu"
        >
          <span class="row-menu-icon" v-html="ICONS.folder"></span>
          <span class="row-menu-label">{{ t("action.create-subgroup") }}</span>
        </button>
        <div class="row-menu-sep" role="separator"></div>
        <button type="button" class="row-menu-item is-danger" role="menuitem" @click="onDeleteFolderFromMenu">
          <span class="row-menu-icon" v-html="ICONS.trash"></span>
          <span class="row-menu-label">{{ t("action.delete-group") }}</span>
        </button>
      </div>
    </Teleport>

    <ConfirmDialog
      v-if="deleteFolderTarget"
      :title="t('group.delete-title', { name: deleteFolderTarget.name })"
      :message="
        deleteFolderFileCount
          ? t('group.delete-message', { count: deleteFolderFileCount, target: deleteFolderFallback })
          : t('group.delete-empty')
      "
      :checkbox="deleteFolderFileCount ? t('group.delete-files', { count: deleteFolderFileCount }) : undefined"
      :checkbox-hint="deleteFolderFileCount ? t('group.delete-files-hint') : undefined"
      :confirm-label="t('action.delete-group')"
      @confirm="confirmDeleteFolder"
      @cancel="deleteFolderTarget = null"
    />

    <ConfirmDialog
      v-if="deleteFileTarget"
      :title="t('confirm.delete', { name: deleteFileTarget.filename })"
      :confirm-label="t('action.delete')"
      @confirm="confirmDeleteFile"
      @cancel="deleteFileTarget = null"
    />

    <PromptDialog
      v-if="promptDialog"
      :title="promptDialog.title"
      :hint="promptDialog.hint"
      :value="promptDialog.value"
      :confirm-label="promptDialog.confirmLabel"
      @confirm="onPromptConfirm"
      @cancel="promptState = null"
    />

    <GroupStatsDialog
      v-if="statsTarget"
      :path="statsTarget.path"
      :files="subtreeFiles(statsTarget.path)"
      @close="statsTarget = null"
    />

    <UploadDock
      ref="uploadDock"
      :target-group="uploadTarget"
      :max-file-size="maxFileSize"
      @uploaded="refresh"
      @failed="loadStorage"
    />

    <div v-if="actionError" class="toast" role="status">{{ actionError }}</div>
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

.brand {
  display: flex;
  align-items: center;
  gap: 9px;
  flex: 0 0 auto;
}

.brand-mark {
  width: 30px;
  height: 30px;
  border-radius: 9px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, var(--accent), #7b6cf6);
  color: #fff;
  box-shadow: var(--shadow-sm);
}

.brand-mark:deep(svg) {
  width: 17px;
  height: 17px;
}

.brand-name {
  font-size: 15px;
  font-weight: 650;
  letter-spacing: 0.2px;
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

.btn.primary {
  border-color: transparent;
  background: linear-gradient(135deg, var(--accent), #7b6cf6);
  color: #fff;
  font-weight: 600;
  box-shadow: var(--shadow-sm);
}

.btn.primary:hover {
  filter: brightness(1.04);
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

.icon-btn:hover {
  background: var(--surface-2);
  color: var(--ink);
}

.icon-btn:deep(svg) {
  width: 17px;
  height: 17px;
}

/* ---------- 布局 ---------- */

.body {
  position: relative;
  flex: 1;
  display: flex;
  min-height: 0;
}

/* 拖动分栏时整页禁用选中，避免把列表文字一起抹蓝 */
.body.is-resizing {
  cursor: col-resize;
  user-select: none;
}

.sidebar-wrap {
  display: flex;
  flex: 0 0 var(--sidebar-width);
  width: var(--sidebar-width);
  min-height: 0;
}

/* 两栏边界的 grip：绝对定位，不占布局宽度；只压住边界两侧几像素 */
.sidebar-grip {
  position: absolute;
  top: 0;
  bottom: 0;
  left: calc(var(--sidebar-width) - 3px);
  z-index: 6;
  width: 6px;
  cursor: col-resize;
  touch-action: none;
}

/* 悬停 / 拖动时显示一条细线，提示这里是可拖拽的分界 */
.sidebar-grip::before {
  content: "";
  position: absolute;
  top: 0;
  bottom: 0;
  left: 2px;
  width: 2px;
  border-radius: 2px;
  background: var(--accent);
  opacity: 0;
  transition: opacity 0.15s;
}

.sidebar-grip:hover::before,
.sidebar-grip.is-active::before {
  opacity: 0.6;
}

.content {
  position: relative;
  flex: 1;
  min-width: 0;
  padding: 18px 24px 24px;
  overflow-y: auto;
  /* 常驻滚动条槽位：切换视图时列表宽度不跳，滚动条也不压住表格右侧的按钮 */
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: rgb(20 23 44 / 0.16) transparent;
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
  background: var(--surface-2);
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

.folder-empty {
  margin: 16px 0 0;
  color: var(--ink-3);
  font-size: 13px;
  text-align: center;
}

.drop-hint {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 14px 0 0;
  color: var(--ink-3);
  font-size: 12px;
}

.drop-hint-icon {
  display: inline-flex;
}

.drop-hint-icon:deep(svg) {
  width: 14px;
  height: 14px;
}

/* ---------- 拖放遮罩 / 提示 ---------- */

.drop-overlay {
  position: fixed;
  inset: 0;
  z-index: 70;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
  background: rgb(22 25 38 / 0.45);
  backdrop-filter: blur(2px);
  color: #fff;
  pointer-events: none;
}

.drop-overlay-icon:deep(svg) {
  width: 46px;
  height: 46px;
}

.drop-overlay-text {
  margin: 0;
  font-size: 17px;
  font-weight: 600;
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

/* ---------- 窄屏：侧栏变抽屉 ---------- */

@media (max-width: 900px) {
  .hamburger {
    display: inline-flex;
  }

  .brand-name {
    display: none;
  }

  .search {
    width: auto;
    flex: 1;
    margin-left: 0;
  }

  .top-actions .btn:not(.primary) {
    display: none;
  }

  .top-actions .btn.primary {
    width: 34px;
    justify-content: center;
    padding: 0;
    font-size: 0;
    gap: 0;
  }

  .top-actions .btn.primary .btn-icon:deep(svg) {
    width: 17px;
    height: 17px;
  }

  .top-actions :deep(.locale-switch) {
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
    padding: 14px 14px 20px;
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
}
</style>