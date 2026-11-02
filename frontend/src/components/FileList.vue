<script setup lang="ts">
/**
 * 一个可拖拽排序的文件列表（渲染成一张 list-card）。
 *
 * 排序用 SortableJS：只有行首的 .drag-handle 能发起拖拽，触屏上有 120ms 长按延迟，
 * 行内按钮不参与。onEnd 时从 DOM 读当前顺序的 id 交给父组件（乐观更新 + PUT），
 * 组件本身不改数据、不发请求，方便失败时由父组件统一回滚。
 */
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import Sortable from "sortablejs";

import FileRow from "@/components/FileRow.vue";
import type { FileItem } from "@/types";

const props = defineProps<{
  files: FileItem[];
  /** 全部分组名，透传给行内「移动到分组」菜单。 */
  groups: readonly string[];
  /** 搜索过滤时 DOM 只是子集，提交顺序会打乱整段，所以直接禁用拖拽。 */
  sortable: boolean;
}>();

const emit = defineEmits<{
  favorite: [file: FileItem];
  download: [file: FileItem];
  rename: [file: FileItem];
  visibility: [file: FileItem];
  delete: [file: FileItem];
  "move-group": [file: FileItem, groupName: string];
  reorder: [orderedIds: string[]];
  "drag-start": [file: FileItem];
  "drag-end": [];
}>();

const listEl = ref<HTMLElement | null>(null);
let instance: Sortable | null = null;

/** 从 DOM 读当前顺序：Sortable 已经把节点挪好了，data-id 是稳定标识。 */
function currentOrder(): string[] {
  const element = listEl.value;
  if (!element) return [];
  return Array.from(element.querySelectorAll<HTMLElement>(".file-row"))
    .map((row) => row.dataset.id ?? "")
    .filter((id) => id.length > 0);
}

function mountSortable(): void {
  if (instance || !props.sortable || !listEl.value) return;
  instance = Sortable.create(listEl.value, {
    animation: 160,
    handle: ".drag-handle",
    // 按钮不参与拖拽：即使从按钮上按下也不开始排序
    filter: "button",
    preventOnFilter: false,
    // 触屏上按住 120ms 才开始拖，避免和纵向滚动冲突；鼠标/触控板立即开始
    delay: 120,
    delayOnTouchOnly: true,
    touchStartThreshold: 4,
    ghostClass: "drag-ghost",
    chosenClass: "drag-chosen",
    dragClass: "drag-dragging",
    // SortableJS 的 onStart 在原生和降级模式下都会触发；把当前文件交给父组件，
    // 父组件据此让左栏分组标签成为可投放目标（不依赖 dataTransfer 类型）。
    onStart: (event) => {
      const id = (event.item as HTMLElement).dataset.id;
      const file = props.files.find((entry) => entry.id === id);
      if (file) emit("drag-start", file);
    },
    onEnd: (event) => {
      emit("drag-end");
      if (event.oldIndex === event.newIndex) return;
      const orderedIds = currentOrder();
      if (orderedIds.length > 1) emit("reorder", orderedIds);
    },
  });
}

function destroySortable(): void {
  instance?.destroy();
  instance = null;
}

onMounted(mountSortable);
watch(
  () => props.sortable,
  (enabled) => {
    if (enabled) mountSortable();
    else destroySortable();
  },
);
onBeforeUnmount(destroySortable);
</script>

<template>
  <ul ref="listEl" class="list-card" :class="{ 'is-sortable': sortable }">
    <FileRow
      v-for="file in files"
      :key="file.id"
      :file="file"
      :groups="groups"
      :draggable="sortable"
      @favorite="emit('favorite', file)"
      @download="emit('download', file)"
      @rename="emit('rename', file)"
      @visibility="emit('visibility', file)"
      @delete="emit('delete', file)"
      @move-group="(groupName) => emit('move-group', file, groupName)"
    />
  </ul>
</template>
