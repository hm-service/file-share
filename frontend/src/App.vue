<script setup lang="ts">
/**
 * 主页面（/）的界面壳：左侧导航（最近 / 收藏 / 分组） + 文件列表 + 浮动上传坞。
 *
 * 列表数据只在这里维护；每行的操作成功后重新拉一次列表，保持与服务端一致
 * （收藏 / 分组是全局的，上传完成后也可能出现新记录）。
 *
 * 视图与地址栏一一对应：`/`「最近」、`/favorites`「收藏」、`/ungrouped`「未分组」、
 * `/<分组名>`「单个分组」（名字按 URL 转义）。切换视图用 pushState 进历史，
 * 浏览器前进 / 后退与直接打开链接都通过 routes.ts 还原。
 *
 * 分组是服务端的独立记录（不再由文件集合现算），空分组也保留；「最近」视图按上传时间
 * 倒序、按本地日期分段、只列最近 20 个文件（搜索时在全部文件里匹配），其余视图按
 * sort_order 平铺，只有后者参与拖拽排序。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import Sortable from "sortablejs";

import ConfirmDialog from "@/components/ConfirmDialog.vue";
import FileList from "@/components/FileList.vue";
import LocaleSwitch from "@/components/LocaleSwitch.vue";
import UploadDock from "@/components/UploadDock.vue";
import {
  ApiError,
  createGroup,
  deleteFile,
  deleteGroup,
  downloadFile,
  listFiles,
  renameFile,
  renameGroup,
  reorderFiles,
  reorderGroups,
  toggleFavorite,
  updateFileGroup,
} from "@/api";
import { formatDayHeading } from "@/format";
import { ICONS } from "@/icons";
import { t } from "@/i18n";
import { viewFromPath, viewPath, type View } from "@/routes";
import { toLogin } from "@/session";
import type { FileItem, GroupItem } from "@/types";

const files = ref<FileItem[]>([]);
const loaded = ref(false);
const refreshing = ref(false);
let refreshSeq = 0;
const loadError = ref("");
const actionError = ref("");
let actionErrorTimer = 0;

/** 当前视图：最近 / 收藏 / 单个分组 / 未分组。 */
const view = ref<View>({ kind: "recent" });
const query = ref("");

const uploadDock = ref<InstanceType<typeof UploadDock> | null>(null);

/* ---------- 分组 ---------- */

/** 全部分组（含空分组）。同一父级内的顺序由服务端 sort_order 决定（拖动可调整）。 */
const groups = ref<GroupItem[]>([]);

/** 顶级分组：侧栏第一层、移动端 chips 第一行。 */
const topGroups = computed(() => groups.value.filter((group) => group.parent === ""));

/** 某个分组下的子分组。 */
function childrenOf(path: string): GroupItem[] {
  return groups.value.filter((group) => group.parent === path);
}

/**
 * 折叠起来的一级分组路径：只影响侧栏是否展开它的子分组，
 * 不写进地址栏、不落盘，刷新后恢复全部展开。
 */
const foldedGroups = ref<Set<string>>(new Set());

function isGroupFolded(path: string): boolean {
  return foldedGroups.value.has(path);
}

function toggleGroupFold(path: string): void {
  const next = new Set(foldedGroups.value);
  if (next.has(path)) next.delete(path);
  else next.add(path);
  foldedGroups.value = next;
}

/** 分组改名 / 删除后同步折叠集合，免得旧路径一直留在里面。 */
function renameFoldedGroup(oldPath: string, newPath: string): void {
  if (!foldedGroups.value.has(oldPath)) return;
  const next = new Set(foldedGroups.value);
  next.delete(oldPath);
  next.add(newPath);
  foldedGroups.value = next;
}

function forgetFoldedGroup(path: string): void {
  let changed = false;
  const next = new Set<string>();
  for (const entry of foldedGroups.value) {
    if (entry === path || entry.startsWith(`${path}/`)) changed = true;
    else next.add(entry);
  }
  if (changed) foldedGroups.value = next;
}

/** 每个分组的直属文件数（父分组的 badge 不含子分组，和分组视图里看到的列表一致）。 */
const groupCounts = computed(() => {
  const counts = new Map<string, number>();
  for (const file of files.value) {
    if (file.group_path) counts.set(file.group_path, (counts.get(file.group_path) ?? 0) + 1);
  }
  return counts;
});

