<script setup lang="ts">
/**
 * 分组统计：右键菜单「统计信息」打开，只读展示该分组及其全部子分组的文件汇总。
 *
 * - 总文件数量与总文件大小；
 * - 文件大小分布：1 GiB 以下每 256 MiB 一档，1 GiB 以上每 512 MiB 一档，
 *   档位标签用 MiB / GiB，只列到最大文件所在的那一档（尾部空档不占地方）。
 *
 * 数据来自列表接口已经下发的完整文件列表，这里纯前端汇总，不额外请求服务端。
 */
import { computed } from "vue";

import { formatSize } from "@/format";
import { t } from "@/i18n";
import type { FileItem } from "@/types";

import DialogFrame from "./DialogFrame.vue";

const props = defineProps<{
  /** 分组完整路径：标题下方显示成说明，顺带说明统计范围。 */
  path: string;
  /** 该分组及其全部子分组下的文件。 */
  files: FileItem[];
}>();

const emit = defineEmits<{ close: [] }>();

const MIB = 1024 ** 2;
const GIB = 1024 ** 3;
/** 1 GiB 以下每档 256 MiB；1 GiB 以上每档 512 MiB。 */
const SMALL_STEP = 256 * MIB;
const LARGE_STEP = 512 * MIB;

interface SizeBucket {
  /** 区间文案，例如「256 – 512 MiB」「1 – 1.5 GiB」。 */
  label: string;
  count: number;
}

/** 区间下标：第 0~3 档覆盖 0~1 GiB，之后每 512 MiB 一档。 */
function bucketIndex(size: number): number {
  if (size < GIB) return Math.min(3, Math.floor(Math.max(size, 0) / SMALL_STEP));
  return 4 + Math.floor((size - GIB) / LARGE_STEP);
}

/** 档位边界：前四档 256 MiB 步长，之后 512 MiB 步长。 */
function bucketBounds(index: number): { from: number; to: number } {
  if (index < 4) return { from: index * SMALL_STEP, to: (index + 1) * SMALL_STEP };
  const from = GIB + (index - 4) * LARGE_STEP;
  return { from, to: from + LARGE_STEP };
}

/** 边界值按 MiB / GiB 显示：1 GiB 以下记 MiB，以上记 GiB，去掉多余的 .0。 */
function boundLabel(bytes: number): string {
  if (bytes < GIB) return `${bytes / MIB} MiB`;
  return `${Number.parseFloat((bytes / GIB).toFixed(2))} GiB`;
}

const totalSize = computed(() => props.files.reduce((sum, file) => sum + file.size, 0));

const buckets = computed<SizeBucket[]>(() => {
  if (!props.files.length) return [];
  let maxIndex = 0;
  for (const file of props.files) maxIndex = Math.max(maxIndex, bucketIndex(file.size));
  const counts = new Array<number>(maxIndex + 1).fill(0);
  for (const file of props.files) counts[bucketIndex(file.size)] += 1;
  return counts.map((count, index) => {
    const { from, to } = bucketBounds(index);
    return { label: `${boundLabel(from)} – ${boundLabel(to)}`, count };
  });
});

/** 档位条按最大计数归一化。 */
const maxBucketCount = computed(() => Math.max(1, ...buckets.value.map((bucket) => bucket.count)));
</script>

<template>
  <DialogFrame
    :title="t('group.stats.title')"
    :hint="t('group.stats.hint', { path })"
    :cancel-label="t('action.close')"
    autofocus="cancel"
    @cancel="emit('close')"
  >
    <dl class="stats-summary">
      <div class="stats-row">
        <dt>{{ t("group.stats.files") }}</dt>
        <dd>{{ t("group.stats.file-count", { count: files.length }) }}</dd>
      </div>
      <div class="stats-row">
        <dt>{{ t("group.stats.total-size") }}</dt>
        <dd>{{ formatSize(totalSize) }}</dd>
      </div>
    </dl>

    <section v-if="buckets.length" class="stats-distribution">
      <h3 class="stats-heading">{{ t("group.stats.distribution") }}</h3>
      <ul class="stats-buckets">
        <li v-for="bucket in buckets" :key="bucket.label" class="stats-bucket">
          <span class="bucket-label">{{ bucket.label }}</span>
          <span class="bucket-bar" aria-hidden="true">
            <span
              class="bucket-fill"
              :class="{ 'is-empty': !bucket.count }"
              :style="{ width: `${(bucket.count / maxBucketCount) * 100}%` }"
            ></span>
          </span>
          <span class="bucket-count">{{ bucket.count }}</span>
        </li>
      </ul>
    </section>
    <p v-else class="stats-empty">{{ t("group.stats.empty") }}</p>
  </DialogFrame>
</template>

<style scoped>
.stats-summary {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid var(--line);
}

.stats-row {
  display: flex;
  align-items: baseline;
  gap: 14px;
  padding: 5px 0;
}

.stats-row dt {
  flex: 0 0 auto;
  width: 76px;
  color: var(--ink-3);
  font-size: 12px;
}

.stats-row dd {
  flex: 1 1 auto;
  color: var(--ink);
  font-size: 13px;
  font-weight: 620;
  font-variant-numeric: tabular-nums;
}

.stats-distribution {
  margin-top: 14px;
}

.stats-heading {
  color: var(--ink-3);
  font-size: 12px;
  font-weight: 600;
}

.stats-buckets {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-top: 8px;
}

.stats-bucket {
  display: grid;
  grid-template-columns: 108px minmax(0, 1fr) 28px;
  align-items: center;
  gap: 10px;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.bucket-label {
  color: var(--ink-2);
  white-space: nowrap;
}

.bucket-bar {
  display: block;
  height: 6px;
  border-radius: 999px;
  background: var(--surface-2);
  overflow: hidden;
}

.bucket-fill {
  display: block;
  height: 100%;
  min-width: 3px;
  border-radius: inherit;
  background: var(--accent);
}

/* 空档不留任何痕迹（min-width 只用来让个位数的档位看得见） */
.bucket-fill.is-empty {
  min-width: 0;
}

.bucket-count {
  color: var(--ink-2);
  text-align: right;
}

.stats-empty {
  margin-top: 14px;
  color: var(--ink-3);
  font-size: 12.5px;
}
</style>