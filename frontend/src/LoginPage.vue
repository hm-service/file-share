<script setup lang="ts">
/**
 * 登录页（/auth）：输入访问令牌换会话。
 * 401 显示 auth.error，网络层失败显示 error.network；成功后写 localStorage 再回主页面。
 *
 * 桌面端是「左侧品牌介绍 + 右侧登录卡」的分栏，窄屏收成品牌条 + 登录卡。
 */
import { ref } from "vue";

import { ApiError, login } from "@/api";
import LocaleSwitch from "@/components/LocaleSwitch.vue";
import { ICONS } from "@/icons";
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
  <div class="login-shell">
    <aside class="login-aside">
      <div class="login-brand">
        <span class="brand-mark brand-mark-inverse" v-html="ICONS.upload"></span>
        <div>
          <p class="login-brand-name">{{ t("app.title") }}</p>
          <p class="login-brand-sub">{{ t("app.tagline") }}</p>
        </div>
      </div>

      <div class="login-hero">
        <h1 class="login-headline">{{ t("auth.hero-title") }}</h1>
        <p class="login-sub">{{ t("auth.hint") }}</p>
        <ul class="login-points">
          <li>
            <span class="login-point-icon" v-html="ICONS.bolt"></span>
            {{ t("auth.point.instant") }}
          </li>
          <li>
            <span class="login-point-icon" v-html="ICONS.link"></span>
            {{ t("auth.point.share") }}
          </li>
          <li>
            <span class="login-point-icon" v-html="ICONS.lock"></span>
            {{ t("auth.point.private") }}
          </li>
        </ul>
      </div>

      <p class="login-aside-foot">SHA-256 · Go · Vue</p>
    </aside>

    <main class="login-main">
      <form class="login-card" novalidate @submit.prevent="submit">
        <header class="login-card-head">
          <p class="login-card-kicker">{{ t("app.title") }}</p>
          <LocaleSwitch />
        </header>

        <h2 class="login-title">{{ t("auth.title") }}</h2>
        <p class="login-hint">{{ t("auth.hint") }}</p>

        <label class="field">
          <span class="field-label">{{ t("auth.token-placeholder") }}</span>
          <input
            v-model="token"
            class="field-input"
            type="password"
            name="token"
            autocomplete="current-password"
            placeholder="AUTH_TOKEN"
            autofocus
          />
        </label>

        <button class="primary-btn" type="submit" :disabled="submitting">
          {{ t("auth.submit") }}
        </button>

        <p v-if="error" class="login-error" role="alert">{{ error }}</p>
      </form>
    </main>
  </div>
</template>
