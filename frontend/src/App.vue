<script setup lang="ts">
/**
 * 主页面（/）：左侧收藏 + 全部文件，右侧上传队列与拖拽区。
 *
 * 列表数据只在这里维护；每行的操作成功后重新拉一次列表，保持与服务端一致
 * （收藏 / 公开状态是全局的，上传完成后也可能出现新记录）。
 */
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

import FileRow from "@/components/FileRow.vue";
import LocaleSwitch from "@/components/LocaleSwitch.vue";
import UploadPanel from "@/components/UploadPanel.vue";
import {
  ApiError,
  deleteFile,
  downloadFile,
  listFiles,
  toggleFavorite,
  togglePublic,
} from "@/api";
import { ICONS } from "@/icons";
import { t } from "@/i18n";
import { toLogin } from "@/session";
import type { FileItem } from "@/types";

const files = ref<FileItem[]>([]);
const loaded = ref(false);
const loadError = ref("");
const actionError = ref("");
let actionErrorTimer = 0;

const favorites = computed(() => files.value.filter((file) => file.is_favorite));
const others = computed(() => files.value.filter((file) => !file.is_favorite));

/* ---------- 侧栏宽度分隔条 ---------- */

/** 宽度占比存 localStorage；拖拽 / 左右方向键调整，双击恢复默认。 */
const SIDEBAR_RATIO_KEY = "file-share.sidebar-ratio";
const DEFAULT_SIDEBAR_RATIO = 1 / 3;
const MIN_SIDEBAR_PX = 280;
const MIN_WORKSPACE_PX = 360;
const SPLITTER_PX = 10;
const KEYBOARD_STEP_PX = 16;
/** 与 CSS 的窄屏断点（max-width: 900px）保持一致：窄屏上下堆叠，不做左右分栏。 */
const WIDE_LAYOUT_MIN_PX = 901;

function readSidebarRatio(): number {
  try {
    const raw = Number(localStorage.getItem(SIDEBAR_RATIO_KEY));
    if (Number.isFinite(raw) && raw > 0) {
      return Math.min(Math.max(raw, 0.12), 0.85);
    }
  } catch {
    /* 隐私模式读不到 localStorage：用默认占比 */
  }
  return DEFAULT_SIDEBAR_RATIO;
}

function persistSidebarRatio(): void {
  try {
    localStorage.setItem(SIDEBAR_RATIO_KEY, ratio.value.toFixed(4));
  } catch {
    /* 存不下只影响下次打开，不影响本次调整 */
  }
}

const appEl = ref<HTMLElement | null>(null);
const appWidth = ref(0);
const ratio = ref(readSidebarRatio());
const splitting = ref(false);
let dragPointerId: number | null = null;
let dragStartX = 0;
let dragStartWidth = 0;
let resizeObserver: ResizeObserver | undefined;

const maxSidebarWidth = computed(() =>
  Math.max(MIN_SIDEBAR_PX, appWidth.value - MIN_WORKSPACE_PX - SPLITTER_PX),
);

function clampSidebarWidth(width: number): number {
  return Math.min(Math.max(width, MIN_SIDEBAR_PX), maxSidebarWidth.value);
}

const sidebarWidth = computed(() => clampSidebarWidth(ratio.value * appWidth.value));
// 窄屏宽度交给 CSS；宽屏用内联 px，拖拽时才能跟手。
const sidebarStyle = computed(() =>
  appWidth.value >= WIDE_LAYOUT_MIN_PX ? { width: `${sidebarWidth.value}px` } : undefined,
);
const sidebarPercent = computed(() =>
  appWidth.value > 0 ? Math.round((sidebarWidth.value / appWidth.value) * 100) : 33,
);
const minSidebarPercent = computed(() =>
  appWidth.value > 0 ? Math.ceil((MIN_SIDEBAR_PX / appWidth.value) * 100) : 0,
);
const maxSidebarPercent = computed(() =>
  appWidth.value > 0 ? Math.floor((maxSidebarWidth.value / appWidth.value) * 100) : 100,
);

function setSidebarWidth(width: number): void {
  ratio.value = clampSidebarWidth(width) / appWidth.value;
}

