<script setup lang="ts">
/**
 * 列表里的一行文件：名称 / 时间 / 大小，hover 显示操作按钮。
 *
 * 复制直链只对公开文件可用（私有文件的 /s/{id} 会 404，复制了也没用）；
 * navigator.clipboard 在 HTTP 部署下不存在或不可写，所以有 textarea + execCommand 兜底。
 */
import { onBeforeUnmount, ref } from "vue";

import { publicLink } from "@/api";
import { formatSize, formatTime } from "@/format";
import { ICONS } from "@/icons";
import { t } from "@/i18n";
import type { FileItem } from "@/types";

const props = defineProps<{ file: FileItem }>();

const emit = defineEmits<{
  favorite: [];
  download: [];
  visibility: [];
  delete: [];
}>();

const copied = ref(false);
let copiedTimer = 0;

/** 复制成功返回 true；失败不弹窗，只是不显示「已复制」。 */
async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* 非安全上下文（纯 HTTP）或权限被拒：走下面的兜底 */
  }
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.setAttribute("readonly", "");
    textarea.style.position = "fixed";
    textarea.style.top = "-1000px";
    textarea.style.opacity = "0";
    document.body.appendChild(textarea);
    textarea.select();
    textarea.setSelectionRange(0, text.length);
    const ok = document.execCommand("copy");
    textarea.remove();
    return ok;
  } catch {
    return false;
  }
}

async function copyLink(): Promise<void> {
  if (!props.file.is_public) return;
  const ok = await copyText(publicLink(props.file.id));
  if (!ok) return;
  copied.value = true;
  window.clearTimeout(copiedTimer);
  copiedTimer = window.setTimeout(() => {
    copied.value = false;
  }, 1200);
}

onBeforeUnmount(() => window.clearTimeout(copiedTimer));
</script>

<template>
  <li class="file-item">
    <div class="file-main">
      <span class="file-glyph" v-html="ICONS.file"></span>
      <div class="file-text">
        <p class="file-name" :title="file.filename">{{ file.filename }}</p>
        <p class="file-meta">{{ formatTime(file.created_at) }} · {{ formatSize(file.size) }}</p>
      </div>
    </div>

    <div class="file-actions">
      <button
        type="button"
        class="icon-btn star"
        :class="{ on: file.is_favorite }"
        :title="file.is_favorite ? t('action.unfavorite') : t('action.favorite')"
        :aria-label="file.is_favorite ? t('action.unfavorite') : t('action.favorite')"
        v-html="ICONS.star"
        @click="emit('favorite')"
      ></button>

      <button
        type="button"
        class="icon-btn download"
        :title="t('action.download')"
        :aria-label="t('action.download')"
        v-html="ICONS.download"
        @click="emit('download')"
      ></button>

      <button
        type="button"
        class="icon-btn link"
        :class="{ 'is-copied': copied, 'is-disabled': !file.is_public }"
        :disabled="!file.is_public"
        :title="
          file.is_public
            ? copied
              ? t('action.copied')
              : t('action.copy-link')
            : t('action.copy-link-private')
        "
        :aria-label="file.is_public ? t('action.copy-link') : t('action.copy-link-private')"
        @click="copyLink"
      >
        <span v-if="copied" class="copied-text">{{ t("action.copied") }}</span>
        <span v-else v-html="ICONS.link"></span>
      </button>

      <button
        type="button"
        class="icon-btn visibility"
        :class="{ on: file.is_public }"
        :title="file.is_public ? t('action.make-private') : t('action.make-public')"
        :aria-label="file.is_public ? t('action.make-private') : t('action.make-public')"
        v-html="file.is_public ? ICONS.eye : ICONS.eyeOff"
        @click="emit('visibility')"
      ></button>

      <button
        type="button"
        class="icon-btn delete"
        :title="t('action.delete')"
        :aria-label="t('action.delete')"
        v-html="ICONS.trash"
        @click="emit('delete')"
      ></button>
    </div>
  </li>
</template>
