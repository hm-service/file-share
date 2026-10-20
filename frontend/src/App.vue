<script setup lang="ts">
/**
 * 主页面（/）的界面壳：左侧导航 + 文件列表 + 浮动上传坞。
 *
 * 列表数据只在这里维护；每行的操作成功后重新拉一次列表，保持与服务端一致
 * （收藏 / 公开状态是全局的，上传完成后也可能出现新记录）。
 * 拖放监听挂在主区域上：桌面端整个内容区都可以接文件，窄屏同样可用。
 */
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

import FileRow from "@/components/FileRow.vue";
import LocaleSwitch from "@/components/LocaleSwitch.vue";
import UploadDock from "@/components/UploadDock.vue";
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

/** 当前视图与文件名过滤（纯前端，后端列表接口是全集）。 */
const view = ref<"all" | "favorites">("all");
const query = ref("");

const uploadDock = ref<InstanceType<typeof UploadDock> | null>(null);

const favorites = computed(() => files.value.filter((file) => file.is_favorite));
const visibleFiles = computed(() => {
  const base = view.value === "favorites" ? favorites.value : files.value;
  const keyword = query.value.trim().toLowerCase();
  if (!keyword) return base;
  return base.filter((file) => file.filename.toLowerCase().includes(keyword));
});
const pageTitle = computed(() =>
  view.value === "favorites" ? t("nav.favorites") : t("nav.all-files"),
);
const emptyKey = computed(() => {
  if (query.value.trim()) return "nav.no-results" as const;
  return view.value === "favorites" ? ("nav.empty-favorites" as const) : ("nav.empty-all" as const);
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

/** 拉列表；401 由 api 客户端处理，其余错误在内容区显示 error.load。 */
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
  void refresh();
});

onBeforeUnmount(() => {
  document.removeEventListener("dragover", swallowDrop);
  document.removeEventListener("drop", swallowDrop);
  window.clearTimeout(actionErrorTimer);
});
</script>

<template>
  <div class="shell">
    <!-- 桌面端侧栏：品牌 / 上传入口 / 视图切换 / 语言与退出 -->
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
        <button
          type="button"
          class="nav-item"
          :class="{ active: view === 'all' }"
          :aria-current="view === 'all' ? 'page' : undefined"
          @click="view = 'all'"
        >
          <span class="nav-icon" v-html="ICONS.file"></span>
          <span class="nav-label">{{ t("nav.all-files") }}</span>
          <span class="nav-count">{{ files.length }}</span>
        </button>
        <button
          type="button"
          class="nav-item"
          :class="{ active: view === 'favorites' }"
          :aria-current="view === 'favorites' ? 'page' : undefined"
          @click="view = 'favorites'"
        >
          <span class="nav-icon" v-html="ICONS.star"></span>
          <span class="nav-label">{{ t("nav.favorites") }}</span>
          <span class="nav-count">{{ favorites.length }}</span>
        </button>
      </nav>

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
          <h1 class="page-title">{{ pageTitle }}</h1>
          <p class="page-sub">{{ t("nav.file-count", { count: visibleFiles.length }) }}</p>
        </div>
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
      </header>

      <!-- 窄屏用标签页切换视图；桌面端用侧栏导航 -->
      <div class="tabs" role="tablist" :aria-label="t('nav.all-files')">
        <button
          type="button"
          class="tab"
          role="tab"
          :class="{ active: view === 'all' }"
          :aria-selected="view === 'all'"
          @click="view = 'all'"
        >
          {{ t("nav.all-files") }}
          <span class="tab-count">{{ files.length }}</span>
        </button>
        <button
          type="button"
          class="tab"
          role="tab"
          :class="{ active: view === 'favorites' }"
          :aria-selected="view === 'favorites'"
          @click="view = 'favorites'"
        >
          {{ t("nav.favorites") }}
          <span class="tab-count">{{ favorites.length }}</span>
        </button>
      </div>

      <section class="content">
        <div v-if="loadError || actionError" class="banners">
          <p v-if="loadError" class="banner" role="alert">{{ loadError }}</p>
          <p v-if="actionError" class="banner" role="alert">{{ actionError }}</p>
        </div>

        <ul v-if="visibleFiles.length" class="list-card">
          <FileRow
            v-for="file in visibleFiles"
            :key="file.id"
            :file="file"
            @favorite="onFavorite(file)"
            @download="onDownload(file)"
            @visibility="onVisibility(file)"
            @delete="onDelete(file)"
          />
        </ul>

        <div v-else class="empty">
          <span class="empty-icon" v-html="view === 'favorites' ? ICONS.star : ICONS.file"></span>
          <p class="empty-title">{{ loaded ? t(emptyKey) : t("state.loading") }}</p>
          <p v-if="loaded && !query.trim() && view === 'all'" class="empty-text">
            <span class="desktop-only">{{ t("upload.hint") }}</span>
            <span class="mobile-only">{{ t("upload.hint-mobile") }}</span>
          </p>
          <button
            v-if="loaded && !query.trim() && view === 'all'"
            type="button"
            class="upload-secondary"
            @click="pickFiles"
          >
            <span class="upload-secondary-icon" v-html="ICONS.upload"></span>
            {{ t("upload.button") }}
          </button>
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
