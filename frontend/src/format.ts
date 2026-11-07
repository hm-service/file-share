/**
 * 展示用的格式化：文件大小、时间与日期分段标题。
 *
 * 文件时间只显示 `MM-DD HH:mm`，年份交给「全部」视图的日期分段标题展示。
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

/** 字节/秒 → "1.18 MB/s"；0 或非法值 → 空串（不显示速度）。 */
export function formatSpeed(bytesPerSecond: number): string {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0) return "";
  return `${formatSize(bytesPerSecond)}/s`;
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

/** unix 秒 → 日期分段标题：中文「2026年11月7号」，英文「November 7, 2026」（本地时区）。 */
export function formatDayHeading(timestamp: number): string {
  const date = new Date(timestamp * 1000);
  return t("date.group-heading", {
    year: date.getFullYear(),
    month: date.getMonth() + 1,
    day: date.getDate(),
    monthName: date.toLocaleDateString("en-US", { month: "long" }),
  });
}
