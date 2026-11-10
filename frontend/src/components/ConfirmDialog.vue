<script setup lang="ts">
/**
 * 通用确认对话框：Teleport 到 body，覆盖一层磨砂黑色半透明遮罩。
 *
 * 删除分组要顺带问「是否连组内文件一起删」，所以支持一个可选的勾选项；
 * 勾选项每次打开都回到未勾选 —— 连带删除这种不可逆动作必须显式选择。
 *
 * Esc 与点遮罩都等于取消；打开时把焦点放到取消按钮上，避免回车误触破坏性操作。
 */
import { onBeforeUnmount, onMounted, ref, watch } from "vue";

import { t } from "@/i18n";

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
const cancelButton = ref<HTMLButtonElement | null>(null);

function onKeydown(event: KeyboardEvent): void {
  if (event.key !== "Escape") return;
  event.stopPropagation();
  emit("cancel");
}

// 勾选状态是「每次打开都重置」的一次性选择：对话框由父组件用 v-if 控制，重开即重建。
watch(
  () => props.title,
  () => {
    checked.value = false;
  },
);

onMounted(() => {
  window.addEventListener("keydown", onKeydown, true);
  cancelButton.value?.focus();
});

onBeforeUnmount(() => {
  window.removeEventListener("keydown", onKeydown, true);
});
</script>

<template>
  <Teleport to="body">
    <div class="dialog-mask" @click.self="emit('cancel')">
      <div class="dialog" role="dialog" aria-modal="true" :aria-label="title">
        <h2 class="dialog-title">{{ title }}</h2>
        <p v-if="message" class="dialog-message">{{ message }}</p>

        <label v-if="checkbox" class="dialog-check">
          <input v-model="checked" type="checkbox" />
          <span class="dialog-check-text">
            {{ checkbox }}
            <span v-if="checked && checkboxHint" class="dialog-check-hint">{{ checkboxHint }}</span>
          </span>
        </label>

        <div class="dialog-actions">
          <button ref="cancelButton" type="button" class="dialog-btn" @click="emit('cancel')">
            {{ t("action.cancel") }}
          </button>
          <button type="button" class="dialog-btn is-danger" @click="emit('confirm', checked)">
            {{ confirmLabel }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>