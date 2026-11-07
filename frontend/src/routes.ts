/**
 * 视图与地址栏的映射：全部 `/`、收藏 `/favorites`、未分组 `/ungrouped`、分组 `/<分组名>`。
 *
 * 分组名是用户输入，可能含中文、空格、`#`、`?` 等字符，生成路径时一律走
 * encodeURIComponent，还原时走 decodeURIComponent；解不开（非法百分号转义）
 * 或多段的路径返回 null，由调用方回退到「全部」。
 *
 * favorites / ungrouped 是保留的一级路径，服务端创建分组时也会拒绝这两个名字，
 * 所以它们不会和分组重名。
 */

/** 当前视图：全部 / 收藏 / 未分组 / 单个分组。 */
export type View =
  | { kind: "all" }
  | { kind: "favorites" }
  | { kind: "ungrouped" }
  | { kind: "group"; group: string };

/** 视图对应的地址栏路径（分组名已转义）。 */
export function viewPath(view: View): string {
  switch (view.kind) {
    case "favorites":
      return "/favorites";
    case "ungrouped":
      return "/ungrouped";
    case "group":
      return `/${encodeURIComponent(view.group)}`;
    default:
      return "/";
  }
}

/** 把地址栏路径还原成视图；无法解析时返回 null。 */
export function viewFromPath(pathname: string): View | null {
  if (pathname === "/") return { kind: "all" };
  if (!pathname.startsWith("/")) return null;
  const segment = pathname.slice(1);
  if (segment === "" || segment.includes("/")) return null;
  let name: string;
  try {
    name = decodeURIComponent(segment);
  } catch {
    return null;
  }
  if (name === "favorites") return { kind: "favorites" };
  if (name === "ungrouped") return { kind: "ungrouped" };
  return { kind: "group", group: name };
}
