/**
 * 拖放相关的小工具：整页拖放、文件夹树、表格行三处共用同一份实现。
 *
 * 内部文件拖拽走 SortableJS（mousedown 系），dataTransfer.types 里没有 "Files"；
 * 从桌面拖进来的文件才有。用这一个判断就能区分「移动文件」与「上传外部文件」。
 */
export function hasFiles(event: DragEvent): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes("Files");
}