function onSplitterPointerDown(event: PointerEvent): void {
  if (event.button !== 0 || appWidth.value < WIDE_LAYOUT_MIN_PX) return;
  event.preventDefault();
  splitting.value = true;
  dragPointerId = event.pointerId;
  dragStartX = event.clientX;
  dragStartWidth = sidebarWidth.value;
  (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
}

function onSplitterPointerMove(event: PointerEvent): void {
  if (!splitting.value || event.pointerId !== dragPointerId) return;
  setSidebarWidth(dragStartWidth + (event.clientX - dragStartX));
}

function onSplitterPointerUp(event: PointerEvent): void {
  if (!splitting.value) return;
  splitting.value = false;
  dragPointerId = null;
  const target = event.currentTarget as HTMLElement;
  if (target.hasPointerCapture(event.pointerId)) target.releasePointerCapture(event.pointerId);
  persistSidebarRatio();
}

function onSplitterKeydown(event: KeyboardEvent): void {
  const step = event.shiftKey ? KEYBOARD_STEP_PX * 3 : KEYBOARD_STEP_PX;
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
  event.preventDefault();
  setSidebarWidth(sidebarWidth.value + (event.key === "ArrowLeft" ? -step : step));
  persistSidebarRatio();
}

function resetSidebarWidth(): void {
  ratio.value = DEFAULT_SIDEBAR_RATIO;
  persistSidebarRatio();
}


/* ---------- 右侧整页拖拽上传 ---------- */

/** UploadPanel 通过 defineExpose 暴露的入口：队列逻辑只有一份。 */
interface UploadPanelApi {
  addFiles(files: FileList | File[] | null | undefined): void;
}

const uploadPanel = ref<UploadPanelApi | null>(null);
const dropActive = ref(false);

/** 只接管「拖文件」：拖文本/链接时不弹覆盖层，也不改 dropEffect。 */
function draggingFiles(event: DragEvent): boolean {
  const types = event.dataTransfer?.types;
  if (!types) return false;
  for (const type of Array.from(types)) {
    if (type === "Files") return true;
  }
  return false;
}

function onWorkspaceDragEnter(event: DragEvent): void {
  if (!draggingFiles(event)) return;
  event.preventDefault();
  dropActive.value = true;
}

function onWorkspaceDragOver(event: DragEvent): void {
  if (!draggingFiles(event)) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
  // dragenter 在子元素之间移动时不一定可靠，dragover 持续兜底
  dropActive.value = true;
}

function onWorkspaceDragLeave(event: DragEvent): void {
  // 离开时不一定还能拿到 dataTransfer.types，所以这里不判断类型：覆盖层本来就是
  // 文件拖拽才显示的，任何一次真正离开右半屏都应该收起来。
  const current = event.currentTarget as Node;
  const next = event.relatedTarget as Node | null;
  if (next && current.contains(next)) return; // 还在右半屏里，不算离开
  dropActive.value = false;
}

function onWorkspaceDrop(event: DragEvent): void {
  dropActive.value = false;
  if (!draggingFiles(event)) return;
  event.preventDefault();
  const files = event.dataTransfer?.files;
  if (files && files.length > 0) uploadPanel.value?.addFiles(files);
}

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

/** 拉列表；401 由 api 客户端处理，其余错误在侧栏显示 error.load。 */
async function refresh(): Promise<void> {
  try {
    files.value = await listFiles();
    loadError.value = "";
  } catch (error) {
    if (isUnauthorized(error)) return;
    loadError.value = t("error.load");
  } finally {
    loaded.value = true;
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
    await downloadFile(file.id, file.filename);
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

function logout(): void {
  toLogin();
}

/** 拖到页面空白处时吞掉默认行为，免得浏览器直接打开文件、把页面顶掉。 */
function swallowDrop(event: DragEvent): void {
  event.preventDefault();
}

/** 量出可用宽度：分隔条按「侧栏 px / 应用 px」的占比工作，窗口变化时跟着重算。 */
function measureAppWidth(): void {
  appWidth.value = appEl.value?.clientWidth ?? window.innerWidth;
}

onMounted(() => {
  document.addEventListener("dragover", swallowDrop);
  document.addEventListener("drop", swallowDrop);
  measureAppWidth();
  resizeObserver = new ResizeObserver(measureAppWidth);
  if (appEl.value) resizeObserver.observe(appEl.value);
  window.addEventListener("resize", measureAppWidth);
  void refresh();
});

onBeforeUnmount(() => {
  document.removeEventListener("dragover", swallowDrop);
  document.removeEventListener("drop", swallowDrop);
  window.removeEventListener("resize", measureAppWidth);
  resizeObserver?.disconnect();
  window.clearTimeout(actionErrorTimer);
});
</script>

<template>
  <div ref="appEl" class="app" :class="{ 'is-splitting': splitting }">
    <!-- 窄屏（竖屏手机/平板）用顶部工具栏替代侧栏底部的语言与退出入口。 -->
    <header class="mobile-topbar">
      <span class="mobile-title">{{ t("app.title") }}</span>
      <div class="mobile-actions">
        <LocaleSwitch />
        <button type="button" class="logout-btn" @click="logout">
          <span class="logout-icon" v-html="ICONS.logout"></span>
          <span class="logout-label">{{ t("action.logout") }}</span>
        </button>
      </div>
    </header>

    <aside class="sidebar" :style="sidebarStyle">
      <div class="sidebar-scroll">
        <div v-if="loadError || actionError" class="banners">
          <p v-if="loadError" class="banner" role="alert">{{ t("error.load") }}</p>
          <p v-if="actionError" class="banner" role="alert">{{ actionError }}</p>
        </div>

        <section class="list-section" :aria-label="t('nav.favorites')">
          <h2 class="section-title">
            <span class="section-icon" v-html="ICONS.star"></span>
            {{ t("nav.favorites") }}
          </h2>
          <ul v-if="favorites.length" class="file-list">
            <FileRow
              v-for="file in favorites"
              :key="file.id"
              :file="file"
              @favorite="onFavorite(file)"
              @download="onDownload(file)"
              @visibility="onVisibility(file)"
              @delete="onDelete(file)"
            />
          </ul>
          <p v-else-if="loaded && !loadError" class="empty-note">{{ t("nav.empty-favorites") }}</p>
        </section>

        <div class="section-divider"></div>

        <section class="list-section" :aria-label="t('nav.all-files')">
          <h2 class="section-title">{{ t("nav.all-files") }}</h2>
          <ul v-if="others.length" class="file-list">
            <FileRow
              v-for="file in others"
              :key="file.id"
              :file="file"
              @favorite="onFavorite(file)"
              @download="onDownload(file)"
              @visibility="onVisibility(file)"
              @delete="onDelete(file)"
            />
          </ul>
          <p v-else-if="loaded && !loadError" class="empty-note">{{ t("nav.empty-files") }}</p>
        </section>
      </div>

      <footer class="sidebar-foot">
        <LocaleSwitch />
        <button type="button" class="logout-btn" @click="logout">
          <span class="logout-icon" v-html="ICONS.logout"></span>
          <span class="logout-label">{{ t("action.logout") }}</span>
        </button>
      </footer>
    </aside>

    <div
      class="panel-splitter"
      role="separator"
      aria-orientation="vertical"
      :aria-label="t('splitter.label')"
      :aria-valuemin="minSidebarPercent"
      :aria-valuemax="maxSidebarPercent"
      :aria-valuenow="sidebarPercent"
      :title="t('splitter.hint')"
      tabindex="0"
      @pointerdown="onSplitterPointerDown"
      @pointermove="onSplitterPointerMove"
      @pointerup="onSplitterPointerUp"
      @pointercancel="onSplitterPointerUp"
      @keydown="onSplitterKeydown"
      @dblclick="resetSidebarWidth"
    >
      <span class="splitter-grip" aria-hidden="true"><i></i><i></i><i></i></span>
    </div>

    <main
      class="workspace"
      :class="{ 'is-drop-target': dropActive }"
      @dragenter="onWorkspaceDragEnter"
      @dragover="onWorkspaceDragOver"
      @dragleave="onWorkspaceDragLeave"
      @drop="onWorkspaceDrop"
    >
      <UploadPanel ref="uploadPanel" @uploaded="refresh" />
      <div v-if="dropActive" class="drop-overlay" aria-hidden="true">
        <span class="drop-overlay-icon" v-html="ICONS.upload"></span>
        <p class="drop-overlay-text">{{ t("upload.drop-overlay") }}</p>
      </div>
    </main>
  </div>
</template>
