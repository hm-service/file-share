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
import PromptDialog from "@/components/PromptDialog.vue";
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

/* ---------- 侧栏宽度（桌面端拖右缘调节） ---------- */

const SIDEBAR_DEFAULT_WIDTH = 264;
const SIDEBAR_MIN_WIDTH = 200;
const SIDEBAR_WIDTH_KEY = "file-share.sidebar-width";

/** 上限取视口 2/3：主区至少留 1/3。 */
function maxSidebarWidth(): number {
  return Math.max(SIDEBAR_MIN_WIDTH, Math.floor(window.innerWidth * (2 / 3)));
}

function clampSidebarWidth(width: number): number {
  return Math.min(Math.max(Math.round(width), SIDEBAR_MIN_WIDTH), maxSidebarWidth());
}

function loadSidebarWidth(): number {
  const stored = Number.parseInt(localStorage.getItem(SIDEBAR_WIDTH_KEY) ?? "", 10);
  return Number.isFinite(stored) ? clampSidebarWidth(stored) : SIDEBAR_DEFAULT_WIDTH;
}

/** 侧栏宽度只存在本机：切视图、刷新、重启都保持用户拖出来的宽度。 */
const sidebarWidth = ref(loadSidebarWidth());
const resizingSidebar = ref(false);
let stopSidebarResize: (() => void) | null = null;

/**
 * 按住两栏边界的 gridgrip 拖动：宽度实时跟随指针，放手后落盘。
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
    localStorage.setItem(SIDEBAR_WIDTH_KEY, String(sidebarWidth.value));
  };
  stopSidebarResize = finish;
  window.addEventListener("pointermove", onMove);
  window.addEventListener("pointerup", finish);
  window.addEventListener("pointercancel", finish);
}

/** 窗口变窄时把侧栏夹回 2/3 以内（用户存下的宽度不覆盖，窗口变宽也不自动还原）。 */
function onWindowResize(): void {
  sidebarWidth.value = clampSidebarWidth(sidebarWidth.value);
}

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
 * 分组层数上限（与服务端 core.MaxGroupDepth 对齐）：
 * 一级 / 二级是分层，各自有页面路由；三级只是分组，只在二级的内容区分段里出现。
 */
const MAX_GROUP_DEPTH = 3;

/** 路径层数："a" → 1，"a/b" → 2，"a/b/c" → 3。 */
function groupDepth(path: string): number {
  return path.split("/").length;
}

/** 上一级路径："a/b/c" → "a/b"，顶级分组 → 空串。 */
function parentPathOf(path: string): string {
  const slash = path.lastIndexOf("/");
  return slash < 0 ? "" : path.slice(0, slash);
}

