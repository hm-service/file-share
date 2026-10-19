<script setup lang="ts">
/**
 * 右侧上传区：拖拽/点击选择文件 → 串行队列。
 *
 * 每一项的状态机与旧界面一致：
 *   pending → hashing（分块算 SHA-256，进度映射到整条 0–10%）
 *   → checking（POST /api/check_file，服务端已有该 blob 就直接秒传）
 *   → uploading（XHR 带 Bearer 与进度，映射到整条 10–100%）
 *   → done / instant（1.2s 后自动移出队列）/ failed（保留后端 detail，可手动移除）。
 *
 * 队列跑空后通知父组件刷新列表（秒传也会在服务端插入记录，所以必须刷新）。
 */
import { computed, onBeforeUnmount, ref } from "vue";

import { ApiError, checkFile, uploadFile } from "@/api";
import { formatSize } from "@/format";
import { hashFile } from "@/hashFile";
import { ICONS } from "@/icons";
import { t } from "@/i18n";

const emit = defineEmits<{ uploaded: [] }>();

type UploadStatus = "pending" | "hashing" | "checking" | "uploading" | "instant" | "done" | "failed";

interface QueueItem {
  id: string;
  /** 处理完就置 null，避免大文件引用一直留在队列里。 */
  file: File | null;
  name: string;
  size: number;
  status: UploadStatus;
  /** 进度条 0–100：hashing 占 0–10，uploading 占 10–100。 */
  progress: number;
  /** 当前阶段百分比，文案里显示。 */
  percent: number;
  /** 已发送字节数（按 server 的 body 大小估算，用于上传中的 loaded）。 */
  loaded: number;
  /** 失败原因：优先用后端本地化 detail。 */
  error: string;
}

const REMOVE_DELAY = 1200;

const queue = ref<QueueItem[]>([]);
const dragging = ref(false);
const fileInput = ref<HTMLInputElement | null>(null);
const hasItems = computed(() => queue.value.length > 0);

let pumping = false;
const removeTimers = new Set<number>();

function queueId(): string {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 10);
}

/** 入队；热区不锁，用户可以边传边继续加文件。 */
function addFiles(files: FileList | null | undefined): void {
  const list = Array.from(files ?? []);
  if (!list.length) return;
  for (const file of list) {
    queue.value.push({
      id: queueId(),
      file,
      name: file.name,
      size: file.size,
      status: "pending",
      progress: 0,
      percent: 0,
      loaded: 0,
      error: "",
    });
  }
  if (fileInput.value) fileInput.value.value = "";
  void pump();
}

function messageOf(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  if (error instanceof Error && error.message) return error.message;
  return t("error.network");
}

function scheduleRemove(id: string): void {
  const timer = window.setTimeout(() => {
    removeTimers.delete(timer);
    const index = queue.value.findIndex((item) => item.id === id);
    if (index !== -1) queue.value.splice(index, 1);
  }, REMOVE_DELAY);
  removeTimers.add(timer);
}

/** 单项流程：算指纹 → 预检秒传 → 真上传。 */
async function processItem(item: QueueItem): Promise<void> {
  const file = item.file;
  if (!file) return;
  try {
    item.status = "hashing";
    item.percent = 0;
    item.progress = 0;
    const sha256 = await hashFile(file, (percent) => {
      item.percent = percent;
      item.progress = Math.round(percent * 0.1);
    });

    item.status = "checking";
    item.progress = 10;
    const check = await checkFile(sha256, item.name);
    if (check.exists) {
      item.status = "instant";
      item.progress = 100;
      scheduleRemove(item.id);
      return;
    }

    item.status = "uploading";
    item.percent = 0;
    item.progress = 10;
    item.loaded = 0;
    await uploadFile(file, sha256, (percent) => {
      item.percent = percent;
      item.loaded = Math.round((item.size * percent) / 100);
      item.progress = Math.round(10 + percent * 0.9);
    });
    item.status = "done";
    item.progress = 100;
    scheduleRemove(item.id);
  } catch (error) {
    item.status = "failed";
    item.error = messageOf(error);
  } finally {
    item.file = null;
  }
}

