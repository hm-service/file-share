<script setup lang="ts">
/**
 * 右侧上传区：紧凑的「上传文件」动作条 + 串行上传队列。
 *
 * 状态机：
 *   pending → hashing（分块算 SHA-256，进度映射到整条 0–10%）
 *   → checking（POST /api/check_file，服务端已有该 blob 就直接秒传）
 *   → uploading（XHR 带 Bearer 与进度，映射到整条 10–100%）
 *   → done / instant（1.2s 后自动移出队列）
 *   → failed（保留后端 detail）/ cancelled（用户取消，不算失败）
 *   failed / cancelled 都保留 File 引用，可以「重试」重新走一遍；「移除」时才释放。
 *
 * 取消靠每个队列项自己的 AbortController：hashing 中断分块循环、check_file 中断 fetch、
 * uploading 中断 XHR。取消后本轮 processItem 正常返回，pump 继续处理后面的 pending 项。
 *
 * 整页拖放由父组件（App.vue）负责：它把文件交给这里暴露的 addFiles，队列逻辑只此一份。
 */
import { computed, onBeforeUnmount, ref } from "vue";

import { isAbortError } from "@/abort";
import { ApiError, checkFile, uploadFile } from "@/api";
import { formatSize } from "@/format";
import { hashFile } from "@/hashFile";
import { ICONS } from "@/icons";
import { t } from "@/i18n";

const emit = defineEmits<{ uploaded: [] }>();

type UploadStatus =
  | "pending"
  | "hashing"
  | "checking"
  | "uploading"
  | "instant"
  | "done"
  | "failed"
  | "cancelled";

interface QueueItem {
  id: string;
  /** 失败/取消时保留（重试要重新读同一个文件）；done/instant 与手动移除时才释放。 */
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
  /** 本轮处理的取消句柄；重试会新建一个。 */
  controller: AbortController | null;
}

const REMOVE_DELAY = 1200;

const queue = ref<QueueItem[]>([]);
const fileInput = ref<HTMLInputElement | null>(null);
const hasItems = computed(() => queue.value.length > 0);

let pumping = false;
const removeTimers = new Set<number>();

function queueId(): string {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 10);
}