const ungroupedCount = computed(() => {
  let count = 0;
  for (const file of files.value) {
    if (!file.group_path) count += 1;
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

/** 「最近」视图最多列出的文件数。 */
const RECENT_LIMIT = 20;

/** 侧栏「最近」的徽标：和列表一致，最多显示 20。 */
const recentCount = computed(() => Math.min(files.value.length, RECENT_LIMIT));

/** 「最近」视图的固定顺序：上传时间倒序，同一秒按 id 稳定排序；不参与拖拽。 */
const timeOrderedFiles = computed(() => {
  return [...files.value].sort((a, b) => {
    if (a.created_at !== b.created_at) return b.created_at - a.created_at;
    return a.id.localeCompare(b.id);
  });
});

/**
 * 当前视图的可见文件：最近按时间倒序、只取前 20 个（有关键词时在全部文件里匹配，
 * 不再截断，免得旧文件搜不到）；其余视图按用户排定的 sort_order，最后统一按关键词过滤。
 */
const visibleFiles = computed(() => {
  const base = (isRecent.value ? timeOrderedFiles.value : sortedFiles.value).filter(matchesQuery);
  const current = view.value;
  switch (current.kind) {
    case "favorites":
      return base.filter((file) => file.is_favorite);
    case "group":
      return base.filter((file) => file.group_path === current.group);
    case "ungrouped":
      return base.filter((file) => !file.group_path);
    default:
      return keyword.value ? base : base.slice(0, RECENT_LIMIT);
  }
});

/** 列表分段：标题为空表示只有一段（收藏 / 分组 / 未分组视图不显示日期标题）。 */
interface FileSection {
  key: string;
  title: string;
  files: FileItem[];
}

/**
 * 「最近」按本地日期分段，新的在上：列表已按上传时间倒序，同一天的文件天然相邻，
 * 顺序扫一遍即可切段；其余视图返回单段，不显示标题。
 */
const visibleSections = computed<FileSection[]>(() => {
  const list = visibleFiles.value;
  if (!isRecent.value) return [{ key: "single", title: "", files: list }];
  const sections: FileSection[] = [];
  let current: FileSection | null = null;
  for (const file of list) {
    const date = new Date(file.created_at * 1000);
    const key = `${date.getFullYear()}-${date.getMonth() + 1}-${date.getDate()}`;
    if (!current || current.key !== key) {
      current = { key, title: formatDayHeading(file.created_at), files: [] };
      sections.push(current);
    }
    current.files.push(file);
  }
  return sections;
});

const pageTitle = computed(() => {
  switch (view.value.kind) {
    case "favorites":
      return t("nav.favorites");
    case "ungrouped":
      return t("nav.ungrouped");
    case "group":
      return view.value.group;
    default:
      return t("nav.recent");
  }
});
const pageCount = computed(() => visibleFiles.value.length);
/** 「最近」列表被截断：只有这时副标题才解释「20 个 / 共 N 个」。 */
const recentCapped = computed(
  () => isRecent.value && keyword.value.length === 0 && files.value.length > RECENT_LIMIT,
);
const emptyKey = computed(() => {
  if (keyword.value) return "nav.no-results" as const;
  return view.value.kind === "favorites" ? ("nav.empty-favorites" as const) : ("nav.empty-recent" as const);
});
const emptyIcon = computed(() => {
  if (view.value.kind === "favorites") return ICONS.star;
  if (view.value.kind === "group" || view.value.kind === "ungrouped") return ICONS.folder;
  return ICONS.file;
});

const isRecent = computed(() => view.value.kind === "recent");
const isFavorites = computed(() => view.value.kind === "favorites");
const isUngrouped = computed(() => view.value.kind === "ungrouped");
const isGroupView = computed(() => view.value.kind === "group");
const isGroupActive = (path: string): boolean =>
  view.value.kind === "group" && view.value.group === path;
/** 当前视图正停在 path 自身或它的子分组里。 */
function groupHoldsView(path: string): boolean {
  const current = view.value;
  if (current.kind !== "group") return false;
  return current.group === path || current.group.startsWith(`${path}/`);
}
/** 折叠时子分组不可见，让父行替它显示「当前位置」；展开时各高亮各自的行。 */
function isGroupRowActive(path: string): boolean {
  return isGroupActive(path) || (isGroupFolded(path) && groupHoldsView(path));
}

/** 当前视图所属的顶级分组；不在分组视图时为空串。 */
const currentTopGroup = computed(() => {
  const current = view.value;
  if (current.kind !== "group") return "";
  const slash = current.group.indexOf("/");
  return slash < 0 ? current.group : current.group.slice(0, slash);
});

/** 当前顶级分组下的子分组：窄屏第二行 chips 只展示这一层。 */
const currentChildren = computed(() =>
  currentTopGroup.value ? childrenOf(currentTopGroup.value) : [],
);

/** 只有按 sort_order 排列的视图才开放拖拽；「最近」是固定时间序，搜索时 DOM 只是子集。 */
const canSort = computed(() => !sortingDisabled.value && !isRecent.value);

/** 当前分组内的文件数（与搜索过滤无关）：为 0 时不显示「全部下载」。 */
const currentGroupCount = computed(() => {
  if (view.value.kind !== "group" || !view.value.group) return 0;
  return groupCounts.value.get(view.value.group) ?? 0;
});

/**
 * 切换视图并同步地址栏。点导航用 push（进历史）；回退 / 修正失效链接用 replace
 * （不留下一个打开就跳走的条目）。
 */
function applyView(next: View, mode: "push" | "replace" = "push"): void {
  view.value = next;
  const path = viewPath(next);
  if (location.pathname === path) return;
  if (mode === "push") history.pushState(null, "", path);
  else history.replaceState(null, "", path);
}

/** 按地址栏还原视图（首次加载与前进 / 后退都走这里）；解不开的路径回退到「最近」。 */
function syncViewFromLocation(): void {
  const next = viewFromPath(location.pathname);
  if (!next) {
    applyView({ kind: "recent" }, "replace");
    return;
  }
  view.value = next;
  const canonical = viewPath(next);
  if (location.pathname !== canonical) history.replaceState(null, "", canonical);
}

function showRecent(): void {
  applyView({ kind: "recent" });
}

function showFavorites(): void {
  applyView({ kind: "favorites" });
}

function showGroup(group: string): void {
  // 拖拽排序松手后浏览器还会补一个 click，别让它顺手切换视图。
  if (Date.now() < groupClickGuardUntil) return;
  applyView({ kind: "group", group });
}

function showUngrouped(): void {
  applyView({ kind: "ungrouped" });
}

/** 分组 / 未分组视图失去存在依据时回退「最近」：同步地址栏，刷新后不会停在空视图。 */
function validateView(): void {
  const current = view.value;
  if (current.kind === "group" && !groups.value.some((group) => group.path === current.group)) {
    applyView({ kind: "recent" }, "replace");
  } else if (current.kind === "ungrouped" && ungroupedCount.value === 0) {
    applyView({ kind: "recent" }, "replace");
  }
}

/* ---------- 分组顺序拖拽 ---------- */

/**
 * 每一层分组是一个独立的排序容器：顶级一层、每个父分组各一层。
 *
 * 顶级项是包含自己子列表的 wrapper（拖动父分组时子分组跟着走），所以顶级的可拖元素是
 * `[data-group-block]`，并用 filter 把内层的 `[data-subgroup]` 排除掉，免得点中子分组时
 * 启动外层的拖拽。容器按 key 存在 Map 里，v-for 重建时由 ref 回调负责挂载与销毁。
 */
const groupSortables = new Map<string, Sortable>();
/** 拖拽结束后的短暂刻度：用来吞掉松手时补发的 click。 */
let groupClickGuardUntil = 0;

function createGroupSortable(parent: string, container: HTMLElement): Sortable {
  const topLevel = parent === "";
  return Sortable.create(container, {
    animation: 150,
    // 分组项里有 <button>：Chrome 的 UA 样式让原生 DnD 对按钮不生效，
    // 统一用 Sortable 自己的 fallback 拖拽，顶层与子层行为一致。
    forceFallback: true,
    fallbackOnBody: true,
    fallbackTolerance: 2,
    draggable: topLevel ? "[data-group-block]" : "[data-subgroup]",
    // 折叠箭头是行内的独立按钮，别让它启动分组拖拽
    filter: topLevel ? "[data-subgroup], .nav-fold" : undefined,
    preventOnFilter: false,
    ghostClass: "group-drag-ghost",
    chosenClass: "group-drag-chosen",
    delay: 120,
    delayOnTouchOnly: true,
    touchStartThreshold: 4,
    onEnd: () => {
      groupClickGuardUntil = Date.now() + 250;
      // 顶级只读直接子元素：内层子分组也带 data-group-name，不能混进来。
      const items = topLevel
        ? Array.from(container.children).filter((node) => node.matches("[data-group-block]"))
        : Array.from(container.querySelectorAll<HTMLElement>("[data-subgroup]"));
      const ordered = items
        .map((node) => (node as HTMLElement).dataset.groupName ?? "")
        .filter((name) => name !== "");
      const current = childrenOf(parent).map((group) => group.name);
      if (ordered.length !== current.length || ordered.join("\n") === current.join("\n")) return;
      void applyGroupOrder(parent, ordered);
    },
  });
}

/** v-for 的 ref 回调：元素挂上/卸载时同步该层的 Sortable 实例。 */
function setGroupContainer(key: string, parent: string, el: unknown): void {
  const existing = groupSortables.get(key);
  if (existing && existing.el !== el) {
    existing.destroy();
    groupSortables.delete(key);
  }
  if (el instanceof HTMLElement && !groupSortables.has(key)) {
    groupSortables.set(key, createGroupSortable(parent, el));
  }
}

function destroyGroupSortables(): void {
  for (const instance of groupSortables.values()) instance.destroy();
  groupSortables.clear();
}

/** 拖拽后先按新顺序重排本地数组（同一父级内），再提交服务端；失败回滚到服务端顺序。 */
async function applyGroupOrder(parent: string, ordered: string[]): Promise<void> {
  const position = new Map(ordered.map((name, index) => [name, index] as const));
  groups.value = [...groups.value].sort((a, b) => {
    if (a.parent !== parent || b.parent !== parent) return 0;
    return (position.get(a.name) ?? 0) - (position.get(b.name) ?? 0);
  });
  try {
    await reorderGroups(parent, ordered);
  } catch (error) {
    showActionError(error);
    await refresh();
  }
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
  const groupName = view.value.group;
  const targets = sortedFiles.value.filter((file) => file.group_path === groupName);
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

/** 当前分组视图内的上传目标；在其它视图上传仍然等同于未分组。 */
const currentUploadGroup = computed(() => {
  return view.value.kind === "group" ? view.value.group : "";
});

/** 手动新建空分组：输入 "父/子" 建二级分组；创建后直接进入该分组，方便马上往里传文件。 */
async function onCreateGroup(): Promise<void> {
  const input = window.prompt(t("group.new-prompt"));
  if (input === null) return;
  const path = input.trim();
  if (!path) return;
  try {
    const created = await createGroup(path);
    await refresh();
    applyView({ kind: "group", group: created.path });
  } catch (error) {
    showActionError(error);
  }
}

/** 重命名当前分组：只改最后一段，父路径不变；成功后地址同步改名。 */
/**
 * 重命名分组：只改路径最后一段。
 * 当前视图正看着这个分组（或它的子分组）时，地址按新路径同步改名 —— 改名顶级分组会
 * 级联改子分组路径，所以当前停在子分组上也要跟着走。
 */
async function onRenameGroup(path: string, currentName: string): Promise<void> {
  const input = window.prompt(t("group.rename-prompt"), currentName);
  if (input === null) return;
  const newName = input.trim();
  if (!newName || newName === currentName) return;
  const slash = path.lastIndexOf("/");
  const parent = slash < 0 ? "" : path.slice(0, slash);
  const nextPath = parent ? `${parent}/${newName}` : newName;
  try {
    await renameGroup(path, newName);
    renameFoldedGroup(path, nextPath);
    const current = view.value;
    if (current.kind === "group" && (current.group === path || current.group.startsWith(`${path}/`))) {
      const rest = current.group.slice(path.length);
      applyView({ kind: "group", group: `${nextPath}${rest}` }, "replace");
    }
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

/* ---------- 分组右键菜单（创建子分组 / 删除） ---------- */

/** 打开中的分组菜单：位置按点击点计算，夹在视口内。 */
const groupMenu = ref<{ path: string; name: string; x: number; y: number } | null>(null);
/** 等待确认删除的分组。 */
const deleteTarget = ref<{ path: string; name: string } | null>(null);

// 与 .row-menu 的 width 一致，用于把菜单夹在视口内。
const GROUP_MENU_WIDTH = 240;

function openGroupMenuAt(x: number, y: number, group: GroupItem): void {
  groupMenu.value = {
    path: group.path,
    name: group.name,
    x: Math.min(x, Math.max(8, window.innerWidth - GROUP_MENU_WIDTH - 8)),
    y: Math.min(y, Math.max(8, window.innerHeight - 120)),
  };
}

/** 侧栏 / chips 上的右键：分组条目的操作入口。 */
function openGroupMenu(event: MouseEvent, group: GroupItem): void {
  event.preventDefault();
  event.stopPropagation();
  openGroupMenuAt(event.clientX, event.clientY, group);
}

/** 标题旁的分组操作按钮：锚在按钮左下角，移动端没有右键时用它。 */
function openGroupMenuFromButton(event: MouseEvent): void {
  if (view.value.kind !== "group") return;
  const current = view.value.group;
  const group = groups.value.find((entry) => entry.path === current);
  if (!group) return;
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  openGroupMenuAt(rect.left, rect.bottom + 6, group);
}

function closeGroupMenu(): void {
  groupMenu.value = null;
}

/** 关闭分组菜单要点的「别处」：在别处按下指针就收起（菜单项自己的点击不算）。 */
function onGroupMenuPointerDown(event: PointerEvent): void {
  const target = event.target;
  if (target instanceof Node && (target as Element).closest?.(".group-menu-pop, .title-action-group")) return;
  closeGroupMenu();
}

watch(groupMenu, (value) => {
  if (value) window.addEventListener("pointerdown", onGroupMenuPointerDown, true);
  else window.removeEventListener("pointerdown", onGroupMenuPointerDown, true);
});

/** 在分组下新建子分组：只输入子分组名，父路径沿用右键的那一项。 */
async function onCreateSubgroup(): Promise<void> {
  const parent = groupMenu.value;
  closeGroupMenu();
  if (!parent) return;
  const input = window.prompt(t("group.new-subgroup-prompt"));
  if (input === null) return;
  const name = input.trim();
  if (!name) return;
  try {
    const created = await createGroup(`${parent.path}/${name}`);
    await refresh();
    applyView({ kind: "group", group: created.path });
  } catch (error) {
    showActionError(error);
  }
}

/** 分组（含子分组）里的文件数：删除确认框据此说明影响范围。 */
const deleteFileCount = computed(() => {
  const target = deleteTarget.value?.path;
  if (!target) return 0;
  return files.value.filter(
    (file) => file.group_path === target || file.group_path.startsWith(`${target}/`),
  ).length;
});

/** 不删文件时，它们会落到哪里：子分组回到上级，顶级分组回到未分组。 */
const deleteFallbackName = computed(() => {
  const path = deleteTarget.value?.path ?? "";
  const slash = path.lastIndexOf("/");
  if (slash < 0) return t("nav.ungrouped");
  const parent = path.slice(0, slash);
  return groups.value.find((group) => group.path === parent)?.name ?? t("nav.ungrouped");
});

function onRenameGroupFromMenu(): void {
  const target = groupMenu.value;
  closeGroupMenu();
  if (!target) return;
  void onRenameGroup(target.path, target.name);
}

function onDeleteGroup(): void {
  const target = groupMenu.value;
  closeGroupMenu();
  if (!target) return;
  deleteTarget.value = { path: target.path, name: target.name };
}

async function confirmDeleteGroup(deleteFiles: boolean): Promise<void> {
  const target = deleteTarget.value;
  if (!target) return;
  deleteTarget.value = null;
  try {
    await deleteGroup(target.path, deleteFiles);
    forgetFoldedGroup(target.path);
    // 视图停在被删分组（或它的子分组）里时，先回退到上级，避免刷新后闪一下「最近」。
    const current = view.value;
    if (current.kind === "group" && (current.group === target.path || current.group.startsWith(`${target.path}/`))) {
      const slash = target.path.lastIndexOf("/");
      applyView(slash < 0 ? { kind: "recent" } : { kind: "group", group: target.path.slice(0, slash) }, "replace");
    }
    await refresh();
  } catch (error) {
    showActionError(error);
  }
}

/** 移动到分组（空串 = 未分组；分组路径随文件出现自动登记）。 */
async function onMoveGroup(file: FileItem, groupPath: string): Promise<void> {
  if (file.group_path === groupPath) return;
  try {
    await updateFileGroup(file.id, groupPath);
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
async function onGroupDrop(event: DragEvent, groupPath: string): Promise<void> {
  event.preventDefault();
  const fileId = draggingFileId.value ?? "";
  draggingFileId.value = null;
  groupDropTarget.value = null;
  const file = files.value.find((entry) => entry.id === fileId);
  if (!file || file.group_path === groupPath) return;
  try {
    await updateFileGroup(fileId, groupPath);
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
  window.addEventListener("popstate", syncViewFromLocation);
  syncViewFromLocation();
  void refresh();
});

onBeforeUnmount(() => {
  document.removeEventListener("dragover", swallowDrop);
  document.removeEventListener("drop", swallowDrop);
  document.removeEventListener("dragend", endFileDrag);
  window.removeEventListener("popstate", syncViewFromLocation);
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

      <nav class="nav" :aria-label="t('nav.views')">
        <button type="button" class="nav-item" :class="{ active: isRecent }" :aria-current="isRecent ? 'page' : undefined" @click="showRecent">
          <span class="nav-icon" v-html="ICONS.file"></span>
          <span class="nav-label">{{ t("nav.recent") }}</span>
          <span class="nav-count">{{ recentCount }}</span>
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
      <div class="nav nav-groups" :ref="(el) => setGroupContainer('nav:', '', el)">
        <div
          v-for="group in topGroups"
          :key="group.path"
          class="nav-group-block"
          data-group-block
          :data-group-name="group.name"
          :class="{ 'is-drop-target': groupDropTarget === group.path }"
          @dragenter="onGroupDragEnter($event, group.path)"
          @dragover="onGroupDragOver($event, group.path)"
          @dragleave="onGroupDragLeave($event, group.path)"
          @drop="onGroupDrop($event, group.path)"
        >
          <div class="nav-group-row" @contextmenu="openGroupMenu($event, group)">
            <button
              type="button"
              class="nav-item nav-item-group"
              :class="{ active: isGroupRowActive(group.path) }"
              :aria-current="isGroupRowActive(group.path) ? 'page' : undefined"
              @click="showGroup(group.path)"
            >
              <span class="nav-icon" v-html="ICONS.folder"></span>
              <span class="nav-label">{{ group.name }}</span>
              <span class="nav-count">{{ groupCounts.get(group.path) ?? 0 }}</span>
            </button>
            <button
              v-if="childrenOf(group.path).length"
              type="button"
              class="nav-fold"
              :class="{ 'is-folded': isGroupFolded(group.path) }"
              :aria-expanded="!isGroupFolded(group.path)"
              :aria-label="t(isGroupFolded(group.path) ? 'action.expand-group' : 'action.collapse-group')"
              :title="t(isGroupFolded(group.path) ? 'action.expand-group' : 'action.collapse-group')"
              @click="toggleGroupFold(group.path)"
            >
              <span class="nav-fold-icon" v-html="ICONS.chevron"></span>
            </button>
          </div>

          <!-- 二级分组：缩进一层，只在本层内拖拽排序 -->
          <div
            v-if="childrenOf(group.path).length && !isGroupFolded(group.path)"
            class="nav nav-subgroups"
            :ref="(el) => setGroupContainer(`nav:${group.path}`, group.path, el)"
          >
            <button
              v-for="child in childrenOf(group.path)"
              :key="child.path"
              type="button"
              class="nav-item nav-item-subgroup"
              data-subgroup
              :data-group-name="child.name"
              :class="{ active: isGroupActive(child.path), 'is-drop-target': groupDropTarget === child.path }"
              :aria-current="isGroupActive(child.path) ? 'page' : undefined"
              @click="showGroup(child.path)"
              @contextmenu="openGroupMenu($event, child)"
              @dragenter.stop="onGroupDragEnter($event, child.path)"
              @dragover.stop="onGroupDragOver($event, child.path)"
              @dragleave.stop="onGroupDragLeave($event, child.path)"
              @drop.stop="onGroupDrop($event, child.path)"
            >
              <span class="nav-icon" v-html="ICONS.folder"></span>
              <span class="nav-label">{{ child.name }}</span>
              <span class="nav-count">{{ groupCounts.get(child.path) ?? 0 }}</span>
            </button>
          </div>
        </div>
        <p v-if="!topGroups.length" class="nav-empty">{{ t("nav.no-groups") }}</p>
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
      <div class="nav nav-new">
        <button type="button" class="nav-item nav-item-new" @click="onCreateGroup">
          <span class="nav-icon" v-html="ICONS.plus"></span>
          <span class="nav-label">{{ t("action.new-group") }}</span>
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
            <!-- 分组操作：桌面主要走侧栏右键，这里给窄屏（没有右键）留入口 -->
            <button
              v-if="isGroupView"
              type="button"
              class="title-action title-action-group"
              :class="{ on: groupMenu !== null }"
              :title="t('action.group-menu')"
              :aria-label="t('action.group-menu')"
              aria-haspopup="menu"
              :aria-expanded="groupMenu !== null"
              @click="openGroupMenuFromButton"
            >
              <span v-html="ICONS.more"></span>
            </button>
          </div>
          <p class="page-sub">
            {{
              recentCapped
                ? t("nav.recent-count", { shown: pageCount, total: files.length })
                : t("nav.file-count", { count: pageCount })
            }}
          </p>
        </div>
        <div class="page-actions">
          <!-- 只看单个分组时提供「全部下载」；「最近 / 收藏 / 未分组」没有稳定的集合边界，不提供。 -->
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
      <div class="tabs" role="tablist" :aria-label="t('nav.views')">
        <button
          type="button"
          class="tab"
          role="tab"
          :class="{ active: isRecent }"
          :aria-selected="isRecent"
          @click="showRecent"
        >
          {{ t("nav.recent") }}
          <span class="tab-count">{{ recentCount }}</span>
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

      <!-- 窄屏分组 chips：第一行顶级分组，第二行是当前所属顶级分组的子分组 -->
      <div
        v-show="topGroups.length || ungroupedCount > 0"
        class="chips"
        role="tablist"
        :aria-label="t('nav.groups')"
      >
        <div class="chips-groups" :ref="(el) => setGroupContainer('chips:', '', el)">
          <div
            v-for="group in topGroups"
            :key="group.path"
            class="chip-block"
            data-group-block
            :data-group-name="group.name"
            :class="{ 'is-drop-target': groupDropTarget === group.path }"
            @dragenter="onGroupDragEnter($event, group.path)"
            @dragover="onGroupDragOver($event, group.path)"
            @dragleave="onGroupDragLeave($event, group.path)"
            @drop="onGroupDrop($event, group.path)"
          >
            <button
              type="button"
              class="chip"
              role="tab"
              :class="{ active: isGroupActive(group.path) }"
              :aria-selected="isGroupActive(group.path)"
              @click="showGroup(group.path)"
              @contextmenu="openGroupMenu($event, group)"
            >
              <span class="chip-icon" v-html="ICONS.folder"></span>
              <span class="chip-label">{{ group.name }}</span>
            </button>
          </div>
          <button
            type="button"
            class="chip chip-new"
            :title="t('action.new-group')"
            :aria-label="t('action.new-group')"
            @click="onCreateGroup"
          >
            <span class="chip-icon" v-html="ICONS.plus"></span>
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
      <div
        v-if="currentChildren.length"
        class="chips chips-subgroups"
        role="tablist"
        :aria-label="t('nav.groups')"
      >
        <div
          class="chips-groups"
          :ref="(el) => setGroupContainer(`chips:${currentTopGroup}`, currentTopGroup, el)"
        >
          <button
            v-for="child in currentChildren"
            :key="child.path"
            type="button"
            class="chip chip-subgroup"
            role="tab"
            data-subgroup
            :data-group-name="child.name"
            :class="{ active: isGroupActive(child.path), 'is-drop-target': groupDropTarget === child.path }"
            :aria-selected="isGroupActive(child.path)"
            @click="showGroup(child.path)"
            @contextmenu="openGroupMenu($event, child)"
            @dragenter.stop="onGroupDragEnter($event, child.path)"
            @dragover.stop="onGroupDragOver($event, child.path)"
            @dragleave.stop="onGroupDragLeave($event, child.path)"
            @drop.stop="onGroupDrop($event, child.path)"
          >
            <span class="chip-icon" v-html="ICONS.folder"></span>
            <span class="chip-label">{{ child.name }}</span>
          </button>
        </div>
      </div>

      <section class="content">
        <div v-if="loadError || actionError" class="banners">
          <p v-if="loadError" class="banner" role="alert">{{ loadError }}</p>
          <p v-if="actionError" class="banner" role="alert">{{ actionError }}</p>
        </div>

        <!-- 最近视图按上传时间倒序、按本地日期分段；其余视图按 sort_order 平铺成一张卡片 -->
        <div v-if="visibleFiles.length" class="file-sections">
          <section v-for="section in visibleSections" :key="section.key" class="file-section">
            <h2 v-if="section.title" class="file-section-title">{{ section.title }}</h2>
            <FileList
              :files="section.files"
              :groups="groups"
              :sortable="canSort"
              @favorite="onFavorite"
              @download="onDownload"
              @rename="onRename"
              @error="showActionError"
              @delete="onDelete"
              @move-group="onMoveGroup"
              @reorder="onReorder"
              @drag-start="startFileDrag"
              @drag-end="endFileDrag"
            />
          </section>
        </div>
        <div v-else class="empty">
          <span class="empty-icon" v-html="emptyIcon"></span>
          <p class="empty-title">{{ loaded ? t(emptyKey) : t("state.loading") }}</p>
          <template v-if="loaded && !query.trim() && (isRecent || isFavorites || isGroupView)">
            <p v-if="isRecent" class="empty-text">
              <span class="desktop-only">{{ t("upload.hint") }}</span>
              <span class="mobile-only">{{ t("upload.hint-mobile") }}</span>
            </p>
            <p v-else-if="isGroupView" class="empty-text">{{ t("upload.group-hint") }}</p>
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

    <!-- 分组右键菜单 -->
    <Teleport to="body">
      <div
        v-if="groupMenu"
        class="row-menu group-menu-pop"
        :style="{ left: `${groupMenu.x}px`, top: `${groupMenu.y}px` }"
        role="menu"
        :aria-label="groupMenu.name"
      >
        <button type="button" class="row-menu-item" role="menuitem" @click="onRenameGroupFromMenu">
          <span class="row-menu-icon" v-html="ICONS.pencil"></span>
          <span class="row-menu-label">{{ t("action.rename-group") }}</span>
        </button>
        <button type="button" class="row-menu-item" role="menuitem" @click="onCreateSubgroup">
          <span class="row-menu-icon" v-html="ICONS.folder"></span>
          <span class="row-menu-label">{{ t("action.create-subgroup") }}</span>
        </button>
        <div class="row-menu-sep" role="separator"></div>
        <button type="button" class="row-menu-item is-danger" role="menuitem" @click="onDeleteGroup">
          <span class="row-menu-icon" v-html="ICONS.trash"></span>
          <span class="row-menu-label">{{ t("action.delete-group") }}</span>
        </button>
      </div>
    </Teleport>

    <ConfirmDialog
      v-if="deleteTarget"
      :title="t('group.delete-title', { name: deleteTarget.name })"
      :message="
        deleteFileCount
          ? t('group.delete-message', { count: deleteFileCount, target: deleteFallbackName })
          : t('group.delete-empty')
      "
      :checkbox="deleteFileCount ? t('group.delete-files', { count: deleteFileCount }) : undefined"
      :checkbox-hint="deleteFileCount ? t('group.delete-files-hint') : undefined"
      :confirm-label="t('action.delete-group')"
      @confirm="confirmDeleteGroup"
      @cancel="deleteTarget = null"
    />

    <UploadDock ref="uploadDock" :target-group="currentUploadGroup" @uploaded="refresh" />
  </div>
</template>