/** 串行调度：队列里没有 pending 就退出，跑空后刷新一次列表。 */
async function pump(): Promise<void> {
  if (pumping) return;
  pumping = true;
  let changed = false;
  try {
    for (;;) {
      const item = queue.value.find((entry) => entry.status === "pending");
      if (!item) break;
      await processItem(item);
      changed = true;
    }
  } finally {
    pumping = false;
  }
  if (changed) emit("uploaded");
}

/** pending 与 failed 可以由用户移除；处理中不允许，避免半截文件留在服务端。 */
function canRemove(item: QueueItem): boolean {
  return item.status === "pending" || item.status === "failed";
}

function removeItem(id: string): void {
  const index = queue.value.findIndex((item) => item.id === id);
  if (index === -1) return;
  if (!canRemove(queue.value[index])) return;
  queue.value.splice(index, 1);
}

function statusText(item: QueueItem): string {
  switch (item.status) {
    case "pending":
      return t("upload.status.pending");
    case "hashing":
      return t("upload.status.hashing", { percent: item.percent });
    case "checking":
      return t("upload.status.checking");
    case "uploading":
      return t("upload.status.uploading", {
        percent: item.percent,
        loaded: formatSize(item.loaded),
        total: formatSize(item.size),
      });
    case "instant":
      return t("upload.status.instant");
    case "done":
      return t("upload.status.done");
    case "failed":
      return item.error || t("upload.status.failed");
  }
}

function statusClass(item: QueueItem): string {
  if (item.status === "failed") return "failed";
  if (item.status === "done" || item.status === "instant") return "done";
  return "working";
}

function barClass(item: QueueItem): string {
  if (item.status === "failed") return "failed";
  if (item.status === "done" || item.status === "instant") return "done";
  return "working";
}

function openPicker(): void {
  fileInput.value?.click();
}

function onFileChange(event: Event): void {
  addFiles((event.target as HTMLInputElement).files);
}

function onDragOver(event: DragEvent): void {
  event.preventDefault();
  dragging.value = true;
}

function onDragLeave(event: DragEvent): void {
  // 拖到子元素上也会触发 dragleave，relatedTarget 还在区域内就不算离开
  const current = event.currentTarget as HTMLElement;
  if (event.relatedTarget instanceof Node && current.contains(event.relatedTarget)) return;
  dragging.value = false;
}

function onDrop(event: DragEvent): void {
  event.preventDefault();
  dragging.value = false;
  addFiles(event.dataTransfer?.files);
}

onBeforeUnmount(() => {
  for (const timer of removeTimers) window.clearTimeout(timer);
  removeTimers.clear();
});
</script>

<template>
  <div class="upload-panel">
    <section v-if="hasItems" class="queue-panel" :aria-label="t('upload.queue-title')">
      <h2 class="queue-title">{{ t("upload.queue-title") }}</h2>
      <ul class="queue-list">
        <li v-for="item in queue" :key="item.id" class="queue-item">
          <div class="queue-head">
            <div class="queue-name">
              <p class="file-name" :title="item.name">{{ item.name }}</p>
              <span class="queue-size">{{ formatSize(item.size) }}</span>
            </div>
            <div class="queue-state">
              <span class="queue-status" :class="statusClass(item)">{{ statusText(item) }}</span>
              <button
                v-if="canRemove(item)"
                type="button"
                class="queue-remove"
                @click="removeItem(item.id)"
              >
                {{ t("upload.action.remove") }}
              </button>
            </div>
          </div>
          <div class="progress-track">
            <div
              class="progress-bar"
              :class="barClass(item)"
              :style="{ width: `${item.progress}%` }"
            ></div>
          </div>
        </li>
      </ul>
    </section>

    <div class="drop-wrap">
      <div
        class="dropzone"
        :class="{ 'is-over': dragging }"
        role="button"
        tabindex="0"
        @click="openPicker"
        @keydown.enter.prevent="openPicker"
        @keydown.space.prevent="openPicker"
        @dragover="onDragOver"
        @dragleave="onDragLeave"
        @drop="onDrop"
      >
        <span class="drop-icon" v-html="ICONS.upload"></span>
        <h3 class="drop-title">{{ t("upload.drop-title") }}</h3>
        <p class="drop-hint">{{ t("upload.drop-hint") }}</p>
        <input
          ref="fileInput"
          class="hidden-input"
          type="file"
          multiple
          tabindex="-1"
          @change="onFileChange"
        />
      </div>
    </div>
  </div>
</template>
