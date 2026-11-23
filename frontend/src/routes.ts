/**
 * 视图与地址栏的映射：最近 `/`、收藏 `/favorites`、文件夹 `/<任意深度路径>`。
 *
 * 目录深度不限制（SMB 那边怎么组织就怎么展示）：路径的每一段都是文件系统里的名字，
 * 可能含中文、空格、`#`、`?` 等字符；生成路径时逐段 encodeURIComponent，
 * 还原时逐段 decodeURIComponent；解不开（非法百分号转义）返回 null，由调用方回退到「最近」。
 *
 * `favorites` 是保留的一级路径：只读视图不能改数据目录，顶级目录恰好叫这个名字时
 * 会被当成收藏视图（可在 README 的已知限制里注明）。
 */

/** 当前视图：最近 / 收藏 / 单个文件夹（任意深度路径）。 */
export type View = { kind: "recent" } | { kind: "favorites" } | { kind: "group"; group: string };

/** 视图对应的地址栏路径（分组路径已逐段转义）。 */
export function viewPath(view: View): string {
  switch (view.kind) {
    case "favorites":
      return "/favorites";
    case "group":
      return `/${view.group.split("/").map(encodeURIComponent).join("/")}`;
    default:
      return "/";
  }
}

/** 把地址栏路径还原成视图；无法解析时返回 null。 */
export function viewFromPath(pathname: string): View | null {
  if (pathname === "/" || pathname === "") return { kind: "recent" };
  if (!pathname.startsWith("/")) return null;
  const segments = pathname.slice(1).split("/");
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
  return { kind: "group", group: path };
}