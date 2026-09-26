"use client";

import useSWR, { type SWRResponse } from "swr";

/** 控制台读的聚合数据结构。 */
export interface ServiceInfo {
  name: string;
  version: string;
  apiVersion: number;
  serverId: string;
  uptimeSeconds: number;
  generatedAt: string;
}

export interface HealthCheck {
  id: string;
  ok: boolean;
  count?: number;
  text?: string;
  reason?: string;
}

export interface Health {
  checks: HealthCheck[];
  passed: number;
  total: number;
}

export interface Pairing {
  armed: boolean;
  pairedDevices: number;
  windowSeconds: number;
  codeLength: number;
  expiresAt?: string;
  expiresInSeconds?: number;
}

export interface Device {
  id: string;
  name: string;
  platform?: string;
  createdAt: string;
  lastSeen: string;
}

export interface Library {
  id: string;
  name: string;
  path: string;
  exists: boolean;
}

export interface ScanProgress {
  running: boolean;
  mode: string;
  libraries: number;
  foldersDone: number;
  filesSeen: number;
  mediaIndexed: number;
  reused: number;
  errors: number;
  startedAt: string;
  finishedAt?: string;
  currentPath?: string;
  message?: string;
  cancelled?: boolean;
}

export interface IndexInfo {
  entries: number;
  removed: number;
  byKind: Record<string, number>;
  lastScanAt: number;
  lastScanGen: number;
}

export interface ScheduleInfo {
  daily: string;
  nextRunAt?: string;
  nextRunInSeconds?: number;
}

export interface CacheInfo {
  thumbFiles: number;
  thumbBytes: number;
  thumbLimitBytes: number;
}

export interface SettingsShape {
  preset: string;
  thumbSize: number;
  thumbCacheLimitBytes: number;
  scanSchedule: string;
  logLevel: string;
  transcode: string;
  motionPhoto: boolean;
  rawPreview: boolean;
  concurrency?: number;
  libraries?: Library[];
  excludePatterns?: string[];
}

export interface VolumeInfo {
  containerPath: string;
  readable: boolean;
  ownerUid: number;
  ownerGid: number;
  mediaFolders?: string[];
  topLevelItems: number;
  error?: string;
}

/**
 * 控制台的聚合数据。
 *
 * 同一份类型服务两个接口：未登录时读公开的 /api/v1/dashboard（最小集），
 * 登录后读 /api/v1/admin/overview（含设备、健康判定、计划）。因此管理面字段
 * 一律可选——组件必须能在这两种形态下都不崩。
 */
export interface ConsoleData {
  service: ServiceInfo;
  pairing: Pairing;
  libraries: Library[];
  index: IndexInfo;
  scan: ScanProgress;
  cache: CacheInfo;
  tools: Record<string, boolean>;
  volumes?: VolumeInfo[];
  suggestedUser?: string;
  logs: string[];
  settings?: SettingsShape;
  hosts?: { scheme: string; host: string; port: string; loopback: boolean; candidates?: string[] };
  // 仅管理员可见
  devices?: Device[];
  health?: Health;
  schedule?: ScheduleInfo;
}

export interface AdminStatus {
  configured: boolean;
  authenticated: boolean;
  username?: string;
}

export class ApiError extends Error {
  status: number;
  code: string;

  constructor(message: string, status: number, code = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function fetcher<T>(url: string): Promise<T> {
  const response = await fetch(url, { headers: { Accept: "application/json" } });
  if (!response.ok) {
    throw new ApiError(`HTTP ${response.status}`, response.status);
  }
  return (await response.json()) as T;
}

export function useAdminStatus(): SWRResponse<AdminStatus, ApiError> {
  return useSWR<AdminStatus, ApiError>("/api/v1/admin/status", fetcher, {
    revalidateOnFocus: true,
  });
}

/**
 * 聚合数据的统一入口。
 *
 * 轮询节奏是刻意的：扫描中 1 秒，空闲 5 秒。后端的挂载探测与缩略图统计都带
 * 短 TTL 缓存，所以这个频率不会退化成每请求一次全目录遍历。
 */
export function useConsoleData(authenticated: boolean): SWRResponse<ConsoleData, ApiError> {
  const key = authenticated ? "/api/v1/admin/overview" : "/api/v1/dashboard";
  return useSWR<ConsoleData, ApiError>(key, fetcher, {
    // 扫描中 1 秒；刚点下扫描、状态还没切到 running 时也按 1 秒拉，
    // 否则启动瞬间会卡在 5 秒空窗里，界面上像没反应。
    refreshInterval: (data) => {
      if (data?.scan?.running) return 1000;
      const started = data?.scan?.startedAt ? Date.parse(data.scan.startedAt) : Number.NaN;
      if (!Number.isNaN(started) && Date.now() - started < 8000 && !data?.scan?.finishedAt) return 1000;
      return 5000;
    },
    revalidateOnFocus: true,
    keepPreviousData: true,
  });
}

export interface LogQuery {
  level?: string;
  q?: string;
  tail?: number;
}

/**
 * 日志。过滤在服务端做（?level / ?q）——Ring 里存着两千行，
 * 每次把全量传到浏览器再筛是浪费。
 * refreshMs 传 0 表示只按需刷新（日志页的「暂停」）。
 */
export function useLogs(
  authenticated: boolean,
  query: LogQuery,
  refreshMs = 5000
): SWRResponse<{ lines: string[] }, ApiError> {
  const params = new URLSearchParams();
  if (query.level) params.set("level", query.level);
  if (query.q) params.set("q", query.q);
  if (query.tail) params.set("tail", String(query.tail));
  const suffix = params.toString();
  // 日志接口在放开前只认 App 令牌，所以未登录时不发请求（会 401）。
  const key = authenticated ? `/api/v1/logs${suffix ? `?${suffix}` : ""}` : null;
  return useSWR<{ lines: string[] }, ApiError>(key, fetcher, { refreshInterval: refreshMs });
}

/** 发一个会改变状态的请求，失败时抛出带后端 message 的错误。 */
export async function send(url: string, method: string, body?: unknown): Promise<unknown> {
  const response = await fetch(url, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const payload = (await response.json().catch(() => ({}))) as {
    message?: string;
    error?: string;
  };
  if (!response.ok) {
    throw new ApiError(payload?.message || `HTTP ${response.status}`, response.status, payload?.error || "");
  }
  return payload;
}

export const controlApi = {
  scan: (mode: "incremental" | "full") => send("/api/v1/scan", "POST", { mode }),
  cancelScan: () => send("/api/v1/scan/cancel", "POST"),
  clearThumbs: () => send("/api/v1/cache/thumbs", "DELETE"),
  patchSettings: (patch: Record<string, unknown>) => send("/api/v1/settings", "PATCH", patch),
  armPairing: () => send("/api/v1/claim", "POST"),
  disarmPairing: () => send("/api/v1/claim", "DELETE"),
  revokeDevice: (id: string) => send(`/api/v1/tokens/${encodeURIComponent(id)}`, "DELETE"),
  logout: () => send("/api/v1/admin/logout", "POST"),
  refreshVolumes: () => fetch("/api/v1/admin/overview?refresh=1", { headers: { Accept: "application/json" } }).then((r) => r.json()),
};
