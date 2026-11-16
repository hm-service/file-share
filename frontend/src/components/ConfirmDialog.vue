<script setup lang="ts">
/**
 * 通用确认对话框：内容骑在 DialogFrame 上，支持一个可选的勾选项。
 *
 * 删除分组要顺带问「是否连组内文件一起删」，所以有勾选项；勾选项每次打开都回到未勾选 ——
 * 连带删除这种不可逆动作必须显式选择。回车误触由外壳把初始焦点放在「取消」上避免。
 */
import { ref, watch } from "vue";

import DialogFrame from "@/components/DialogFrame.vue";

const props = defineProps<{
  title: string;
  message?: string;
  /** 有这项时才显示勾选框。 */
  checkbox?: string;
  /** 勾选状态下补充说明（例如「文件会被永久删除」）。 */
  checkboxHint?: string;
  confirmLabel: string;
}>();

const emit = defineEmits<{
  confirm: [checked: boolean];
  cancel: [];
}>();

const checked = ref(false);

// 勾选状态是「每次打开都重置」的一次性选择：对话框由父组件用 v-if 控制，重开即重建。
watch(
  () => props.title,
  () => {
    checked.value = false;
  },
);
</script>

<template>
  <DialogFrame
    :title="title"
    :hint="message"
    :confirm-label="confirmLabel"
    tone="danger"
    @confirm="emit('confirm', checked)"
    @cancel="emit('cancel')"
  >
    <label v-if="checkbox" class="dialog-check">
      <input v-model="checked" type="checkbox" />
      <span class="dialog-check-text">
        {{ checkbox }}
        <span v-if="checked && checkboxHint" class="dialog-check-hint">{{ checkboxHint }}</span>
      </span>
    </label>
  </DialogFrame>
</template>

<style scoped>
.dialog-check {
  display: flex;
  align-items: flex-start;
  gap: 9px;
  margin-top: 14px;
  padding: 10px 12px;
  border: 1px solid var(--line);
  border-radius: 11px;
  background: var(--surface-2);
  color: var(--ink-2);
  font-size: 12.5px;
  cursor: pointer;
}

.dialog-check input {
  flex: 0 0 auto;
  margin-top: 2px;
  accent-color: var(--rose);
}

.dialog-check-hint {
  display: block;
  margin-top: 3px;
  color: var(--rose);
  font-size: 11.5px;
}
</style>