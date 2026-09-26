/**
 * 展示层的格式化工具。
 *
 * 原则：这里只做**纯数据**转换，不产出人类语言。相对时间这类需要措辞的，
 * 一律返回 { value, unit } 交给 i18n 组合——否则一次翻译要改代码。
 */

export function formatNumber(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  return new Intl.NumberFormat().format(value);
}

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB", "PB"] as const;

export function formatBytes(value: number | null | undefined): { value: string; unit: string } | null {
  if (value === null || value === undefined || Number.isNaN(value)) return null;
  if (value < 0) return null;
  let size = value;
  let index = 0;
  while (size >= 1024 && index < BYTE_UNITS.length - 1) {
    size /= 1024;
    index += 1;
  }
  const rounded = index === 0 ? String(size) : String(Math.round(size * 10) / 10);
  return { value: rounded, unit: BYTE_UNITS[index] };
}

/** 运行时长：12d 6h / 6h 12m / 12m 30s / 45s */
export function formatUptime(seconds: number | null | undefined): string {
  const total = Math.max(0, Math.floor(seconds ?? 0));
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  if (days > 0) return `${days}d ${hours}h`;
  if (hours > 0) return `${hours}h ${minutes}m`;
  if (minutes > 0) return `${minutes}m ${total % 60}s`;
  return `${total}s`;
}

/** mm:ss，用于扫描已用时与配对倒计时。 */
export function formatClock(seconds: number | null | undefined): string {
  const total = Math.max(0, Math.floor(seconds ?? 0));
  const minutes = Math.floor(total / 60);
  const rest = total % 60;
  return `${String(minutes).padStart(2, "0")}:${String(rest).padStart(2, "0")}`;
}

export type RelativeUnit = "second" | "minute" | "hour" | "day" | "month" | "year";

export interface Relative {
  value: number;
  unit: RelativeUnit;
}

/** 过去时间点的相对距离；无法解析时返回 null（由调用方决定显示什么）。 */
export function relativeSince(iso: string | null | undefined, nowMs = Date.now()): Relative | null {
  const elapsed = elapsedSeconds(iso, nowMs);
  if (elapsed === null) return null;
  return bucket(elapsed);
}

/** 未来时间点的相对距离，用于「下次计划扫描」这类倒计时。 */
export function relativeUntil(iso: string | null | undefined, nowMs = Date.now()): Relative | null {
  if (!iso) return null;
  const target = Date.parse(iso);
  if (Number.isNaN(target)) return null;
  return bucket(Math.max(0, (target - nowMs) / 1000));
}

function elapsedSeconds(iso: string | null | undefined, nowMs: number): number | null {
  if (!iso) return null;
  const parsed = Date.parse(iso);
  if (Number.isNaN(parsed)) return null;
  // Go 的零值 time.Time 会序列化成 0001-01-01，那不是「很久以前」而是「从未发生」。
  if (parsed <= 0) return null;
  return Math.max(0, (nowMs - parsed) / 1000);
}

function bucket(seconds: number): Relative {
  if (seconds < 60) return { value: Math.floor(seconds), unit: "second" };
  if (seconds < 3600) return { value: Math.floor(seconds / 60), unit: "minute" };
  if (seconds < 86400) return { value: Math.floor(seconds / 3600), unit: "hour" };
  if (seconds < 2592000) return { value: Math.floor(seconds / 86400), unit: "day" };
  if (seconds < 31536000) return { value: Math.floor(seconds / 2592000), unit: "month" };
  return { value: Math.floor(seconds / 31536000), unit: "year" };
}

/** 是否为 Go 的零值时间戳（从未发生）。 */
export function isZeroTime(iso: string | null | undefined): boolean {
  if (!iso) return true;
  const parsed = Date.parse(iso);
  return Number.isNaN(parsed) || parsed <= 0;
}

export interface ParsedLog {
  raw: string;
  time?: string;
  level?: string;
  msg: string;
  fields: { key: string; value: string }[];
}

/** slog 自己占用的字段，其余一律按「结构化附加字段」展示。 */
const LOG_RESERVED = new Set(["time", "level", "msg", "source"]);

/**
 * 把一行 slog JSON 拆成可渲染的结构。
 *
 * Ring 里存的是 slog 的 JSON 输出，直接铺在页面上没法读；但解析失败时**不丢弃**，
 * 而是原样显示——万一有非结构化输出（例如栈回溯），静默吞掉比难看更糟。
 */
export function parseLogLine(raw: string): ParsedLog {
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return { raw, msg: raw, fields: [] };
    }
    const record = parsed as Record<string, unknown>;
    const fields = Object.entries(record)
      .filter(([key]) => !LOG_RESERVED.has(key))
      .map(([key, value]) => ({ key, value: stringifyField(value) }));
    return {
      raw,
      time: typeof record.time === "string" ? record.time : undefined,
      level: typeof record.level === "string" ? record.level.toUpperCase() : undefined,
      msg: typeof record.msg === "string" ? record.msg : "",
      fields,
    };
  } catch {
    return { raw, msg: raw, fields: [] };
  }
}

function stringifyField(value: unknown): string {
  if (value === null) return "null";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

/** HH:MM:SS，日志行左侧的时间列。 */
export function formatLogTime(iso: string | undefined): string {
  if (!iso) return "--:--:--";
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) return "--:--:--";
  return [parsed.getHours(), parsed.getMinutes(), parsed.getSeconds()]
    .map((part) => String(part).padStart(2, "0"))
    .join(":");
}

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime()) || parsed.getTime() <= 0) return "—";
  return parsed.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

/** HH:MM，用于「每日 03:00」这类计划时间。 */
export function formatHM(iso: string | null | undefined): string {
  if (!iso) return "—";
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) return "—";
  return `${String(parsed.getHours()).padStart(2, "0")}:${String(parsed.getMinutes()).padStart(2, "0")}`;
}
