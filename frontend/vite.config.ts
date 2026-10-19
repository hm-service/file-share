import { fileURLToPath } from "node:url";

import vue from "@vitejs/plugin-vue";
import { defineConfig, type Plugin } from "vite";

const entry = (name: string) => fileURLToPath(new URL(`./${name}`, import.meta.url));

// 后端默认在 8080；开发时前端跑 Vite，接口仍打 Go（just run 起 8080）。
const BACKEND = "http://127.0.0.1:8080";

/** 产物里认证页是 auth.html，线上由后端把 /auth 映射过去；开发时改写成 auth.html 保持一致。 */
function authPage(): Plugin {
  return {
    name: "file-share-auth-page",
    configureServer(server) {
      server.middlewares.use((request, _response, next) => {
        if (request.url === "/auth" || request.url?.startsWith("/auth?")) {
          const query = request.url.includes("?") ? request.url.slice(request.url.indexOf("?")) : "";
          request.url = `/auth.html${query}`;
        }
        next();
      });
    },
  };
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue(), authPage()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  build: {
    outDir: "dist",
    rollupOptions: {
      // 两个入口：主页面 index.html + 登录页 auth.html（URL 映射由根目录 web.go 决定）
      input: {
        index: entry("index.html"),
        auth: entry("auth.html"),
      },
    },
  },
  server: {
    proxy: Object.fromEntries(
      ["/api", "/s", "/livez", "/readyz"].map((path) => [path, BACKEND] as const),
    ),
  },
});
