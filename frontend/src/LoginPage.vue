<script setup lang="ts">
/**
 * 登录页（/auth）：输入访问令牌换会话。
 * 401 显示 auth.error，网络层失败显示 error.network；成功后写 localStorage 再回主页面。
 */
import { ref } from "vue";

import { ApiError, login } from "@/api";
import LocaleSwitch from "@/components/LocaleSwitch.vue";
import { t } from "@/i18n";
import { saveSession } from "@/session";

const token = ref("");
const submitting = ref(false);
const error = ref("");

async function submit(): Promise<void> {
  if (submitting.value) return;
  const value = token.value.trim();
  if (!value) {
    error.value = t("auth.error");
    return;
  }
  submitting.value = true;
  error.value = "";
  try {
    const session = await login(value);
    saveSession({ token: session.token, expires_at: session.expires_at });
    location.href = "/";
  } catch (cause) {
    // 401 = 令牌不对；status 0 = 网络层失败；其余（400 之类）也按登录失败处理
    if (cause instanceof ApiError && cause.status === 0) error.value = t("error.network");
    else error.value = t("auth.error");
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="login-page">
    <main class="login-card">
      <header class="login-head">
        <p class="login-brand">{{ t("app.title") }}</p>
        <LocaleSwitch />
      </header>

      <h1 class="login-title">{{ t("auth.title") }}</h1>
      <p class="login-hint">{{ t("auth.hint") }}</p>

      <form class="login-form" novalidate @submit.prevent="submit">
        <input
          v-model="token"
          class="login-input"
          type="password"
          name="token"
          autocomplete="current-password"
          :placeholder="t('auth.token-placeholder')"
          :aria-label="t('auth.token-placeholder')"
          autofocus
        />
        <button class="primary-btn" type="submit" :disabled="submitting">
          {{ t("auth.submit") }}
        </button>
      </form>

      <p v-if="error" class="login-error" role="alert">{{ error }}</p>
    </main>
  </div>
</template>
