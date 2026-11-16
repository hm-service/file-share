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

<style scoped>
/* ================= 登录页 ================= */
.login-shell {
  display: flex;
  height: 100vh;
  height: 100dvh;
  overflow: hidden;
  background: var(--bg);
}

.login-aside {
  position: relative;
  display: flex;
  flex: 0 0 46%;
  flex-direction: column;
  justify-content: space-between;
  max-width: 620px;
  padding: 42px;
  overflow: hidden;
  background:
    radial-gradient(700px 420px at 90% 0%, rgb(139 92 246 / 0.45), transparent 60%),
    radial-gradient(600px 480px at 0% 100%, rgb(59 130 246 / 0.28), transparent 55%),
    linear-gradient(155deg, #15162f 0%, #232449 60%, #1d1e3c 100%);
  color: #fff;
}

.login-brand {
  display: flex;
  align-items: center;
  gap: 12px;
}

.brand-mark-inverse {
  background: rgb(255 255 255 / 0.14);
  box-shadow: none;
}

.login-brand-name {
  font-size: 15px;
  font-weight: 700;
}

.login-brand-sub {
  margin-top: 1px;
  color: rgb(255 255 255 / 0.55);
  font-size: 11px;
}

.login-hero {
  max-width: 420px;
}

.login-headline {
  color: #fff;
  font-size: 40px;
  font-weight: 780;
  letter-spacing: -0.03em;
  line-height: 1.12;
}

.login-sub {
  margin-top: 14px;
  color: rgb(255 255 255 / 0.62);
  font-size: 14px;
}

.login-points {
  display: flex;
  flex-direction: column;
  gap: 14px;
  margin-top: 38px;
}

.login-points li {
  display: flex;
  align-items: center;
  gap: 12px;
  color: rgb(255 255 255 / 0.8);
  font-size: 13px;
}

.login-point-icon {
  display: grid;
  flex: 0 0 auto;
  place-items: center;
  width: 34px;
  height: 34px;
  border: 1px solid rgb(255 255 255 / 0.12);
  border-radius: 10px;
  background: rgb(255 255 255 / 0.07);
  color: #c9caff;
}

.login-point-icon :deep(svg) {
  width: 16px;
  height: 16px;
}

.login-aside-foot {
  color: rgb(255 255 255 / 0.3);
  font-size: 11px;
  letter-spacing: 0.14em;
  text-transform: uppercase;
}

.login-main {
  display: grid;
  flex: 1 1 auto;
  place-items: center;
  padding: 28px;
}

.login-card {
  width: 100%;
  max-width: 404px;
  padding: 30px;
  border: 1px solid var(--line);
  border-radius: 22px;
  background: var(--surface);
  box-shadow: var(--shadow-lg);
}

.login-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 24px;
}

.login-card-kicker {
  color: var(--ink-3);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.12em;
  text-transform: uppercase;
}

.login-title {
  color: var(--ink);
  font-size: 24px;
  font-weight: 750;
  letter-spacing: -0.02em;
}

.login-hint {
  margin-top: 6px;
  color: var(--ink-3);
  font-size: 12.5px;
}

.field {
  display: block;
  margin-top: 22px;
}

.field-label {
  display: block;
  margin-bottom: 6px;
  color: var(--ink-2);
  font-size: 11.5px;
  font-weight: 650;
}

.field-input {
  width: 100%;
  padding: 11px 13px;
  border: 1px solid var(--line);
  border-radius: 11px;
  background: var(--surface);
  color: var(--ink);
  transition:
    border-color 0.15s,
    box-shadow 0.15s;
}

.field-input:focus {
  border-color: var(--accent);
  outline: none;
  box-shadow: 0 0 0 3px rgb(91 91 224 / 0.13);
}

.primary-btn {
  width: 100%;
  margin-top: 14px;
  padding: 11px 14px;
  border-radius: 11px;
  background: linear-gradient(135deg, var(--accent), #7c6cf6);
  color: #fff;
  font-size: 13.5px;
  font-weight: 650;
  transition:
    filter 0.15s,
    transform 0.15s;
}

.primary-btn:hover {
  filter: brightness(1.05);
}

.primary-btn:active {
  transform: translateY(1px);
}

.primary-btn:disabled {
  opacity: 0.6;
}

.login-error {
  margin-top: 14px;
  color: var(--rose);
  font-size: 12.5px;
}

@media (max-width: 900px) {
  /* 登录页：品牌条 + 登录卡 */
  .login-shell {
    flex-direction: column;
  }

  .login-aside {
    flex: 0 0 auto;
    max-width: none;
    padding: calc(20px + env(safe-area-inset-top, 0px)) 20px 20px;
  }

  .login-hero,
  .login-aside-foot {
    display: none;
  }

  .login-main {
    flex: 1 1 auto;
    align-items: flex-start;
    padding: 18px 16px calc(24px + env(safe-area-inset-bottom, 0px));
  }

  .login-card {
    max-width: none;
    margin-top: 6px;
    padding: 24px;
    border-radius: 18px;
    box-shadow: var(--shadow-md);
  }
}

@media (max-width: 560px) {
  .login-card {
    padding: 20px;
  }

  .login-headline {
    font-size: 30px;
  }
}
</style>
