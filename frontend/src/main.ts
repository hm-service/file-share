/**
 * 主页面（/）的入口：装配根组件、挂调试句柄。
 *
 * 没有会话就直接去 /auth；调试句柄先挂上，方便在浏览器控制台/验收脚本里
 * 调用 api、i18n、session、hashFile（即使随后被重定向也不影响）。
 */
import { createApp } from "vue";

import App from "@/App.vue";
import * as api from "@/api";
import { hashFile } from "@/hashFile";
import * as i18n from "@/i18n";
import { selfCheck } from "@/sha256";
import * as session from "@/session";
import "@/style.css";

declare global {
  interface Window {
    __app: {
      api: typeof api;
      i18n: typeof i18n;
      session: typeof session;
      hashFile: typeof hashFile;
    };
  }
}

window.__app = { api, i18n, session, hashFile };

// 开发时自检手写的 SHA-256：向量不过直接抛错，别带着错指纹去联调
if (import.meta.env.DEV) selfCheck();

i18n.applyDocumentLocale("app.title");
if (session.requireSession()) {
  createApp(App).mount("#app");
}
