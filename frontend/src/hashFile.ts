/**
 * File → SHA-256 十六进制，按 4 MiB 分块读、分块喂给增量 hasher。
 *
 * 每块用 `await blob.slice(...).arrayBuffer()`：读盘是异步的，每块之间会让出主线程，
 * 所以大文件计算指纹时界面不会冻住，进度回调也来得及渲染。
 *
 * 可选 AbortSignal：每块开始前（以及 await 读盘回来、写进 hasher 之前）检查一次，
 * 已取消就抛 AbortError。arrayBuffer 本身不可中断，所以取消最多延迟一块的读盘时间。
 */
import { abortError } from "./abort";
import { Sha256 } from "./sha256";

/** 分块大小：4 MiB。大文件不会一次性进内存。 */
export const HASH_CHUNK_SIZE = 4 * 1024 * 1024;

export async function hashFile(
  file: Blob,
  onProgress?: (percent: number) => void,
  signal?: AbortSignal,
): Promise<string> {
  const throwIfAborted = (): void => {
    if (signal?.aborted) throw abortError();
  };

  throwIfAborted();
  const hasher = new Sha256();
  const total = file.size;
  if (total === 0) {
    onProgress?.(100);
    return hasher.hex();
  }

  const chunks = Math.ceil(total / HASH_CHUNK_SIZE);
  for (let index = 0; index < chunks; index += 1) {
    throwIfAborted();
    const start = index * HASH_CHUNK_SIZE;
    const end = Math.min(start + HASH_CHUNK_SIZE, total);
    const buffer = await file.slice(start, end).arrayBuffer();
    throwIfAborted();
    hasher.update(new Uint8Array(buffer));
    onProgress?.(Math.round(((index + 1) / chunks) * 100));
  }
  throwIfAborted();
  return hasher.hex();
}