/** 还能不能再建下一级：三级分组没有子分组，因此菜单里不再出现「创建子分组」。 */
function canCreateSubgroup(path: string): boolean {
  return groupDepth(path) < MAX_GROUP_DEPTH;
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

/** 双击一级分组行：展开 / 收起它的子分组（单击仍然是进入分组）。 */
function toggleGroupFoldByDoubleClick(path: string): void {
  if (childrenOf(path).length) toggleGroupFold(path);
}

/* ---------- 侧栏分组过滤 ---------- */

/** 侧栏分组过滤关键词：只筛侧栏导航，和文件列表的搜索框互不影响。 */
const groupQuery = ref("");
const groupKeyword = computed(() => groupQuery.value.trim().toLowerCase());

function groupMatches(group: GroupItem): boolean {
  return group.name.toLowerCase().includes(groupKeyword.value) || group.path.toLowerCase().includes(groupKeyword.value);
}

/** 侧栏展示的顶级分组：关键词命中自身或任一子分组时保留。 */
const sidebarTopGroups = computed(() => {
  if (!groupKeyword.value) return topGroups.value;
  return topGroups.value.filter((group) => groupMatches(group) || childrenOf(group.path).some(groupMatches));
});

/**
 * 侧栏里某个顶级分组下要展示的子分组：父分组自己命中时保留全部子分组，
 * 只有子分组命中时才按关键词裁掉不相关的那些。
 */
function sidebarChildren(group: GroupItem): GroupItem[] {
  const children = childrenOf(group.path);
  if (!groupKeyword.value || groupMatches(group)) return children;
  return children.filter(groupMatches);
}

/** 点击侧栏分组：先清掉过滤词，再进分组，否则侧栏会一直停在过滤后的子集上。 */
function openSidebarGroup(path: string): void {
  // 拖拽排序松手后浏览器补发的 click 由 showGroup 吞掉；这里也跳过，别顺手清了过滤词。
  if (Date.now() < groupClickGuardUntil) return;
  groupQuery.value = "";
  showGroup(path);
}

/**
 * 每个分组（含更深层分组）的文件数：侧栏徽标用它。
 * 三级分组只是分组，文件常常沉在三级里，父分组再按直属数显示就会是 0。
 */
const groupSubtreeCounts = computed(() => {
  const counts = new Map<string, number>();
  for (const file of files.value) {
    let path = file.group_path;
    while (path) {
      counts.set(path, (counts.get(path) ?? 0) + 1);
      path = parentPathOf(path);
    }
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

/** 某个分组（含更深层分组）下的全部文件，按列表顺序。 */
function subtreeFiles(path: string): FileItem[] {
  return sortedFiles.value.filter(
    (file) => file.group_path === path || file.group_path.startsWith(`${path}/`),
  );
}

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

/* ---------- 二级分组里的三级分组：内容区分段 ---------- */

/**
 * 三级分组只是分组：不进侧栏、不产生路由，只在二级分组的内容区里分段展示。
 *
 * 分段规则：当前分组下的每个子分组一段，段内是该子分组（含更深层）的全部文件；
 * 直接挂在当前分组上的文件单独成一段「直属文件」，只有存在时才出现。
 * 一级视图用同一套规则按二级分组分段，所以点一级分组也能看到它下面的全部文件。
 */
interface GroupSection {
  key: string;
  title: string;
  /** 段对应的子分组；「直属文件」段没有分组。 */
  group?: GroupItem;
  files: FileItem[];
}

/** 段筛选：all / direct（直属文件段）/ 某个子分组路径。 */
const sectionFilter = ref("all");
/** 折叠起来的段（按段 key），只影响展示。 */
const foldedSections = ref<Set<string>>(new Set());

const currentSubgroups = computed(() => {
  const current = view.value;
  return current.kind === "group" ? childrenOf(current.group) : [];
});

const groupSections = computed<GroupSection[]>(() => {
  const current = view.value;
  if (current.kind !== "group") return [];
  const children = currentSubgroups.value;
  if (!children.length) return [];
  const sections: GroupSection[] = [];
  const direct = sortedFiles.value.filter((file) => file.group_path === current.group);
  if (direct.length) sections.push({ key: "direct", title: t("group.direct-files"), files: direct });
  for (const child of children) {
    sections.push({ key: child.path, title: child.name, group: child, files: subtreeFiles(child.path) });
  }
  return sections;
});

/** 有子分组的分组视图才按段展示；没有子分组时仍是原来的平铺列表。 */
const isGroupedView = computed(() => groupSections.value.length > 0);

/**
 * 生效的段筛选：搜索时放宽到全部段 —— 命中可能落在别的段，只看当前段会漏。
 * 搜索结束后自动恢复原来选中的段。
 */
const effectiveSectionFilter = computed(() => (keyword.value ? "all" : sectionFilter.value));

/** 段筛选 + 文件名搜索后的段；搜索时只留下有命中的段。 */
const visibleGroupSections = computed<GroupSection[]>(() => {
  const filter = effectiveSectionFilter.value;
  const searching = keyword.value.length > 0;
  return groupSections.value
    .filter((section) => filter === "all" || section.key === filter)
    .map((section) => ({ ...section, files: section.files.filter(matchesQuery) }))
    .filter((section) => !searching || section.files.length > 0);
});

/** 当前视图里「看得见」的文件：分组视图下等于分段筛选 / 搜索之后的结果。 */
const pageFiles = computed(() =>
  isGroupedView.value
    ? visibleGroupSections.value.flatMap((section) => section.files)
    : visibleFiles.value,
);

/** 分组视图的总文件数（含更深层分组）：一级视图是整棵子树，二级视图是它下面的全部文件。 */
const currentGroupFiles = computed(() => {
  const current = view.value;
  return current.kind === "group" ? subtreeFiles(current.group) : [];
});

function isSectionFolded(key: string): boolean {
  return foldedSections.value.has(key);
}

function toggleSectionFold(key: string): void {
  // 拖拽排序松手后浏览器还会补一个 click，别让它顺手把段折起来。
  if (Date.now() < sectionClickGuardUntil) return;
  const next = new Set(foldedSections.value);
  if (next.has(key)) next.delete(key);
  else next.add(key);
  foldedSections.value = next;
}

/** 点段胶囊切换段：搜索中先清掉搜索，否则筛选会被「搜索放宽到全部段」盖住，点了看不出反应。 */
function selectSectionFilter(value: string): void {
  sectionFilter.value = value;
  if (query.value) query.value = "";
}

/** 段内文件来自同一个分组时才开放拖拽排序：一级视图的段混着多个三级分组，排序没有意义。 */
const sectionSortable = computed(
  () => canSort.value && view.value.kind === "group" && groupDepth(view.value.group) === 2,
);

/** 一级视图的段里混着三级分组的文件，行内补一个分组标签才看得出归属。 */
const sectionShowGroup = computed(
  () => view.value.kind === "group" && groupDepth(view.value.group) < 2,
);

/** 直属文件段：有直属文件时才出现，所以筛选条里也只有这时才有这一项。 */
const directSection = computed(() => groupSections.value.find((section) => section.key === "direct"));

/** 拖文件到段头 / 段胶囊时的目标分组：「直属文件」段落到当前分组本身。 */
function sectionDropPath(section: GroupSection): string {
  if (section.group) return section.group.path;
  return view.value.kind === "group" ? view.value.group : "";
}

/** 段头右键：段有对应分组时打开分组菜单。 */
function openSectionMenu(event: MouseEvent, section: GroupSection): void {
  if (section.group) openGroupMenu(event, section.group);
}

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
const pageCount = computed(() => pageFiles.value.length);
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

/** 地址栏查询参数：q = 搜索词，section = 内容区选中的分组段（direct / 分组路径）。 */
const SEARCH_PARAM = "q";
const SECTION_PARAM = "section";

/**
 * 把视图、搜索词与段筛选写进地址栏，链接复制出去就能定位到同一处。
 *
 * 换视图用 push 进历史；搜索词与段筛选用 replace：一次输入 / 一次点击不值得各留一条历史，
 * 但地址栏随时是完整的可分享状态。
 */
function syncLocation(mode: "push" | "replace"): void {
  const params = new URLSearchParams();
  const search = query.value.trim();
  if (search) params.set(SEARCH_PARAM, search);
  // 段筛选只属于分组视图：三级分组没有路由，靠这个参数定位到某一段。
  if (view.value.kind === "group" && sectionFilter.value !== "all") {
    params.set(SECTION_PARAM, sectionFilter.value);
  }
  const queryString = params.toString();
  const url = `${viewPath(view.value)}${queryString ? `?${queryString}` : ""}`;
  if (location.pathname + location.search === url) return;
  if (mode === "push") history.pushState(null, "", url);
  else history.replaceState(null, "", url);
}

/**
 * 切换视图并同步地址栏。点导航用 push（进历史）；回退 / 修正失效链接用 replace
 * （不留下一个打开就跳走的条目）。
 *
 * 换视图时重置段筛选与折叠：上一组选中的三级分组不该带到另一个分组里。
 * 新建三级分组只改筛选、不换视图，所以同一路径的重设要放过去（否则刚选中的段会被清掉）。
 */
function applyView(next: View, mode: "push" | "replace" = "push"): void {
  if (viewPath(next) !== viewPath(view.value)) {
    sectionFilter.value = "all";
    foldedSections.value = new Set();
  }
  view.value = next;
  syncLocation(mode);
}

/** 按地址栏还原视图、搜索词与段筛选（首次加载与前进 / 后退都走这里）。 */
function syncViewFromLocation(): void {
  const next = viewFromPath(location.pathname);
  const params = new URLSearchParams(location.search);
  const search = params.get(SEARCH_PARAM) ?? "";
  const section = params.get(SECTION_PARAM) ?? "";
  if (!next) {
    // 路径解不开：回到「最近」，搜索词还能用，段筛选没有归属。
    if (viewPath(view.value) !== "/") foldedSections.value = new Set();
    view.value = { kind: "recent" };
    query.value = search;
    sectionFilter.value = "all";
    syncLocation("replace");
    return;
  }
  if (viewPath(next) !== viewPath(view.value)) foldedSections.value = new Set();
  view.value = next;
  query.value = search;
  sectionFilter.value =
    next.kind === "group" && section && section !== "all" ? section : "all";
  syncLocation("replace");
  // 段筛选是否存在要等分组列表就绪后才校验；首次加载时由 refresh 之后的 validateView 兜底。
  if (loaded.value) validateView();
}

// 搜索词与段筛选任何来源的改动（输入、点胶囊、删除 / 改名后回退）都立即写回地址栏。
watch([query, sectionFilter], () => syncLocation("replace"));

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
  // 段筛选指向的分组不在了（被删除 / 改名），或分享链接里的段已经不存在时，回到「全部」。
  const filter = sectionFilter.value;
  const validChild =
    filter !== "all" &&
    filter !== "direct" &&
    currentSubgroups.value.some((group) => group.path === filter);
  if (filter !== "all" && filter !== "direct" && !validChild) {
    sectionFilter.value = "all";
  } else if (filter === "direct" && !directSection.value) {
    sectionFilter.value = "all";
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

/* ---------- 内容区分段拖拽排序（子分组顺序） ---------- */

/**
 * 分段顺序就是子分组的 sort_order：拖段头调整，落定后提交服务端。
 *
 * 「直属文件」段不是分组，固定在最上面：用 onMove 拦住别的段越过它。
 * 搜索 / 按段筛选时有些段不在 DOM 里，这时禁用拖动，免得把看不见的段顺序挤乱。
 * 一级视图的段是二级分组、二级视图的段是三级分组，两层都是同一个父级下的兄弟分组。
 */
const sectionOrderEnabled = computed(
  () =>
    !keyword.value &&
    sectionFilter.value === "all" &&
    groupSections.value.filter((section) => section.group).length > 1,
);

let sectionOrderSortable: Sortable | null = null;
/** 段拖拽结束后的短暂刻度：用来吞掉松手时补发的 click。 */
let sectionClickGuardUntil = 0;

function createSectionOrderSortable(el: HTMLElement): Sortable {
  const instance = Sortable.create(el, {
    animation: 150,
    forceFallback: true,
    fallbackOnBody: true,
    fallbackTolerance: 2,
    draggable: "[data-group-name]",
    // 只有段头能起拖：段体里是文件行，它有自己的拖拽与排序。
    handle: ".section-head",
    // 段头右侧的下载 / 菜单按钮不参与拖拽。
    filter: ".section-action",
    preventOnFilter: false,
    ghostClass: "group-drag-ghost",
    chosenClass: "group-drag-chosen",
    delay: 120,
    delayOnTouchOnly: true,
    touchStartThreshold: 4,
    onMove: (event) => !(event.related as HTMLElement).matches("[data-section-fixed]"),
    onEnd: () => {
      sectionClickGuardUntil = Date.now() + 250;
      if (!sectionOrderEnabled.value) return;
      const current = view.value;
      if (current.kind !== "group") return;
      const ordered = Array.from(el.querySelectorAll<HTMLElement>("[data-group-name]"))
        .map((node) => node.dataset.groupName ?? "")
        .filter((name) => name !== "");
      const children = childrenOf(current.group);
      if (ordered.length !== children.length) return;
      if (ordered.join("\n") === children.map((group) => group.name).join("\n")) return;
      void applyGroupOrder(current.group, ordered);
    },
  });
  instance.option("disabled", !sectionOrderEnabled.value);
  return instance;
}

/** v-if 的 ref 回调：分段容器挂上 / 卸载时创建 / 销毁排序实例。 */
function setSectionOrderContainer(el: unknown): void {
  if (!(el instanceof HTMLElement)) {
    sectionOrderSortable?.destroy();
    sectionOrderSortable = null;
    return;
  }
  if (!sectionOrderSortable) sectionOrderSortable = createSectionOrderSortable(el);
}

// 搜索或按段筛选时不具备完整段集合，随时开关拖动。
watch(sectionOrderEnabled, (enabled) => {
  sectionOrderSortable?.option("disabled", !enabled);
});

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
 * 逐个签下载地址并触发下载，每个文件是独立的一次下载。
 * 浏览器可能询问「是否允许下载多个文件」，允许后即可一次保存整批。
 */
async function onDownloadFiles(targets: FileItem[]): Promise<void> {
  if (!targets.length) return;
  try {
    for (const file of targets) {
      await downloadFile(file.id);
    }
  } catch (error) {
    showActionError(error);
  }
}

/** 分组「全部下载」：下载当前视图里看得见的文件（含三级分组段筛选 / 搜索的结果）。 */
async function onDownloadGroup(): Promise<void> {
  if (view.value.kind !== "group") return;
  await onDownloadFiles(pageFiles.value);
}

/* ---------- 输入 / 确认对话框 ---------- */

/**
 * 输入对话框正在问什么：所有「输入一个名字」的操作都列在这里，由同一个 PromptDialog 渲染。
 *
 * 不用 window.prompt / confirm：原生对话框带「阻止此页面创建更多对话框」勾选项，一旦被勾上
 * 就再也不出现，而页面又依赖它的返回值，等于相关操作永久失效、且极难改回来。
 */
type PromptState =
  | { kind: "rename-file"; file: FileItem }
  | { kind: "new-group" }
  /** 重命名分组：只改路径最后一段，父路径不变。 */
  | { kind: "rename-group"; path: string; name: string }
  /** 在某个分组下新建子分组：只输入子分组名。 */
  | { kind: "new-subgroup"; parent: string }
  /** 「移动到分组」菜单里新建分组：名字可以是 父/子，随后把文件移进去。 */
  | { kind: "move-file-new-group"; file: FileItem };

const promptState = ref<PromptState | null>(null);

/** 等待确认删除的文件。 */
const deleteFileTarget = ref<FileItem | null>(null);

/** 当前输入对话框的标题 / 说明 / 初始值 / 确认按钮文案。 */
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
    case "new-group":
      return {
        title: t("group.new-title"),
        hint: t("group.new-hint"),
        value: "",
        confirmLabel: t("action.create"),
      };
    case "rename-group":
      return {
        title: t("group.rename-title"),
        hint: "",
        value: state.name,
        confirmLabel: t("action.rename"),
      };
    case "new-subgroup":
      return {
        title: t("group.new-subgroup-title"),
        hint: "",
        value: "",
        confirmLabel: t("action.create"),
      };
    case "move-file-new-group":
      return {
        title: t("group.new-title"),
        hint: t("group.new-hint"),
        value: "",
        confirmLabel: t("action.move-group"),
      };
  }
});

/** 输入确认：先关对话框，再执行打开它的那个操作；失败统一进顶部提示。 */
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
    case "new-group": {
      try {
        const created = await createGroup(value);
        await refresh();
        openCreatedGroup(created.path);
      } catch (error) {
        showActionError(error);
      }
      return;
    }
    case "rename-group": {
      if (value === state.name) return;
      const slash = state.path.lastIndexOf("/");
      const parent = slash < 0 ? "" : state.path.slice(0, slash);
      const nextPath = parent ? `${parent}/${value}` : value;
      try {
        await renameGroup(state.path, value);
        renameFoldedGroup(state.path, nextPath);
        const filter = sectionFilter.value;
        if (filter === state.path) sectionFilter.value = nextPath;
        else if (filter.startsWith(`${state.path}/`)) {
          sectionFilter.value = nextPath + filter.slice(state.path.length);
        }
        const current = view.value;
        if (
          current.kind === "group" &&
          (current.group === state.path || current.group.startsWith(`${state.path}/`))
        ) {
          const rest = current.group.slice(state.path.length);
          applyView({ kind: "group", group: `${nextPath}${rest}` }, "replace");
        }
        await refresh();
      } catch (error) {
        showActionError(error);
      }
      return;
    }
    case "new-subgroup": {
      try {
        const created = await createGroup(`${state.parent}/${value}`);
        await refresh();
        openCreatedGroup(created.path);
      } catch (error) {
        showActionError(error);
      }
      return;
    }
    case "move-file-new-group": {
      await onMoveGroup(state.file, value);
      return;
    }
  }
}

