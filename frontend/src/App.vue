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
  <div class="app">
    <aside class="sidebar">
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
          {{ t("action.logout") }}
        </button>
      </footer>
    </aside>

    <main class="workspace">
      <UploadPanel @uploaded="refresh" />
    </main>
  </div>
</template>
