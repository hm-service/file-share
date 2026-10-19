/**
 * File → SHA-256 十六进制，按 4 MiB 分块读、分块喂给增量 hasher。
 *
 * 每块用 `await blob.slice(...).arrayBuffer()`：读盘是异步的，每块之间会让出主线程，
 * 所以大文件计算指纹时界面不会冻住，进度回调也来得及渲染。
 */
import { Sha256 } from "./sha256";

/** 分块大小：4 MiB。大文件不会一次性进内存。 */
export const HASH_CHUNK_SIZE = 4 * 1024 * 1024;

export async function hashFile(
  file: Blob,
  onProgress?: (percent: number) => void,
): Promise<string> {
  const hasher = new Sha256();
  const total = file.size;
  if (total === 0) {
    onProgress?.(100);
    return hasher.hex();
  }

  const chunks = Math.ceil(total / HASH_CHUNK_SIZE);
  for (let index = 0; index < chunks; index += 1) {
    const start = index * HASH_CHUNK_SIZE;
    const end = Math.min(start + HASH_CHUNK_SIZE, total);
    const buffer = await file.slice(start, end).arrayBuffer();
    hasher.update(new Uint8Array(buffer));
    onProgress?.(Math.round(((index + 1) / chunks) * 100));
  }
  return hasher.hex();
}