/** 重命名文件：输入框预填当前名字；名字没改就不发请求。 */
function onRename(file: FileItem): void {
  promptState.value = { kind: "rename-file", file };
}

/** 删除文件：先让用户确认，确认后不可恢复。 */
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

/**
 * 上传目标：选中某个三级分组段时传到它，否则传到当前分组本身（其它视图等同于未分组）。
 * 和侧栏 / 段筛选保持一致，用户不用再选一次目标。
 */
const currentUploadGroup = computed(() => {
  if (view.value.kind !== "group") return "";
  const filter = sectionFilter.value;
  if (filter !== "all" && filter !== "direct") return filter;
  return view.value.group;
});

/** 选中三级分组段时，标题下注明上传会落到哪一组。 */
const uploadTargetName = computed(() => {
  const filter = sectionFilter.value;
  if (view.value.kind !== "group" || filter === "all" || filter === "direct") return "";
  return groups.value.find((group) => group.path === filter)?.name ?? "";
});

/** 新建分组后落到哪：二级及以内直接进视图；三级分组没有路由，进父分组并选中它那一段。 */
function openCreatedGroup(path: string): void {
  if (groupDepth(path) < MAX_GROUP_DEPTH) {
    applyView({ kind: "group", group: path });
    return;
  }
  applyView({ kind: "group", group: parentPathOf(path) });
  sectionFilter.value = path;
}

/** 手动新建空分组：输入 "父/子" 建二级分组，也可以 "父/子/孙" 一次建到三级。 */
function onCreateGroup(): void {
  promptState.value = { kind: "new-group" };
}

/**
 * 重命名分组：只改路径最后一段。
 * 当前视图正看着这个分组（或它的子分组）时，地址按新路径同步改名 —— 改名顶级分组会
 * 级联改子分组路径，所以当前停在子分组上也要跟着走。这些在确认处理里做。
 */