/** 入队；热区不锁，用户可以边传边继续加文件（整页拖放也走这里）。 */
function addFiles(files: FileList | File[] | null | undefined): void {
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
      controller: null,
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

/** 从队列移除并释放大文件引用（自动移除与手动移除共用）。 */
function dropFromQueue(id: string): void {
  const index = queue.value.findIndex((item) => item.id === id);
  if (index === -1) return;
  const [item] = queue.value.splice(index, 1);
  item.file = null;
  item.controller = null;
}

function scheduleRemove(id: string): void {
  const timer = window.setTimeout(() => {
    removeTimers.delete(timer);
    dropFromQueue(id);
  }, REMOVE_DELAY);
  removeTimers.add(timer);
}

/** 单项流程：算指纹 → 预检秒传 → 真上传；处理结束后保留 File 供失败/取消后重试。 */
async function processItem(item: QueueItem): Promise<void> {
  const file = item.file;
  if (!file) return;

  const controller = new AbortController();
  item.controller = controller;
  item.error = "";
  try {
    item.status = "hashing";
    item.percent = 0;
    item.progress = 0;
    const sha256 = await hashFile(
      file,
      (percent) => {
        item.percent = percent;
        item.progress = Math.round(percent * 0.1);
      },
      controller.signal,
    );

    item.status = "checking";
    item.progress = 10;
    const check = await checkFile(sha256, item.name, controller.signal);
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
    await uploadFile(
      file,
      sha256,
      (percent) => {
        item.percent = percent;
        item.loaded = Math.round((item.size * percent) / 100);
        item.progress = Math.round(10 + percent * 0.9);
      },
      controller.signal,
    );
    item.status = "done";
    item.progress = 100;
    scheduleRemove(item.id);
  } catch (error) {
    // 取消不是失败：signal 已 abort 或异常本身是 AbortError 都记成 cancelled
    if (controller.signal.aborted || isAbortError(error)) {
      item.status = "cancelled";
      item.error = "";
    } else {
      item.status = "failed";
      item.error = messageOf(error);
    }
  } finally {
    item.controller = null;
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

function isProcessing(item: QueueItem): boolean {
  return item.status === "hashing" || item.status === "checking" || item.status === "uploading";
}

/** 处理中可以取消；已 abort 的那一轮按钮先收起，避免重复取消。 */
function canCancel(item: QueueItem): boolean {
  return isProcessing(item) && !item.controller?.signal.aborted;
}

/** 失败/取消的项保留 File，可重试；重试会重新进入串行 pump。 */
function canRetry(item: QueueItem): boolean {
  return (item.status === "failed" || item.status === "cancelled") && item.file !== null;
}

/** pending / failed / cancelled 可以移除；处理中必须先取消，避免留下半截请求。 */
function canRemove(item: QueueItem): boolean {
  return item.status === "pending" || item.status === "failed" || item.status === "cancelled";
}

function cancelItem(id: string): void {
  const item = queue.value.find((entry) => entry.id === id);
  if (!item || !isProcessing(item)) return;
  const controller = item.controller;
  if (!controller || controller.signal.aborted) return;
  controller.abort();
}

function retryItem(id: string): void {
  const item = queue.value.find((entry) => entry.id === id);
  if (!item || !canRetry(item)) return;
  item.status = "pending";
  item.progress = 0;
  item.percent = 0;
  item.loaded = 0;
  item.error = "";
  item.controller = null;
  void pump();
}

function removeItem(id: string): void {
  const item = queue.value.find((entry) => entry.id === id);
  if (!item || !canRemove(item)) return;
  dropFromQueue(id);
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
    case "cancelled":
      return t("upload.status.cancelled");
  }
}

function statusClass(item: QueueItem): string {
  if (item.status === "failed") return "failed";
  if (item.status === "cancelled") return "cancelled";
  if (item.status === "done" || item.status === "instant") return "done";
  return "working";
}

function barClass(item: QueueItem): string {
  if (item.status === "failed") return "failed";
  if (item.status === "cancelled") return "cancelled";
  if (item.status === "done" || item.status === "instant") return "done";
  return "working";
}

function openPicker(): void {
  fileInput.value?.click();
}

function onFileChange(event: Event): void {
  addFiles((event.target as HTMLInputElement).files);
}

onBeforeUnmount(() => {
  for (const timer of removeTimers) window.clearTimeout(timer);
  removeTimers.clear();
  // 页面离开时中断仍在进行的请求（服务端会清理临时文件），并释放文件引用
  for (const item of queue.value) {
    item.controller?.abort();
    item.controller = null;
    item.file = null;
  }
});

// 整页拖放由 App.vue 把文件转交进来，队列逻辑不在父组件复制
defineExpose({ addFiles });
</script>

<template>
  <div class="upload-panel">
    <div class="upload-actions">
      <button type="button" class="upload-btn" @click="openPicker">
        <span class="upload-btn-icon" v-html="ICONS.upload"></span>
        <span>{{ t("upload.button") }}</span>
      </button>
      <p class="upload-hint">{{ t("upload.hint") }}</p>
      <input
        ref="fileInput"
        class="hidden-input"
        type="file"
        multiple
        tabindex="-1"
        @change="onFileChange"
      />
    </div>

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
                v-if="canCancel(item)"
                type="button"
                class="queue-action cancel"
                @click="cancelItem(item.id)"
              >
                {{ t("upload.action.cancel") }}
              </button>
              <button
                v-if="canRetry(item)"
                type="button"
                class="queue-action retry"
                @click="retryItem(item.id)"
              >
                {{ t("upload.action.retry") }}
              </button>
              <button
                v-if="canRemove(item)"
                type="button"
                class="queue-action remove"
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
  </div>
</template>
