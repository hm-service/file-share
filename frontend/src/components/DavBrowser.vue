<script setup lang="ts">
/**
 * 「网盘」视图：浏览 WebDAV 命名空间（独立的 dav_node 表，和文件列表互不可见）。
 *
 * 只做浏览与下载：目录点进去，文件点下载。下载走 /api/dav/ticket 换一张短期票据，
 * 再由浏览器导航下载，和文件列表同一套机制。
 */
import { computed, onMounted, ref } from "vue";

import { downloadDav, listDav } from "@/api";
import { formatSize, formatTime } from "@/format";
import { ICONS } from "@/icons";
import { t } from "@/i18n";
import type { DavEntry } from "@/types";

/** 当前目录的内部路径；空串是根。 */
const path = ref("");
const entries = ref<DavEntry[]>([]);
const loading = ref(true);
const error = ref("");

/** 面包屑：路径每一段，点击回到对应层级。 */
const crumbs = computed(() => {
  if (!path.value) return [];
  const segments = path.value.split("/");
  return segments.map((name, index) => ({
    name,
    path: segments.slice(0, index + 1).join("/"),
  }));
});

async function open(next: string): Promise<void> {
  loading.value = true;
  error.value = "";
  try {
    const payload = await listDav(next);
    path.value = payload.path;
    entries.value = payload.entries;
  } catch (err) {
    error.value = err instanceof Error ? err.message : t("drive.error");
  } finally {
    loading.value = false;
  }
}

async function download(entry: DavEntry): Promise<void> {
  try {
    await downloadDav(entry.path);
  } catch (err) {
    error.value = err instanceof Error ? err.message : t("drive.error");
  }
}

onMounted(() => void open(""));
</script>

<template>
  <div class="dav">
    <nav class="dav-crumbs" :aria-label="t('nav.drive')">
      <button
        type="button"
        class="dav-crumb"
        :class="{ 'is-current': !path }"
        @click="open('')"
      >
        {{ t("drive.root") }}
      </button>
      <template v-for="crumb in crumbs" :key="crumb.path">
        <span class="dav-crumb-sep" aria-hidden="true">/</span>
        <button
          type="button"
          class="dav-crumb"
          :class="{ 'is-current': crumb.path === path }"
          @click="open(crumb.path)"
        >
          {{ crumb.name }}
        </button>
      </template>
    </nav>

    <p v-if="error" class="banner" role="alert">{{ error }}</p>

    <ul v-if="entries.length" class="dav-list">
      <li v-for="entry in entries" :key="entry.path" class="dav-row">
        <button
          v-if="entry.is_dir"
          type="button"
          class="dav-main"
          :title="entry.name"
          @click="open(entry.path)"
        >
          <span class="dav-icon" v-html="ICONS.folder"></span>
          <span class="dav-name">{{ entry.name }}</span>
          <span class="dav-meta">{{ t("drive.dir") }}</span>
          <span class="dav-chevron" v-html="ICONS.chevron"></span>
        </button>
        <div v-else class="dav-main is-file">
          <span class="dav-icon" v-html="ICONS.file"></span>
          <span class="dav-name" :title="entry.name">{{ entry.name }}</span>
          <span class="dav-meta">{{ formatSize(entry.size) }} · {{ formatTime(entry.updated_at) }}</span>
        </div>
        <button
          v-if="!entry.is_dir"
          type="button"
          class="icon-btn download dav-download"
          :title="t('action.download')"
          :aria-label="t('action.download')"
          v-html="ICONS.download"
          @click="download(entry)"
        ></button>
      </li>
    </ul>

    <div v-else class="empty dav-empty">
      <span class="empty-icon" v-html="ICONS.drive"></span>
      <p class="empty-title">{{ loading ? t("state.loading") : t("drive.empty") }}</p>
    </div>
  </div>
</template>