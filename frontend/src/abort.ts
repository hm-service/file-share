/**
 * 取消语义的统一表示。
 *
 * AbortSignal 中断（hashFile / fetch）与 XMLHttpRequest.abort() 都会让调用方拿到
 * 一个 AbortError。上传队列需要把它和真正的失败区分开：取消不是 failed。
 * 这里只放两个极小的工具，避免 hashFile 反过来依赖 api 模块。
 */

/** 取消时抛出的标准错误：DOMException，name 为 "AbortError"。 */
export function abortError(): DOMException {
  return new DOMException("The operation was aborted.", "AbortError");
}

/** 判断异常是不是「取消」而不是「失败」（跨 realm 时 name 判断最稳）。 */
export function isAbortError(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    (error as { name?: unknown }).name === "AbortError"
  );
}
