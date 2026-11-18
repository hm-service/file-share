<script setup lang="ts">
/**
 * 一个可拖拽排序的文件列表（渲染成一张 list-card）。
 *
 * 排序用 SortableJS：只有行首的 .drag-handle 能发起拖拽，触屏上有 120ms 长按延迟，
 * 行内按钮不参与。onEnd 时从 DOM 读当前顺序的 id 交给父组件（乐观更新 + PUT），
 * 组件本身不改数据、不发请求，方便失败时由父组件统一回滚。
 *
 * 分组视图里每个段一张列表；带 sectionKey 的列表加入同一个 Sortable group，文件可以直接
 * 拖进另一个段的行之间——松手位置就是它在目标分组里的顺序。跨段落定时把目标段和来源段
 * 的新顺序一起交给父组件（cross-drop），由父组件提交「改分组 + 两段排序」。
 */
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import Sortable from "sortablejs";

import FileRow from "@/components/FileRow.vue";
import type { FileItem, GroupItem } from "@/types";

const props = defineProps<{
  files: FileItem[];
  /** 全部分组，透传给行内「移动到分组」菜单。 */
  groups: readonly GroupItem[];
  /** 搜索过滤时 DOM 只是子集，提交顺序会打乱整段，所以直接禁用拖拽。 */
  sortable: boolean;
  /** 行内额外显示文件所属分组（「最近」视图里看不出分组，需要它）。 */
  showGroup?: boolean;
  /** 分段视图里本段的标识（沿用段 key）：有值时各段之间可以跨列表拖拽、调整落点位置。 */
  sectionKey?: string;
}>();

const emit = defineEmits<{
  favorite: [file: FileItem];
  download: [file: FileItem];
  rename: [file: FileItem];
  /** 行内操作（如签分享链接）失败，交给父组件统一提示。 */
  error: [error: unknown];
  delete: [file: FileItem];
  "move-group": [file: FileItem, groupName: string];
  /** 「移动到分组」菜单里的新建分组，交给父组件统一弹输入框。 */
  "new-group": [file: FileItem];
  reorder: [orderedIds: string[]];
  /** 拖进另一段落定：两段在 DOM 里的新顺序一起交出去（含被拖走的文件已从来源段移除）。 */
  "cross-drop": [
    payload: {
      fileId: string;
      toKey: string;
      toOrder: string[];
      fromOrder: string[];
    },
  ];
  "drag-start": [file: FileItem];
  "drag-end": [];
}>();

const listEl = ref<HTMLElement | null>(null);
let instance: Sortable | null = null;

/** 从列表 DOM 读当前顺序：Sortable 已经把节点挪好了，data-id 是稳定标识。 */
function orderOf(element: Element | null): string[] {
  if (!element) return [];
  return Array.from(element.querySelectorAll<HTMLElement>(".file-row"))
    .map((row) => row.dataset.id ?? "")
    .filter((id) => id.length > 0);
}

/** 本列表当前顺序（段内排序提交用）。 */
function currentOrder(): string[] {
  return orderOf(listEl.value);
}

function mountSortable(): void {
  if (instance || !props.sortable || !listEl.value) return;
  instance = Sortable.create(listEl.value, {
    animation: 160,
    handle: ".drag-handle",
    // 分段列表共用同一个拖拽组：别的段的行能落进来，落点就是它在目标分组里的位置。
    // 不分段的列表（最近 / 收藏 / 未分组的单张卡片）不带 sectionKey，不参与跨列表拖拽。
    ...(props.sectionKey ? { group: "file-sections" } : {}),
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
      const from = listEl.value;
      const to = event.to as HTMLElement | null;
      const fileId = (event.item as HTMLElement | undefined)?.dataset.id ?? "";
      // 拖到别的段：落点位置在目标段的 DOM 里，两段顺序一起交给父组件提交。
      // 注意顺序：cross-drop 要在 drag-end 之前发，父组件靠「拖拽状态是否已被消费」
      // 区分这次松手是段内位置调整，还是已经按「移到分组末尾」处理过了。
      if (from && to && to !== from && props.sectionKey && fileId) {
        emit("cross-drop", {
          fileId,
          toKey: to.dataset.sectionKey ?? "",
          toOrder: orderOf(to),
          fromOrder: orderOf(from),
        });
      } else if (event.oldIndex !== event.newIndex) {
        const orderedIds = currentOrder();
        if (orderedIds.length > 1) emit("reorder", orderedIds);
      }
      emit("drag-end");
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
  <ul
    ref="listEl"
    class="list-card"
    :class="{ 'is-sortable': sortable, 'is-cross-list': sectionKey }"
    :data-section-key="sectionKey"
  >
    <FileRow
      v-for="file in files"
      :key="file.id"
      :file="file"
      :groups="groups"
      :draggable="sortable"
      :show-group="showGroup"
      @favorite="emit('favorite', file)"
      @download="emit('download', file)"
      @rename="emit('rename', file)"
      @error="emit('error', $event)"
      @delete="emit('delete', file)"
      @move-group="(groupName) => emit('move-group', file, groupName)"
      @new-group="emit('new-group', file)"
    />
  </ul>
</template>

<style scoped>
.list-card {
  overflow: hidden;
  border: 1px solid var(--line);
  border-radius: 18px;
  background: var(--surface);
  box-shadow: var(--shadow-md);
  container-type: inline-size;
}

/*
 * 分段列表在首行上方 / 末行下方各留一条窄留白：跨段拖拽时指针落进留白就是
 * 「放到这一段的最前面 / 最后面」。没有留白时，从更上面的段往下拖永远插不到第 1 位
 * （SortableJS 里元素还没进入列表时只能落在悬停行之后）。
 */
.list-card.is-cross-list {
  padding: 5px 0;
}

@media (max-width: 900px) {
  .list-card {
    border-radius: 16px;
  }
}
</style>
