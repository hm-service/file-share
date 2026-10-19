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