function onRenameGroup(path: string, currentName: string): void {
  promptState.value = { kind: "rename-group", path, name: currentName };
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

/** 三级分组段头旁的「⋯」：锚在按钮左下角，触摸屏没有右键时用它。 */
function openGroupMenuFromSection(event: MouseEvent, group: GroupItem): void {
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  openGroupMenuAt(rect.left - GROUP_MENU_WIDTH + 36, rect.bottom + 6, group);
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
function onCreateSubgroup(): void {
  const parent = groupMenu.value;
  closeGroupMenu();
  if (!parent) return;
  promptState.value = { kind: "new-subgroup", parent: parent.path };
}

/** 段胶囊行末尾的「+」：直接在当前分组下新建三级分组。 */
function onCreateChildGroup(): void {
  const current = view.value;
  if (current.kind !== "group") return;
  promptState.value = { kind: "new-subgroup", parent: current.group };
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
    const filter = sectionFilter.value;
    if (filter === target.path || filter.startsWith(`${target.path}/`)) sectionFilter.value = "all";
    foldedSections.value = new Set();
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

/** 「移动到分组」菜单里的新建分组：输入名字（可带 父/），随后把文件移进去。 */
function onNewGroupForFile(file: FileItem): void {
  promptState.value = { kind: "move-file-new-group", file };
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
  window.addEventListener("resize", onWindowResize);
  syncViewFromLocation();
  void refresh();
});

onBeforeUnmount(() => {
  document.removeEventListener("dragover", swallowDrop);
  document.removeEventListener("drop", swallowDrop);
  document.removeEventListener("dragend", endFileDrag);
  window.removeEventListener("popstate", syncViewFromLocation);
  window.removeEventListener("resize", onWindowResize);
  stopSidebarResize?.();
  destroyGroupSortables();
  window.clearTimeout(actionErrorTimer);
});
</script>

<template>
  <div
    class="shell"
    :class="{ 'is-resizing': resizingSidebar }"
    :style="{ '--sidebar-width': `${sidebarWidth}px` }"
  >
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

      <!-- 分组标题与过滤框同一行，把竖向空间都留给下面的分组列表 -->
      <div class="nav-groups-head">
        <p class="nav-section-title">{{ t("nav.groups") }}</p>
        <label class="group-search">
          <span class="group-search-icon" v-html="ICONS.search"></span>
          <input
            v-model="groupQuery"
            type="search"
            class="group-search-input"
            :placeholder="t('nav.group-search-placeholder')"
            :aria-label="t('nav.group-search-placeholder')"
          />
        </label>
      </div>

      <!-- 分组可拖拽排序：未分组不是真实分组，跟随列表排在最后、不参与排序 -->
      <div class="nav nav-groups" :ref="(el) => setGroupContainer('nav:', '', el)">
        <div
          v-for="group in sidebarTopGroups"
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
          <div class="nav-group-row" @contextmenu="openGroupMenu($event, group)" @dblclick="toggleGroupFoldByDoubleClick(group.path)">
            <button
              type="button"
              class="nav-item nav-item-group"
              :class="{ active: isGroupRowActive(group.path) }"
              :aria-current="isGroupRowActive(group.path) ? 'page' : undefined"
              @click="openSidebarGroup(group.path)"
            >
              <span class="nav-icon" v-html="ICONS.folder"></span>
              <span class="nav-label">{{ group.name }}</span>
              <span class="nav-count">{{ groupSubtreeCounts.get(group.path) ?? 0 }}</span>
            </button>
            <!-- 过滤时子分组自动展开，箭头没有意义，直接隐藏 -->
            <button
              v-if="!groupKeyword && sidebarChildren(group).length"
              type="button"
              class="nav-fold"
              :class="{ 'is-folded': isGroupFolded(group.path) }"
              :aria-expanded="!isGroupFolded(group.path)"
              :aria-label="t(isGroupFolded(group.path) ? 'action.expand-group' : 'action.collapse-group')"
              :title="t(isGroupFolded(group.path) ? 'action.expand-group' : 'action.collapse-group')"
              @click="toggleGroupFold(group.path)"
              @dblclick.stop
            >
              <span class="nav-fold-icon" v-html="ICONS.chevron"></span>
            </button>
          </div>

          <!-- 二级分组：缩进一层，只在本层内拖拽排序 -->
          <div
            v-if="sidebarChildren(group).length && (groupKeyword || !isGroupFolded(group.path))"
            class="nav nav-subgroups"
            :ref="(el) => setGroupContainer(`nav:${group.path}`, group.path, el)"
          >
            <button
              v-for="child in sidebarChildren(group)"
              :key="child.path"
              type="button"
              class="nav-item nav-item-subgroup"
              data-subgroup
              :data-group-name="child.name"
              :class="{ active: isGroupActive(child.path), 'is-drop-target': groupDropTarget === child.path }"
              :aria-current="isGroupActive(child.path) ? 'page' : undefined"
              @click="openSidebarGroup(child.path)"
              @contextmenu="openGroupMenu($event, child)"
              @dragenter.stop="onGroupDragEnter($event, child.path)"
              @dragover.stop="onGroupDragOver($event, child.path)"
              @dragleave.stop="onGroupDragLeave($event, child.path)"
              @drop.stop="onGroupDrop($event, child.path)"
            >
              <span class="nav-icon" v-html="ICONS.folder"></span>
              <span class="nav-label">{{ child.name }}</span>
              <span class="nav-count">{{ groupSubtreeCounts.get(child.path) ?? 0 }}</span>
            </button>
          </div>
        </div>
        <p v-if="groupKeyword && !sidebarTopGroups.length" class="nav-empty">{{ t("nav.no-group-results") }}</p>
        <p v-else-if="!sidebarTopGroups.length" class="nav-empty">{{ t("nav.no-groups") }}</p>
      </div>

      <!-- 未分组与「新建分组」固定在分组列表下方，不随列表滚动 -->
      <div class="nav-groups-foot">
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
      </div>

      <div class="sidebar-foot">
        <LocaleSwitch />
        <button type="button" class="logout-btn" @click="logout">
          <span class="logout-icon" v-html="ICONS.logout"></span>
          {{ t("action.logout") }}
        </button>
      </div>
    </aside>

    <!-- 两栏之间的 gridgrip：只覆盖边界几像素，按住左右拖动调侧栏宽度 -->
    <div
      class="sidebar-grip"
      :class="{ 'is-active': resizingSidebar }"
      role="separator"
      aria-orientation="vertical"
      :aria-label="t('nav.resize-sidebar')"
      :title="t('nav.resize-sidebar')"
      @pointerdown="startSidebarResize"
    ></div>

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
            <span v-if="uploadTargetName" class="page-sub-target">
              · {{ t("group.upload-target", { name: uploadTargetName }) }}
            </span>
          </p>
        </div>
        <div class="page-actions">
          <!-- 只看单个分组时提供「全部下载」；「最近 / 收藏 / 未分组」没有稳定的集合边界，不提供。 -->
          <button
            v-if="isGroupView && pageCount > 0"
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

      <!-- 三级分组筛选：只做内容区的分段切换，不进导航、不产生路由 -->
      <div
        v-if="isGroupedView"
        class="group-filters"
        role="tablist"
        :aria-label="t('nav.groups')"
      >
        <button
          type="button"
          class="filter-chip"
          role="tab"
          :class="{ on: effectiveSectionFilter === 'all' }"
          :aria-selected="effectiveSectionFilter === 'all'"
          @click="selectSectionFilter('all')"
        >
          {{ t("group.all") }}
          <span class="filter-count">{{ currentGroupFiles.length }}</span>
        </button>
        <button
          v-if="directSection"
          type="button"
          class="filter-chip"
          role="tab"
          :class="{ on: effectiveSectionFilter === 'direct' }"
          :aria-selected="effectiveSectionFilter === 'direct'"
          @click="selectSectionFilter('direct')"
        >
          {{ t("group.direct-files") }}
          <span class="filter-count">{{ directSection.files.length }}</span>
        </button>
        <button
          v-for="child in currentSubgroups"
          :key="child.path"
          type="button"
          class="filter-chip"
          role="tab"
          :class="{ on: effectiveSectionFilter === child.path, 'is-drop-target': groupDropTarget === child.path }"
          :aria-selected="effectiveSectionFilter === child.path"
          @click="selectSectionFilter(child.path)"
          @contextmenu="openGroupMenu($event, child)"
          @dragenter.stop="onGroupDragEnter($event, child.path)"
          @dragover.stop="onGroupDragOver($event, child.path)"
          @dragleave.stop="onGroupDragLeave($event, child.path)"
          @drop.stop="onGroupDrop($event, child.path)"
        >
          {{ child.name }}
          <span class="filter-count">{{ groupSubtreeCounts.get(child.path) ?? 0 }}</span>
        </button>
        <button
          type="button"
          class="filter-chip filter-new"
          :title="t('action.create-subgroup')"
          :aria-label="t('action.create-subgroup')"
          @click="onCreateChildGroup"
        >
          <span class="filter-plus" v-html="ICONS.plus"></span>
        </button>
      </div>

      <section class="content">
        <div v-if="loadError || actionError" class="banners">
          <p v-if="loadError" class="banner" role="alert">{{ loadError }}</p>
          <p v-if="actionError" class="banner" role="alert">{{ actionError }}</p>
        </div>

        <!-- 有子分组的分组视图：内容区按子分组分段（二级视图里就是三级分组） -->
        <div
          v-if="isGroupedView && (pageFiles.length || !keyword.trim())"
          :ref="setSectionOrderContainer"
          class="group-sections"
        >
          <section
            v-for="section in visibleGroupSections"
            :key="section.key"
            class="group-section"
            :data-group-name="section.group?.name"
            :data-section-fixed="section.group ? undefined : ''"
          >
            <div
              class="section-head"
              :class="{ 'is-drop-target': groupDropTarget === sectionDropPath(section) }"
              @click="toggleSectionFold(section.key)"
              @contextmenu="openSectionMenu($event, section)"
              @dragenter.stop="onGroupDragEnter($event, sectionDropPath(section))"
              @dragover.stop="onGroupDragOver($event, sectionDropPath(section))"
              @dragleave.stop="onGroupDragLeave($event, sectionDropPath(section))"
              @drop.stop="onGroupDrop($event, sectionDropPath(section))"
            >
              <span
                class="section-fold"
                :class="{ folded: isSectionFolded(section.key) }"
                v-html="ICONS.chevron"
              ></span>
              <span
                class="section-icon"
                :class="{ muted: !section.group }"
                v-html="section.group ? ICONS.folder : ICONS.inbox"
              ></span>
              <span class="section-name">{{ section.title }}</span>
              <span class="section-count">{{ section.files.length }}</span>
              <span class="section-actions" @click.stop>
                <button
                  v-if="section.files.length"
                  type="button"
                  class="section-action"
                  :title="t('action.download-group')"
                  :aria-label="t('action.download-group')"
                  v-html="ICONS.download"
                  @click="onDownloadFiles(section.files)"
                ></button>
                <button
                  v-if="section.group"
                  type="button"
                  class="section-action"
                  :title="t('action.group-menu')"
                  :aria-label="t('action.group-menu')"
                  aria-haspopup="menu"
                  v-html="ICONS.more"
                  @click="openGroupMenuFromSection($event, section.group)"
                ></button>
              </span>
            </div>
            <div v-if="!isSectionFolded(section.key)" class="section-body">
              <FileList
                v-if="section.files.length"
                :files="section.files"
                :groups="groups"
                :sortable="sectionSortable"
                :show-group="sectionShowGroup"
                @favorite="onFavorite"
                @download="onDownload"
                @rename="onRename"
                @error="showActionError"
                @delete="onDelete"
                @move-group="onMoveGroup"
                @new-group="onNewGroupForFile"
                @reorder="onReorder"
                @drag-start="startFileDrag"
                @drag-end="endFileDrag"
              />
              <p v-else class="section-empty">{{ t("group.empty-child") }}</p>
            </div>
          </section>
        </div>

        <!-- 最近视图按上传时间倒序、按本地日期分段；其余视图按 sort_order 平铺成一张卡片 -->
        <div v-else-if="!isGroupedView && visibleFiles.length" class="file-sections">
          <section v-for="section in visibleSections" :key="section.key" class="file-section">
            <h2 v-if="section.title" class="file-section-title">{{ section.title }}</h2>
            <FileList
              :files="section.files"
              :groups="groups"
              :sortable="canSort"
              :show-group="isRecent"
              @favorite="onFavorite"
              @download="onDownload"
              @rename="onRename"
              @error="showActionError"
              @delete="onDelete"
              @move-group="onMoveGroup"
              @new-group="onNewGroupForFile"
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
            <template v-else-if="isGroupView">
              <p class="empty-text">{{ t("upload.group-hint") }}</p>
              <p class="empty-text desktop-only">{{ t("upload.hint") }}</p>
            </template>
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
        <button
          v-if="canCreateSubgroup(groupMenu.path)"
          type="button"
          class="row-menu-item"
          role="menuitem"
          @click="onCreateSubgroup"
        >
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

    <UploadDock ref="uploadDock" :target-group="currentUploadGroup" @uploaded="refresh" />
  </div>
</template>

<style scoped>
/* 同一位置按断点切换的两套文案。 */
.mobile-only {
  display: none;
}

/* ================= 应用外壳 ================= */
.shell {
  --sidebar-width: 264px; /* 桌面端拖动 gridgrip 时由行内 style 覆盖 */
  position: relative;
  display: flex;
  height: 100vh;
  height: 100dvh;
  overflow: hidden;
}

/* 拖动分栏时整页禁用选中，避免把列表文字一起抹蓝 */
.shell.is-resizing {
  cursor: col-resize;
  user-select: none;
}

/* ================= 侧栏（桌面） ================= */
.sidebar {
  display: flex;
  flex: 0 0 var(--sidebar-width);
  flex-direction: column;
  gap: 12px;
  width: var(--sidebar-width);
  padding: 18px 16px 14px;
  border-right: 1px solid var(--line);
  background: rgb(255 255 255 / 0.72);
  backdrop-filter: blur(10px);
}

/* 两栏边界的 gridgrip：绝对定位，不占布局宽度；只压住边界两侧几像素 */
.sidebar-grip {
  position: absolute;
  top: 0;
  bottom: 0;
  left: calc(var(--sidebar-width) - 2px);
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

.brand {
  display: flex;
  align-items: center;
  gap: 11px;
  min-width: 0;
}

.brand-text {
  min-width: 0;
}

.brand-name {
  overflow: hidden;
  color: var(--ink);
  font-size: 15px;
  font-weight: 700;
  letter-spacing: -0.01em;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.brand-sub {
  overflow: hidden;
  margin-top: 1px;
  color: var(--ink-3);
  font-size: 10.5px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.upload-primary {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 11px 14px;
  border-radius: 12px;
  background: linear-gradient(135deg, var(--accent), #7c6cf6);
  color: #fff;
  font-size: 13.5px;
  font-weight: 650;
  box-shadow: 0 12px 22px -12px rgb(91 91 224 / 0.9);
  transition:
    transform 0.15s,
    box-shadow 0.15s;
}

.upload-primary:hover {
  transform: translateY(-1px);
  box-shadow: 0 16px 26px -12px rgb(91 91 224 / 0.95);
}

.upload-primary-icon {
  display: inline-flex;
}

.upload-primary-icon :deep(svg) {
  width: 17px;
  height: 17px;
}

.nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-top: 2px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  /* 右侧多留 19px：一级分组行在那放折叠箭头，其余行留白，保证徽标对齐一竖列 */
  padding: 8px 30px 8px 10px;
  border-radius: 10px;
  color: var(--ink-2);
  font-size: 13px;
  font-weight: 550;
  text-align: left;
  transition:
    background-color 0.15s,
    color 0.15s;
}

.nav-item:hover {
  background: var(--surface-2);
}

.nav-item.active {
  background: var(--accent-soft);
  color: var(--accent-strong);
  font-weight: 650;
}

.nav-icon {
  display: inline-flex;
  color: inherit;
  opacity: 0.8;
}

.nav-icon :deep(svg) {
  width: 16px;
  height: 16px;
}

.nav-label {
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.nav-count {
  flex: 0 0 auto;
  padding: 1px 7px;
  border-radius: 999px;
  background: rgb(20 23 44 / 0.05);
  color: var(--ink-3);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
}

.nav-item.active .nav-count {
  background: rgb(91 91 224 / 0.14);
  color: var(--accent-strong);
}

/* 文件拖到分组标签上时的落点高亮：虚线画在元素内侧，避免被滚动容器裁掉。 */
.nav-item.is-drop-target {
  outline: 2px dashed var(--accent);
  outline-offset: -3px;
  background: var(--accent-soft);
  color: var(--accent-strong);
}

.nav-item.is-drop-target .nav-count {
  background: rgb(91 91 224 / 0.18);
  color: var(--accent-strong);
}

/* 顶级分组把落点高亮画在它自己那一行上（wrapper 还包着子分组列表） */
.nav-group-block.is-drop-target > .nav-group-row > .nav-item-group {
  outline: 2px dashed var(--accent);
  outline-offset: -3px;
  background: var(--accent-soft);
  color: var(--accent-strong);
}

/* 分组区标题行：标题 + 过滤框并排，分组列表拿剩余的全部高度 */
.nav-groups-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
  padding-top: 10px;
  border-top: 1px solid var(--line);
}

.nav-section-title {
  flex: 0 0 auto;
  color: var(--ink-3);
  font-size: 10.5px;
  font-weight: 650;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

/* 分组过滤框：与「分组」标题同行，占满标题右侧的剩余宽度 */
.group-search {
  display: flex;
  flex: 1 1 auto;
  align-items: center;
  gap: 6px;
  min-width: 0;
  padding: 4px 8px;
  border: 1px solid var(--line);
  border-radius: 9px;
  background: var(--surface);
  color: var(--ink-3);
  transition:
    border-color 0.15s,
    box-shadow 0.15s;
}

.group-search:focus-within {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px rgb(91 91 224 / 0.12);
}

.group-search-icon {
  display: inline-flex;
  flex: 0 0 auto;
}

.group-search-icon :deep(svg) {
  width: 13px;
  height: 13px;
}

.group-search-input {
  width: 100%;
  min-width: 0;
  border: 0;
  outline: none;
  background: transparent;
  color: var(--ink);
  font-size: 12px;
}

.group-search-input::placeholder {
  color: var(--ink-3);
}

.nav-groups {
  /* 侧栏里唯一可伸缩的块：窗口越高、分组列表越长，拿到的空间越多 */
  flex: 1 1 auto;
  min-height: 0;
  /* 滚动条挪到侧栏的内边距里：负 margin 把它推到分组行右侧留白处，
     padding 再把内容拉回原位，滚动条就不会压住分组行 / 徽标 */
  margin-right: -12px;
  padding-right: 12px;
  overflow-y: auto;
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: rgb(20 23 44 / 0.16) transparent;
}

/* 细滚动条：轨道透明，拇指内缩成圆角胶囊 */
.nav-groups::-webkit-scrollbar {
  width: 8px;
}

.nav-groups::-webkit-scrollbar-track {
  background: transparent;
}

.nav-groups::-webkit-scrollbar-thumb {
  border: 2px solid transparent;
  border-radius: 999px;
  background-clip: content-box;
  background-color: rgb(20 23 44 / 0.16);
}

.nav-groups::-webkit-scrollbar-thumb:hover {
  background-color: rgb(20 23 44 / 0.28);
}

.nav-item-group .nav-icon {
  opacity: 0.7;
}

/* 顶级分组的一项：自己的一行 + 它的子分组列表，整体参与顶层拖拽 */
.nav-group-block {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

/* 顶级分组的一行：分组按钮 + 右侧折叠箭头（箭头压在按钮右侧留白上） */
.nav-group-row {
  position: relative;
  display: flex;
  flex-direction: column;
}

.nav-fold {
  position: absolute;
  top: 50%;
  right: 4px;
  display: grid;
  place-items: center;
  width: 20px;
  height: 20px;
  border-radius: 6px;
  color: var(--ink-3);
  transform: translateY(-50%);
  transition:
    background-color 0.15s,
    color 0.15s;
}

.nav-fold:hover {
  background: rgb(20 23 44 / 0.07);
  color: var(--ink-2);
}

.nav-fold-icon {
  display: inline-flex;
  transition: transform 0.15s;
}

.nav-fold-icon :deep(svg) {
  width: 13px;
  height: 13px;
}

/* 折叠时箭头从「朝下（展开）」转到「朝右（收起）」 */
.nav-fold.is-folded .nav-fold-icon {
  transform: rotate(-90deg);
}

/* 二级分组缩进一层，只在本层内拖拽排序 */
.nav-subgroups {
  margin-top: 0;
  padding-left: 14px;
}

.nav-item-subgroup {
  padding: 6px 30px 6px 10px;
  font-size: 12.5px;
  font-weight: 500;
}

.nav-item-subgroup .nav-icon {
  opacity: 0.55;
}

.nav-item-subgroup .nav-icon :deep(svg) {
  width: 14px;
  height: 14px;
}

/* 未分组 / 新建分组：固定在分组列表下方，不参与列表滚动 */
.nav-groups-foot {
  display: flex;
  flex: 0 0 auto;
  flex-direction: column;
  gap: 2px;
}

.nav-ungrouped,
.nav-new {
  flex: 0 0 auto;
}

.nav-new {
  margin-top: 0;
}

.nav-item-new {
  color: var(--ink-3);
}

.nav-item-new:hover {
  color: var(--accent-strong);
}

/* 分组拖拽：ghost 半透明，chosen 高亮。分组行不设 grab 光标，沿用按钮的默认指针。 */
.group-drag-ghost {
  opacity: 0.35;
}

/* 拖拽中的分组高亮画在圆角的行 / chip 自身，不要给包装层铺方块底色。
   顶级分组拖拽的是包装层，二级分组拖拽的元素本身就是按钮，两种都要覆盖。 */
.nav-group-block.group-drag-chosen > .nav-group-row > .nav-item,
.nav-item-subgroup.group-drag-chosen {
  background: var(--accent-soft);
  color: var(--accent-strong);
}

.chip-block.group-drag-chosen .chip,
.chip-subgroup.group-drag-chosen {
  border-color: var(--accent);
  background: var(--accent-soft);
  color: var(--accent-strong);
}

.nav-empty {
  padding: 4px 11px;
  color: var(--ink-3);
  font-size: 11.5px;
}

.sidebar-foot {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  margin-top: auto;
  padding-top: 10px;
  border-top: 1px solid var(--line);
  white-space: nowrap;
}

/* 侧栏拖到最窄（200px）时这一行也不能折行：语言切换与退出按钮各自不收缩 */
.sidebar-foot :deep(.locale-switch) {
  flex: 0 0 auto;
}

.sidebar-foot :deep(.locale-btn) {
  padding: 2px 7px;
  white-space: nowrap;
}

.logout-btn {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 6px;
  padding: 5px 6px;
  border-radius: 9px;
  color: var(--ink-3);
  font-size: 11.5px;
  font-weight: 550;
  white-space: nowrap;
}

.logout-btn:hover {
  background: var(--rose-soft);
  color: var(--rose);
}

.logout-icon {
  display: inline-flex;
}

.logout-icon :deep(svg) {
  width: 14px;
  height: 14px;
}

/* ================= 主区域 ================= */
.main {
  position: relative;
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

.main.is-drop-target {
  cursor: copy;
}

.mobile-bar {
  display: none;
}

.page-head {
  display: flex;
  flex: 0 0 auto;
  align-items: flex-end;
  justify-content: space-between;
  gap: 20px;
  padding: 28px 34px 0;
}

.page-title {
  color: var(--ink);
  font-size: 26px;
  font-weight: 750;
  letter-spacing: -0.025em;
  line-height: 1.2;
}

.page-sub {
  margin-top: 4px;
  color: var(--ink-3);
  font-size: 12px;
}

.page-title-line {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  /* 分组视图会在标题旁多出 30px 高的「⋯」按钮：给标题行固定最小高度，
     避免切换视图时标题行变高，把下面的标签页 / chips / 列表整体顶下去。 */
  min-height: 30px;
}

/* 单分组视图标题旁的重命名入口 */
.title-action {
  display: grid;
  flex: 0 0 auto;
  place-items: center;
  width: 30px;
  height: 30px;
  border-radius: 9px;
  color: var(--ink-3);
  transition:
    color 0.15s,
    background-color 0.15s;
}

.title-action:hover,
.title-action.on {
  background: var(--surface-2);
  color: var(--accent-strong);
}

.title-action :deep(svg) {
  width: 16px;
  height: 16px;
}

.page-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 10px;
}

/* 单分组视图的整包下载入口：和搜索框同高 */
.group-download {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 9px 13px;
  border: 1px solid var(--line);
  border-radius: 11px;
  background: var(--surface);
  box-shadow: var(--shadow-sm);
  color: var(--ink-2);
  font-size: 12.5px;
  font-weight: 600;
  white-space: nowrap;
  transition:
    border-color 0.15s,
    color 0.15s;
}

.group-download:hover {
  border-color: var(--accent);
  color: var(--accent-strong);
}

.group-download-icon {
  display: inline-flex;
}

.group-download-icon :deep(svg) {
  width: 16px;
  height: 16px;
}

/* 手动刷新：上传自动刷新之外的独立入口，刷新中图标旋转 */
.refresh-btn {
  display: grid;
  flex: 0 0 auto;
  place-items: center;
  width: 38px;
  height: 38px;
  border: 1px solid var(--line);
  border-radius: 11px;
  background: var(--surface);
  box-shadow: var(--shadow-sm);
  color: var(--ink-3);
  transition:
    border-color 0.15s,
    color 0.15s;
}

.refresh-btn:hover {
  border-color: var(--accent);
  color: var(--accent-strong);
}

.refresh-btn:disabled {
  cursor: default;
  opacity: 0.6;
}

.refresh-btn :deep(svg) {
  width: 16px;
  height: 16px;
}

.refresh-btn.spinning :deep(svg) {
  animation: spin 0.9s linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.search {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 300px;
  padding: 9px 12px;
  border: 1px solid var(--line);
  border-radius: 11px;
  background: var(--surface);
  box-shadow: var(--shadow-sm);
  color: var(--ink-3);
  transition:
    border-color 0.15s,
    box-shadow 0.15s;
}

.search:focus-within {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px rgb(91 91 224 / 0.12);
}

.search-icon {
  display: inline-flex;
  flex: 0 0 auto;
}

.search-icon :deep(svg) {
  width: 15px;
  height: 15px;
}

.search-input {
  width: 100%;
  min-width: 0;
  border: 0;
  outline: none;
  background: transparent;
  color: var(--ink);
  font-size: 13px;
}

.search-input::placeholder {
  color: var(--ink-3);
}

.tabs {
  display: none;
}

/* 窄屏分组 chips：桌面端由侧栏承担分组导航，这里隐藏 */
.chips {
  display: none;
}

.content {
  flex: 1 1 auto;
  min-height: 0;
  padding: 20px 34px 34px;
  overflow-y: auto;
  /* 常驻滚动条槽位：切换视图时列表宽度不跳，滚动条也落在内边距里不压卡片 */
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: rgb(20 23 44 / 0.16) transparent;
}

.content::-webkit-scrollbar {
  width: 12px;
}

.content::-webkit-scrollbar-track {
  background: transparent;
}

.content::-webkit-scrollbar-thumb {
  border: 4px solid transparent;
  border-radius: 999px;
  background-clip: content-box;
  background-color: rgb(20 23 44 / 0.16);
}

.content::-webkit-scrollbar-thumb:hover {
  background-color: rgb(20 23 44 / 0.28);
}

.banners {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 14px;
}

.banner {
  padding: 10px 14px;
  border: 1px solid #ffd0da;
  border-radius: 11px;
  background: var(--rose-soft);
  color: #b3123a;
  font-size: 12.5px;
}

/* 标题下补一句上传目标：选中三级分组段时上传落到它，免得传错组 */
.page-sub-target {
  color: var(--ink-2);
}

/* ================= 三级分组筛选（内容区） ================= */
/* 三级分组只是分组：这条筛选只切换内容区的分段，不进地址栏、也不进侧栏 */
.group-filters {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 6px;
  padding: 14px 34px 0;
  overflow-x: auto;
  scrollbar-width: none;
}

.group-filters::-webkit-scrollbar {
  display: none;
}

.filter-chip {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border: 1px solid var(--line);
  border-radius: 999px;
  background: var(--surface);
  box-shadow: var(--shadow-sm);
  color: var(--ink-2);
  font-size: 12px;
  font-weight: 600;
  white-space: nowrap;
  transition:
    border-color 0.15s,
    background-color 0.15s,
    color 0.15s;
}

.filter-chip:hover {
  border-color: var(--accent);
  color: var(--accent-strong);
}

.filter-chip.on {
  border-color: var(--accent);
  background: var(--accent-soft);
  color: var(--accent-strong);
}

.filter-count {
  color: var(--ink-3);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
}

.filter-chip.on .filter-count {
  color: var(--accent-strong);
}

/* 文件拖到三级分组胶囊上 = 移动到该分组 */
.filter-chip.is-drop-target {
  outline: 2px dashed var(--accent);
  outline-offset: -3px;
  background: var(--accent-soft);
}

.filter-chip.filter-new {
  padding: 6px 9px;
  border-style: dashed;
  color: var(--ink-3);
}

.filter-chip.filter-new:hover {
  border-color: var(--accent);
  color: var(--accent-strong);
}

.filter-plus {
  display: inline-flex;
}

.filter-plus :deep(svg) {
  width: 13px;
  height: 13px;
}

/* ================= 内容区：按三级分组分段 ================= */
.group-sections {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.group-section {
  overflow: hidden;
  border: 1px solid var(--line);
  border-radius: 18px;
  background: var(--surface);
  box-shadow: var(--shadow-md);
}

.section-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  background: linear-gradient(180deg, #fbfbff, #f7f8fc);
  cursor: pointer;
  user-select: none;
}

.section-head:hover {
  background: #f4f5fd;
}

/* 正在拖动的段：段头高亮，和侧栏分组拖拽保持同一套视觉 */
.group-section.group-drag-chosen .section-head {
  background: var(--accent-soft);
}

/* 文件拖到段头上 = 移动到这一段的分组 */
.section-head.is-drop-target {
  outline: 2px dashed var(--accent);
  outline-offset: -3px;
  background: var(--accent-soft);
}

/* ICONS.chevron 本来就是朝下的：展开不转，折叠时转到朝右 */
.section-fold {
  display: grid;
  flex: 0 0 auto;
  place-items: center;
  width: 18px;
  height: 18px;
  color: var(--ink-3);
  transition: transform 0.15s;
}

.section-fold.folded {
  transform: rotate(-90deg);
}

.section-fold :deep(svg) {
  width: 14px;
  height: 14px;
}

.section-icon {
  display: inline-flex;
  flex: 0 0 auto;
  color: var(--ink-3);
}

.section-icon.muted {
  opacity: 0.75;
}

.section-icon :deep(svg) {
  width: 16px;
  height: 16px;
}

.section-name {
  overflow: hidden;
  color: var(--ink);
  font-size: 13px;
  font-weight: 650;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.section-count {
  flex: 0 0 auto;
  padding: 1px 8px;
  border-radius: 999px;
  background: rgb(20 23 44 / 0.05);
  color: var(--ink-3);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
}

.section-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 2px;
  margin-left: auto;
}

.section-action {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  border-radius: 8px;
  color: var(--ink-3);
  transition:
    background-color 0.15s,
    color 0.15s;
}

.section-action:hover {
  background: rgb(22 25 38 / 0.06);
  color: var(--ink);
}

.section-action :deep(svg) {
  width: 15px;
  height: 15px;
}

/* 段内沿用 FileList 的卡片，但段头已经是一层卡片边：内层不再重复边框与阴影 */
.section-body {
  padding: 6px 10px 10px;
}

.section-body :deep(.list-card) {
  border: 0;
  border-radius: 12px;
  box-shadow: none;
}

/* 段拖拽的 fallback 克隆体：只留段头，很长的段也不会拖出一堵墙。
   克隆挂在 body 上但复制了 scope 属性，所以这条 scoped 选择器仍然命中。 */
.sortable-fallback .section-body {
  display: none;
}

.section-empty {
  padding: 8px 10px 10px;
  color: var(--ink-3);
  font-size: 12px;
}

/* 「最近」视图按日期分段：每段一个标题 + 一张卡片，段与段之间留出呼吸空间 */
.file-sections {
  display: flex;
  flex-direction: column;
  gap: 22px;
}

.file-section-title {
  margin: 0 4px 8px;
  color: var(--ink-3);
  font-size: 12px;
  font-weight: 650;
  letter-spacing: 0.02em;
}

/* 分组条目的右键菜单：和行内菜单同一套外观，靠点击点定位 */
.group-menu-pop {
  z-index: 120;
}

/* ================= 空状态 ================= */
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  min-height: 360px;
  padding: 48px 24px;
  text-align: center;
}

.empty-icon {
  display: grid;
  place-items: center;
  width: 62px;
  height: 62px;
  margin-bottom: 8px;
  border: 1px solid var(--line);
  border-radius: 20px;
  background: var(--surface);
  box-shadow: var(--shadow-sm);
  color: var(--accent);
}

.empty-icon :deep(svg) {
  width: 26px;
  height: 26px;
}

.empty-title {
  color: var(--ink);
  font-size: 14.5px;
  font-weight: 620;
}

.empty-text {
  max-width: 340px;
  color: var(--ink-3);
  font-size: 12.5px;
}

.upload-secondary {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  margin-top: 12px;
  padding: 9px 16px;
  border-radius: 11px;
  background: var(--accent-soft);
  color: var(--accent-strong);
  font-size: 12.5px;
  font-weight: 650;
}

.upload-secondary:hover {
  background: #e2e0ff;
}

.upload-secondary-icon {
  display: inline-flex;
}

.upload-secondary-icon :deep(svg) {
  width: 15px;
  height: 15px;
}

/* ================= 拖放覆盖层 ================= */
.drop-overlay {
  position: absolute;
  inset: 14px;
  z-index: 30;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
  border: 2px dashed var(--accent);
  border-radius: 22px;
  background: rgb(91 91 224 / 0.08);
  color: var(--accent-strong);
  pointer-events: none;
}

.drop-overlay-icon :deep(svg) {
  width: 54px;
  height: 54px;
}

.drop-overlay-text {
  font-size: 15px;
  font-weight: 650;
}

/* ================= 窄屏 ================= */
@media (max-width: 900px) {
  .desktop-only {
    display: none;
  }

  .mobile-only {
    display: inline;
  }

  .shell {
    flex-direction: column;
  }

  .sidebar {
    display: none;
  }

  /* 窄屏没有两栏布局，边界 gridgrip 一并隐藏 */
  .sidebar-grip {
    display: none;
  }

  .mobile-bar {
    display: flex;
    flex: 0 0 auto;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: calc(12px + env(safe-area-inset-top, 0px)) 16px 10px;
    border-bottom: 1px solid var(--line);
    background: rgb(255 255 255 / 0.8);
    backdrop-filter: blur(10px);
  }

  .brand-mobile {
    gap: 9px;
  }

  .brand-mobile .brand-mark {
    width: 32px;
    height: 32px;
    border-radius: 10px;
  }

  .brand-mobile .brand-mark :deep(svg) {
    width: 16px;
    height: 16px;
  }

  .brand-mobile .brand-name {
    font-size: 15px;
  }

  .mobile-actions {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .mobile-upload {
    display: grid;
    place-items: center;
    width: 36px;
    height: 36px;
    border-radius: 11px;
    background: linear-gradient(135deg, var(--accent), #7c6cf6);
    color: #fff;
    box-shadow: 0 10px 18px -10px rgb(91 91 224 / 0.9);
  }

  .mobile-upload :deep(svg) {
    width: 17px;
    height: 17px;
  }

  .mobile-logout {
    display: grid;
    place-items: center;
    width: 34px;
    height: 34px;
    border-radius: 10px;
    color: var(--ink-3);
  }

  .mobile-logout:hover {
    background: var(--surface-2);
  }

  .mobile-logout :deep(svg) {
    width: 16px;
    height: 16px;
  }

  .page-head {
    flex-direction: column;
    align-items: stretch;
    gap: 12px;
    padding: 14px 16px 0;
  }

  .page-title {
    font-size: 22px;
  }

  .page-actions {
    gap: 8px;
  }

  .page-actions .search {
    flex: 1 1 auto;
    width: auto;
    min-width: 0;
  }

  .tabs {
    display: flex;
    flex: 0 0 auto;
    gap: 6px;
    padding: 12px 16px 0;
  }

  .tab {
    display: flex;
    flex: 1 1 0;
    align-items: center;
    justify-content: center;
    gap: 6px;
    padding: 8px 10px;
    border: 1px solid var(--line);
    border-radius: 10px;
    background: var(--surface);
    color: var(--ink-2);
    font-size: 12.5px;
    font-weight: 600;
  }

  .tab.active {
    border-color: var(--accent);
    background: var(--accent);
    color: #fff;
  }

  .tab-count {
    padding: 0 6px;
    border-radius: 999px;
    background: rgb(20 23 44 / 0.06);
    color: inherit;
    font-size: 10.5px;
    font-variant-numeric: tabular-nums;
  }

  .tab.active .tab-count {
    background: rgb(255 255 255 / 0.22);
  }

  /* 标签页之下的分组 chips：分组横向滚动、可拖拽排序；未分组固定在右侧 */
  .chips {
    display: flex;
    flex: 0 0 auto;
    gap: 6px;
    padding: 10px 16px 0;
    overflow: hidden;
  }

  .chips-groups {
    display: flex;
    flex: 0 1 auto;
    gap: 6px;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }

  .chips-groups::-webkit-scrollbar {
    display: none;
  }

  .chip-ungrouped {
    flex: 0 0 auto;
  }

  /* chips 也按层级分块：顶级 chip 单独一层，第二行只放当前顶级分组的子分组 */
  .chip-block {
    display: flex;
    flex: 0 0 auto;
    align-items: center;
  }

  .chips-subgroups {
    padding-top: 6px;
  }

  .chip-subgroup {
    border-style: dashed;
    color: var(--ink-3);
    font-weight: 550;
  }

  .chip-subgroup.active {
    border-style: solid;
    color: var(--accent-strong);
  }

  /* 新建分组：虚线边框的窄按钮，排在分组 chips 之后 */
  .chip-new {
    flex: 0 0 auto;
    padding: 6px 9px;
    border-style: dashed;
    color: var(--ink-3);
  }

  .chip-new:hover {
    border-color: var(--accent);
    color: var(--accent-strong);
  }

  .chip {
    display: inline-flex;
    flex: 0 0 auto;
    align-items: center;
    gap: 5px;
    padding: 6px 11px;
    border: 1px solid var(--line);
    border-radius: 999px;
    background: var(--surface);
    color: var(--ink-2);
    font-size: 12px;
    font-weight: 600;
    white-space: nowrap;
  }

  .chip-icon {
    display: inline-flex;
    color: var(--ink-3);
  }

  .chip-icon :deep(svg) {
    display: block;
    width: 13px;
    height: 13px;
  }

  .chip.active {
    border-color: var(--accent);
    background: var(--accent-soft);
    color: var(--accent-strong);
  }

  .chip.active .chip-icon {
    color: var(--accent-strong);
  }

  .chip.is-drop-target {
    outline: 2px dashed var(--accent);
    outline-offset: -3px;
    background: var(--accent-soft);
    color: var(--accent-strong);
  }

  .chip-block.is-drop-target .chip {
    outline: 2px dashed var(--accent);
    outline-offset: -3px;
    background: var(--accent-soft);
    color: var(--accent-strong);
  }

  .content {
    padding: 12px 12px calc(24px + env(safe-area-inset-bottom, 0px));
  }

  .group-filters {
    padding: 10px 16px 0;
  }

  .group-section {
    border-radius: 16px;
  }

  .section-head {
    padding: 9px 10px;
  }

  .empty {
    min-height: 300px;
    padding: 32px 16px;
  }

  .drop-overlay {
    inset: 10px;
    border-radius: 18px;
  }
}

@media (max-width: 560px) {
  .mobile-bar .brand-sub {
    display: none;
  }

  .page-title {
    font-size: 20px;
  }
}
</style>
