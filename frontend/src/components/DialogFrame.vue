<script setup lang="ts">
/**
 * 对话框外壳：Teleport 到 body 的磨砂遮罩 + 白卡片 + 标题 / 说明 / 取消与确认按钮。
 *
 * ConfirmDialog（危险确认，可带勾选项）与 PromptDialog（文本输入）共用这一层：
 * 遮罩、Esc 取消、点遮罩取消、按钮样式与初始焦点都只在这里维护一次，
 * 各自只负责插在中间的字段（勾选项 / 输入框）。
 */
import { onBeforeUnmount, onMounted, ref } from "vue";

import { t } from "@/i18n";

const props = withDefaults(
  defineProps<{
    title: string;
    /** 标题下方的说明文字。 */
    hint?: string;
    confirmLabel: string;
    /** 左侧取消按钮的文案；纯展示型对话框传「关闭」之类。默认「取消」。 */
    cancelLabel?: string;
    /** 确认按钮配色：删除等不可逆操作用 danger。 */
    tone?: "primary" | "danger";
    confirmDisabled?: boolean;
    /** 打开时的焦点；默认落在「取消」上，避免回车误触破坏性操作。 */
    autofocus?: "cancel" | "confirm" | "none";
  }>(),
  { tone: "primary", confirmDisabled: false, autofocus: "cancel" },
);

const emit = defineEmits<{
  confirm: [];
  cancel: [];
}>();

const cancelButton = ref<HTMLButtonElement | null>(null);
const confirmButton = ref<HTMLButtonElement | null>(null);

function onKeydown(event: KeyboardEvent): void {
  if (event.key !== "Escape") return;
  event.stopPropagation();
  emit("cancel");
}

onMounted(() => {
  window.addEventListener("keydown", onKeydown, true);
  if (props.autofocus === "cancel") cancelButton.value?.focus();
  else if (props.autofocus === "confirm") confirmButton.value?.focus();
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
        <p v-if="hint" class="dialog-message">{{ hint }}</p>

        <slot />

        <div class="dialog-actions">
          <button ref="cancelButton" type="button" class="dialog-btn" @click="emit('cancel')">
            {{ cancelLabel ?? t("action.cancel") }}
          </button>
          <button
            ref="confirmButton"
            type="button"
            class="dialog-btn"
            :class="`is-${tone}`"
            :disabled="confirmDisabled"
            @click="emit('confirm')"
          >
            {{ confirmLabel }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
/* 磨砂黑色半透明遮罩：把背后的列表压下去，同时保留上下文 */
.dialog-mask {
  position: fixed;
  inset: 0;
  z-index: 200;
  display: grid;
  place-items: center;
  padding: 24px;
  background: rgb(12 14 24 / 0.45);
  backdrop-filter: blur(10px) saturate(120%);
  -webkit-backdrop-filter: blur(10px) saturate(120%);
}

.dialog {
  width: min(420px, 100%);
  padding: 22px 22px 18px;
  border-radius: 18px;
  background: var(--surface);
  box-shadow: var(--shadow-lg);
}

.dialog-title {
  color: var(--ink);
  font-size: 16px;
  font-weight: 700;
  letter-spacing: -0.01em;
}

.dialog-message {
  margin-top: 8px;
  color: var(--ink-2);
  font-size: 13px;
  line-height: 1.65;
}

.dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}

.dialog-btn {
  padding: 9px 16px;
  border: 1px solid var(--line);
  border-radius: 11px;
  background: var(--surface);
  color: var(--ink-2);
  font-size: 13px;
  font-weight: 600;
  transition:
    background-color 0.15s,
    border-color 0.15s;
}

.dialog-btn:hover {
  background: var(--surface-2);
}

.dialog-btn.is-danger {
  border-color: transparent;
  background: var(--rose);
  color: #fff;
}

.dialog-btn.is-danger:hover {
  background: #be123c;
}

.dialog-btn.is-primary {
  border-color: transparent;
  background: var(--accent);
  color: #fff;
}

.dialog-btn.is-primary:hover {
  background: var(--accent-strong);
}

.dialog-btn:disabled {
  opacity: 0.45;
}
</style>