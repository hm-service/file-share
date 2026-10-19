/**
 * 登录页（/auth）的入口：已有有效会话就直接回主页面。
 */
import { createApp } from "vue";

import LoginPage from "@/LoginPage.vue";
import { applyDocumentLocale } from "@/i18n";
import { getSession } from "@/session";
import "@/style.css";

if (getSession()) {
  // 已有会话直接回主页面；失效的会话会在主页面收到 401 再被送回来
  location.replace("/");
} else {
  applyDocumentLocale("app.title");
  createApp(LoginPage).mount("#app");
}
