/**
 * 展示用的格式化：文件大小与时间。
 *
 * 时间只显示 `MM-DD HH:mm`（与旧界面一致），年份不展示；拿不到时间时走 i18n。
 */
import { t } from "./i18n";

const SIZE_UNITS = ["B", "KB", "MB", "GB", "TB"] as const;
const BASE = 1024;

/** 1234567 → "1.18 MB"；0 与非法值 → "0 B"。 */
export function formatSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 1) return "0 B";
  const unit = Math.min(Math.floor(Math.log(bytes) / Math.log(BASE)), SIZE_UNITS.length - 1);
  const value = bytes / BASE ** unit;
  return `${Number.parseFloat(value.toFixed(2))} ${SIZE_UNITS[unit]}`;
}

/** unix 秒 → "MM-DD HH:mm"（本地时区）。 */
export function formatTime(timestamp: number): string {
  if (!Number.isFinite(timestamp) || timestamp <= 0) return t("meta.unknown-time");
  const date = new Date(timestamp * 1000);
  const month = date.getMonth() + 1;
  const day = date.getDate();
  const hours = String(date.getHours()).padStart(2, "0");
  const minutes = String(date.getMinutes()).padStart(2, "0");
  return `${month}-${day} ${hours}:${minutes}`;
}
