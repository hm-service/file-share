/**
 * 视图与地址栏的映射：最近 `/`、收藏 `/favorites`、网盘 `/drive`、未分组 `/ungrouped`、
 * 分组 `/<顶级>` 或 `/<顶级>/<二级>`。
 *
 * 分组路径的每一段都是用户输入，可能含中文、空格、`#`、`?` 等字符：
 * 生成路径时逐段 encodeURIComponent，还原时逐段 decodeURIComponent；
 * 解不开（非法百分号转义）或超过两段的路径返回 null，由调用方回退到「最近」。
 *
 * favorites / ungrouped / drive 是保留的一级路径，服务端创建分组时也会拒绝这些名字，
 * 所以它们不会和分组重名。
 */

/** 当前视图：最近 / 收藏 / 网盘 / 未分组 / 单个分组。 */
export type View =
  | { kind: "recent" }
  | { kind: "favorites" }
  | { kind: "drive" }
  | { kind: "ungrouped" }
  | { kind: "group"; group: string };

/** 视图对应的地址栏路径（分组路径已逐段转义）。 */
export function viewPath(view: View): string {
  switch (view.kind) {
    case "favorites":
      return "/favorites";
    case "drive":
      return "/drive";
    case "ungrouped":
      return "/ungrouped";
    case "group":
      return `/${view.group.split("/").map(encodeURIComponent).join("/")}`;
    default:
      return "/";
  }
}

/** 把地址栏路径还原成视图；无法解析时返回 null。 */
export function viewFromPath(pathname: string): View | null {
  if (pathname === "/") return { kind: "recent" };
  if (!pathname.startsWith("/")) return null;
  const segments = pathname.slice(1).split("/");
  if (segments.length > 2) return null;
  const decoded: string[] = [];
  for (const segment of segments) {
    if (segment === "") return null;
    try {
      decoded.push(decodeURIComponent(segment));
    } catch {
      return null;
    }
  }
  const path = decoded.join("/");
  if (path === "favorites") return { kind: "favorites" };
  if (path === "drive") return { kind: "drive" };
  if (path === "ungrouped") return { kind: "ungrouped" };
  return { kind: "group", group: path };
}