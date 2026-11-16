<script setup lang="ts">
/** 语言切换：两个语言各自用母语显示；切换会写 localStorage 并同步 <html lang>。 */
import { LANGUAGES, locale, setLocale, t } from "@/i18n";
</script>

<template>
  <div class="locale-switch" role="group" :aria-label="t('lang.label')">
    <button
      v-for="language in LANGUAGES"
      :key="language.id"
      type="button"
      class="locale-btn"
      :class="{ active: locale === language.id }"
      :lang="language.htmlLang"
      :aria-pressed="locale === language.id"
      @click="setLocale(language.id)"
    >
      {{ language.label }}
    </button>
  </div>
</template>

<style scoped>
/* ================= 语言切换 ================= */
.locale-switch {
  display: inline-flex;
  gap: 2px;
  padding: 2px;
  border: 1px solid var(--line);
  border-radius: 999px;
  background: var(--surface-2);
}

.locale-btn {
  padding: 3px 10px;
  border-radius: 999px;
  color: var(--ink-3);
  font-size: 11px;
  font-weight: 650;
}

.locale-btn:hover {
  color: var(--ink-2);
}

.locale-btn.active {
  background: var(--surface);
  box-shadow: var(--shadow-sm);
  color: var(--ink);
}
</style>
