<script setup lang="ts">
/**
 * 文件详情：点文件名打开，复用 DialogFrame 的居中对话框。
 *
 * 只读展示大小、上传时间、所在分组与完整 SHA-256（等宽字体、可选中复制），
 * 底部「关闭 / 下载」两个按钮；下载交给父组件（FileRow 转发给列表）。
 */
import { formatSize, formatTime } from "@/format";
import { t } from "@/i18n";
import type { FileItem } from "@/types";

import DialogFrame from "./DialogFrame.vue";

defineProps<{ file: FileItem }>();

const emit = defineEmits<{
  close: [];
  download: [];
}>();
</script>

<template>
  <DialogFrame
    :title="t('file.detail.title')"
    :confirm-label="t('action.download')"
    :cancel-label="t('action.close')"
    autofocus="none"
    @confirm="emit('download')"
    @cancel="emit('close')"
  >
    <p class="detail-name" :title="file.filename">{{ file.filename }}</p>

    <dl class="detail-list">
      <div class="detail-row">
        <dt>{{ t("file.detail.size") }}</dt>
        <dd>{{ formatSize(file.size) }}</dd>
      </div>
      <div class="detail-row">
        <dt>{{ t("file.detail.time") }}</dt>
        <dd>{{ formatTime(file.created_at) }}</dd>
      </div>
      <div class="detail-row">
        <dt>{{ t("file.detail.group") }}</dt>
        <dd>{{ file.group_path || t("nav.ungrouped") }}</dd>
      </div>
      <div class="detail-row">
        <dt>{{ t("file.sha256") }}</dt>
        <dd class="detail-sha">{{ file.sha256 }}</dd>
      </div>
    </dl>
  </DialogFrame>
</template>

<style scoped>
/* 文件名可能很长：允许在任意字符处折行，不撑破对话框 */
.detail-name {
  overflow-wrap: anywhere;
  color: var(--ink);
  font-size: 13.5px;
  font-weight: 620;
  line-height: 1.5;
}

.detail-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid var(--line);
}

.detail-row {
  display: flex;
  align-items: baseline;
  gap: 14px;
  padding: 5px 0;
}

.detail-row dt {
  flex: 0 0 auto;
  width: 68px;
  color: var(--ink-3);
  font-size: 12px;
}

.detail-row dd {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--ink-2);
  font-size: 12.5px;
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
}

/* 完整指纹：等宽 + 可选中复制 */
.detail-sha {
  font-family: ui-monospace, sfmono-regular, menlo, consolas, monospace;
  font-size: 11.5px;
  line-height: 1.6;
  user-select: text;
}
</style>