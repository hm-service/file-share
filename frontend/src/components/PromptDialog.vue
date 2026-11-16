<script setup lang="ts">
/**
 * 文本输入对话框：内容骑在 DialogFrame 上，只负责输入框与提交语义。
 *
 * 代替 window.prompt：浏览器原生对话框带「阻止此页面创建更多对话框」勾选项，一旦被勾上
 * 就不再出现，而页面又依赖它的返回值，等于相关操作永久失效、且很难改回来；自建对话框
 * 没有这个状态，样式也与界面一致。
 *
 * 打开时焦点落在输入框并全选原有内容；Enter 提交（输入法组合中不提交），Esc 与点遮罩
 * 由外壳取消。去空白后为空时确认按钮保持禁用，调用方拿到的永远是有效输入。
 */
import { computed, onMounted, ref, watch } from "vue";

import DialogFrame from "@/components/DialogFrame.vue";

const props = defineProps<{
  title: string;
  /** 输入框上方的说明文字。 */
  hint?: string;
  /** 初始值；重命名时预填当前名字。 */
  value?: string;
  confirmLabel: string;
}>();

const emit = defineEmits<{
  confirm: [value: string];
  cancel: [];
}>();

const text = ref(props.value ?? "");
const input = ref<HTMLInputElement | null>(null);

const canSubmit = computed(() => text.value.trim().length > 0);

function submit(): void {
  const value = text.value.trim();
  if (!value) return;
  emit("confirm", value);
}

/** 输入法组合中的回车只用来选词，不能当提交。 */
function onEnterKeydown(event: KeyboardEvent): void {
  if (event.isComposing) {
    event.preventDefault();
    return;
  }
  submit();
}

// 对话框由父组件用 v-if 控制，正常重开会重建实例；万一父组件在打开状态下换了目标，
// 也要回到新目标的初始值。
watch(
  () => [props.title, props.value],
  () => {
    text.value = props.value ?? "";
  },
);

onMounted(() => {
  input.value?.focus();
  input.value?.select();
});
</script>

<template>
  <DialogFrame
    :title="title"
    :hint="hint"
    :confirm-label="confirmLabel"
    :confirm-disabled="!canSubmit"
    autofocus="none"
    @confirm="submit"
    @cancel="emit('cancel')"
  >
    <input
      ref="input"
      v-model="text"
      class="dialog-input"
      type="text"
      autocomplete="off"
      spellcheck="false"
      :aria-label="title"
      @keydown.enter="onEnterKeydown"
    />
  </DialogFrame>
</template>

<style scoped>
/* 与登录页的 .field-input 同一套边框与聚焦光晕 */
.dialog-input {
  width: 100%;
  margin-top: 14px;
  padding: 10px 12px;
  border: 1px solid var(--line);
  border-radius: 11px;
  background: var(--surface);
  color: var(--ink);
  transition:
    border-color 0.15s,
    box-shadow 0.15s;
}

.dialog-input:focus {
  border-color: var(--accent);
  outline: none;
  box-shadow: 0 0 0 3px rgb(91 91 224 / 0.13);
}
</style>