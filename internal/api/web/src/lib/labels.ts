import type { Relative } from "@/lib/format";

/**
 * 把纯数据翻译成人类措辞。
 *
 * 这里刻意只做「选 key + 填参数」，所有文字都留在 i18n.json：
 * 一旦在代码里拼中文，每加一个语种就要改一次逻辑。
 */
export type Translate = (key: string, values?: Record<string, string | number>) => string;

/** 过去时刻：「3 分钟前」「刚刚」「从未」 */
export function describePast(rel: Relative | null, t: Translate): string {
  if (!rel) return t("time.never");
  if (rel.unit === "second" && rel.value < 5) return t("time.justNow");
  return t(`time.ago.${rel.unit}`, { count: rel.value });
}

/** 未来时刻：「4 小时后」「即将」 */
export function describeFuture(rel: Relative | null, t: Translate): string {
  if (!rel) return t("time.notScheduled");
  if (rel.unit === "second" && rel.value < 5) return t("time.inMoments");
  return t(`time.in.${rel.unit}`, { count: rel.value });
}

/** 运行时长用固定英文单位（12d 6h）——它是技术读数，各语种下都更好扫读。 */
export function describeElapsed(seconds: number, t: Translate): string {
  const total = Math.max(0, Math.floor(seconds));
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  if (days > 0) return t("time.daysHours", { days, hours });
  if (hours > 0) return t("time.hoursMinutes", { hours, minutes });
  if (minutes > 0) return t("time.minutesSeconds", { minutes, seconds: total % 60 });
  return t("time.secondsOnly", { seconds: total });
}

const KNOWN_PLATFORMS = new Set(["ios", "ipados", "tvos", "macos", "watchos", "visionos"]);

/** 设备平台。服务端只会存白名单里的取值，认不出来的一律显示「未知设备」。 */
export function describePlatform(platform: string | undefined, t: Translate): string {
  if (platform && KNOWN_PLATFORMS.has(platform)) return t(`platform.${platform}`);
  return t("platform.unknown");
}

/** 健康检查项的名称。 */
export function describeCheck(id: string, t: Translate): string {
  return t(`health.check.${id}`);
}

/**
 * 健康检查未通过的原因。
 * 后端下发的是机器可读短码（unreadable / unset / never …），不是句子。
 */
const KNOWN_REASONS = new Set([
  "unreadable",
  "unset",
  "empty",
  "none",
  "over",
  "never",
  "running",
  "cancelled",
  "errors",
  "incomplete",
]);

export function describeReason(reason: string | undefined, t: Translate): string {
  if (!reason || !KNOWN_REASONS.has(reason)) return "";
  return t(`health.reason.${reason}`);
}

/** 检查项的取值描述：计数类用 count，文本类用 text。 */
export function describeCheckValue(
  check: { count?: number; text?: string },
  t: Translate
): string {
  if (typeof check.count === "number") return t("health.count", { count: check.count });
  if (check.text) return check.text;
  return "";
}

/** 扫描收尾状态的措辞，对应 Scanner.Progress.Message 的几种取值。 */
export function describeScanMessage(message: string | undefined, cancelled: boolean, t: Translate): string {
  if (cancelled) return t("scan.message.cancelled");
  switch (message) {
    case "scan finished":
      return t("scan.message.finished");
    case "scan incomplete; existing entries retained":
      return t("scan.message.incomplete");
    case "no libraries configured":
      return t("scan.message.noLibraries");
    case "":
    case undefined:
      return "";
    default:
      return message;
  }
}

/**
 * 扫描收尾回执的色调。
 * 完成是 ok；中止 / 不完整是 warn；未配置媒体库或其它 failScan 原因是 danger。
 */
export function scanMessageTone(
  message: string | undefined,
  cancelled: boolean
): "ok" | "warn" | "danger" | "tech" {
  if (cancelled) return "warn";
  switch (message) {
    case "scan finished":
      return "ok";
    case "scan incomplete; existing entries retained":
      return "warn";
    case "":
    case undefined:
      return "tech";
    default:
      return "danger";
  }
}

/** 扫描模式（incremental / full）。 */
export function describeScanMode(mode: string | undefined, t: Translate): string {
  if (mode === "full") return t("scan.mode.full");
  if (mode === "incremental") return t("scan.mode.incremental");
  return mode || "—";
}

/** 日志级别短码 → 展示用标签。级别名保持英文常量，不翻译。 */
export function describeLogLevel(level: string): string {
  return level.toUpperCase();
}

/**
 * 索引里的条目类别。键名对应 internal/media/probe.go 的 Kind* 常量。
 *
 * 未知类别一律原样显示而不是丢弃：聚合接口的 byKind 是按实际扫到的内容分组的，
 * 前端硬编码「照片/视频/RAW」三类会把动态照片与实况片段静默漏掉——
 * 口径一漏，KPI 的总数就和明细对不上（515 vs 304）。
 */
const KIND_KEYS: Record<string, string> = {
  image: "index.images",
  video: "index.videos",
  raw: "index.raw",
  motion: "index.motion",
  "motion-clip": "index.motionClip",
};

export function describeKind(kind: string, t: Translate): string {
  const key = KIND_KEYS[kind];
  return key ? t(key) : kind;
}
