"use client";

import { useEffect, useRef, useState } from "react";
import { useI18n } from "@/components/I18nProvider";
import { Badge, Bar, Button, Card, CardHead, Empty, ErrorNote, Feedback, KeyValue, Notice } from "@/components/ui";
import { IconAlert, IconCheck, IconDevice } from "@/components/icons";
import { controlApi, type CacheInfo, type Device, type Health, type Library, type ScanProgress, type ScheduleInfo } from "@/lib/api";
import { formatBytes, formatClock, formatHM, formatNumber, relativeSince, relativeUntil } from "@/lib/format";
import {
  describeCheck,
  describeCheckValue,
  describeFuture,
  describePast,
  describePlatform,
  describeReason,
  describeScanMessage,
  describeScanMode,
  scanMessageTone,
} from "@/lib/labels";

/** 让依赖「现在几点」的读数（已用时、相对时间）每秒自己动起来。 */
function useTicker(active: boolean) {
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!active) return;
    const id = window.setInterval(() => setTick((n) => n + 1), 1000);
    return () => window.clearInterval(id);
  }, [active]);
}

type ActionFlash = { tone: "ok" | "warn" | "danger" | "tech"; text: string };

/** 统一处理「点一下 → 成功回执 / 失败原地说清原因」。 */
function useAction(reload: () => void) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [flash, setFlash] = useState<ActionFlash | null>(null);

  const run = async (
    label: string,
    task: () => Promise<unknown>,
    options?: { successMessage?: string; successTone?: ActionFlash["tone"] }
  ) => {
    setBusy(true);
    setError("");
    setFlash(null);
    try {
      await task();
      if (options?.successMessage) {
        setFlash({ tone: options.successTone ?? "ok", text: options.successMessage });
      }
      reload();
    } catch (caught) {
      setError(`${label}：${caught instanceof Error ? caught.message : t("error.unknown")}`);
    } finally {
      setBusy(false);
    }
  };

  return {
    busy,
    error,
    flash,
    run,
    clearError: () => setError(""),
    clearFlash: () => setFlash(null),
  };
}

// ─────────────────────────────── 扫描 ───────────────────────────────

type ScanFlash = { tone: "ok" | "warn" | "danger" | "tech"; text: string };

export function ScanPanel({
  progress,
  schedule,
  reload,
  compact = false,
}: {
  progress?: ScanProgress;
  schedule?: ScheduleInfo;
  reload: () => void;
  compact?: boolean;
}) {
  const { t } = useI18n();
  const serverRunning = !!progress?.running;
  // 点下按钮到服务端状态回来之间，本地先顶上「扫描中」，避免界面毫无反应。
  const [startingMode, setStartingMode] = useState<"full" | "incremental" | null>(null);
  // 「刚点下按钮」的即时回执；与上一轮结果（持久 message）分开，刷新后仍能看到结果。
  const [flash, setFlash] = useState<ScanFlash | null>(null);
  // 仅用于 POST 本身失败（服务端还没写进 progress.message）。
  const [localError, setLocalError] = useState<string | null>(null);
  const running = serverRunning || startingMode !== null;
  useTicker(running);
  const { busy, error, run } = useAction(reload);

  const startedAt = progress?.startedAt ? Date.parse(progress.startedAt) : Number.NaN;
  const elapsedSeconds = Number.isNaN(startedAt) ? 0 : Math.max(0, (Date.now() - startedAt) / 1000);
  const message = describeScanMessage(progress?.message, !!progress?.cancelled, t);
  const resultTone = scanMessageTone(progress?.message, !!progress?.cancelled);

  // 服务端确认进入 running 后撤掉本地 starting；结束后立刻刷新聚合数据。
  const prevRunning = useRef(false);
  const reloadRef = useRef(reload);
  reloadRef.current = reload;
  useEffect(() => {
    if (serverRunning) {
      setStartingMode(null);
      prevRunning.current = true;
      return;
    }
    if (prevRunning.current) {
      prevRunning.current = false;
      setStartingMode(null);
      setFlash(null);
      reloadRef.current();
    }
  }, [serverRunning]);

  // 启动失败但还没写进 progress 时（例如网络错误），超时撤掉乐观的「扫描中」。
  useEffect(() => {
    if (startingMode === null || serverRunning) return;
    const id = window.setTimeout(() => setStartingMode(null), 8000);
    return () => window.clearTimeout(id);
  }, [startingMode, serverRunning]);

  const startScan = async (mode: "full" | "incremental") => {
    setStartingMode(mode);
    setLocalError(null);
    setFlash({
      tone: "tech",
      text: t(mode === "full" ? "scan.startedFull" : "scan.startedIncremental"),
    });
    try {
      await controlApi.scan(mode);
      reloadRef.current();
    } catch (caught) {
      setStartingMode(null);
      setFlash(null);
      setLocalError(
        `${t(mode === "full" ? "scan.startFull" : "scan.startIncremental")}：${
          caught instanceof Error ? caught.message : t("error.unknown")
        }`
      );
    }
  };

  const cancelScan = () => {
    void run(t("scan.cancel"), controlApi.cancelScan, {
      successMessage: t("scan.cancelRequested"),
      successTone: "warn",
    });
  };

  const statusBadge = running ? (
    <Badge tone="tech" dot>
      {t("scan.running")}
    </Badge>
  ) : progress?.cancelled ? (
    <Badge tone="warn">{t("scan.cancelled")}</Badge>
  ) : progress?.message === "scan finished" ? (
    <Badge tone="ok">{t("scan.finished")}</Badge>
  ) : message && resultTone === "danger" ? (
    <Badge tone="danger">{t("scan.failed")}</Badge>
  ) : (
    <Badge tone="neutral">{t("scan.idle")}</Badge>
  );

  return (
    <Card className="flex min-h-0 flex-col">
      <CardHead
        title={t("nav.scan")}
        note={
          running ? t("scan.running") : progress?.finishedAt ? describePast(relativeSince(progress.finishedAt), t) : ""
        }
      >
        {statusBadge}
      </CardHead>

      {/* 没有总量分母，所以扫描中给的是不确定态光带而不是编出来的百分比。
          空闲时留空槽——填满的绿条会被读成「扫描已完成 100%」，而它其实什么都没说。 */}
      <Bar indeterminate={running} value={0} tone="tech" />

      <div className="mt-3.5 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Counter label={t("scan.filesSeen")} value={progress?.filesSeen} />
        <Counter label={t("scan.mediaIndexed")} value={progress?.mediaIndexed} />
        <Counter label={t("scan.errors")} value={progress?.errors} tone={progress?.errors ? "danger" : undefined} />
        <Counter
          label={running ? t("scan.elapsed") : t("scan.mode")}
          text={
            running
              ? formatClock(elapsedSeconds)
              : describeScanMode(progress?.mode || startingMode || undefined, t)
          }
        />
      </div>

      {running && progress?.currentPath ? (
        <p
          className="mt-3 truncate rounded-ctl border border-sep bg-canvas px-2.5 py-2 font-mono text-[11.5px] text-t3"
          title={progress.currentPath}
        >
          {progress.currentPath}
        </p>
      ) : null}

      {flash ? (
        <div className="mt-3">
          <Feedback tone={flash.tone}>{flash.text}</Feedback>
        </div>
      ) : null}

      {!running && message ? (
        <div className="mt-3">
          <Feedback tone={resultTone} role={resultTone === "danger" ? "alert" : "status"}>
            {message}
          </Feedback>
        </div>
      ) : null}

      <div className="mt-auto flex flex-wrap gap-2.5 pt-4">
        {running ? (
          <Button variant="danger" disabled={busy} onClick={cancelScan}>
            {t("scan.cancel")}
          </Button>
        ) : (
          <>
            <Button variant="primary" disabled={busy} onClick={() => void startScan("incremental")}>
              {t("scan.startIncremental")}
            </Button>
            <Button variant="ghost" disabled={busy} onClick={() => void startScan("full")}>
              {t("scan.startFull")}
            </Button>
          </>
        )}
      </div>

      {error || localError ? (
        <div className="mt-3">
          <ErrorNote>{localError || error}</ErrorNote>
        </div>
      ) : null}

      {!compact ? (
        <div className="mt-4">
          <KeyValue label={t("scan.scheduleLabel")} value={schedule?.daily || t("scan.manualOnly")} />
          {schedule?.nextRunAt ? (
            <KeyValue
              label={t("scan.nextRun")}
              value={`${formatHM(schedule.nextRunAt)} · ${describeFuture(relativeUntil(schedule.nextRunAt), t)}`}
            />
          ) : null}
          <KeyValue label={t("scan.foldersDone")} value={formatNumber(progress?.foldersDone)} />
          <KeyValue label={t("scan.reused")} value={formatNumber(progress?.reused)} />
          <KeyValue label={t("scan.librariesLabel")} value={formatNumber(progress?.libraries)} />
        </div>
      ) : null}

      {!running && !progress?.startedAt ? (
        <div className="mt-3">
          <Notice>{t("scan.neverHint")}</Notice>
        </div>
      ) : null}
    </Card>
  );
}

function Counter({
  label,
  value,
  text,
  tone,
}: {
  label: string;
  value?: number;
  text?: string;
  tone?: "danger";
}) {
  return (
    <div>
      <p className="font-mono text-[11px] leading-tight text-t3">{label}</p>
      <p
        className={`tabular mt-1 font-mono text-[19px] font-light leading-none ${
          tone === "danger" ? "text-danger" : "text-t1"
        }`}
      >
        {text ?? formatNumber(value)}
      </p>
    </div>
  );
}

// ─────────────────────────────── 设备 ───────────────────────────────

export function DeviceTable({
  devices,
  reload,
  compact = false,
}: {
  devices?: Device[];
  reload: () => void;
  compact?: boolean;
}) {
  const { t } = useI18n();
  const { busy, error, run } = useAction(reload);
  const rows = devices ?? [];

  const revoke = (device: Device) => {
    if (!window.confirm(t("device.revokeConfirm", { name: device.name }))) return;
    void run(t("device.revoke"), () => controlApi.revokeDevice(device.id));
  };

  return (
    <Card className="flex min-h-0 flex-col">
      <CardHead title={t("device.title")} note={t("device.count", { count: rows.length })} />

      {rows.length === 0 ? (
        <Empty>{t("device.none")}</Empty>
      ) : (
        <div className="-mx-1 overflow-x-auto">
          <table className="w-full min-w-[520px] border-collapse text-[13px]">
            <thead>
              <tr className="border-b border-sep">
                <th className="px-1 pb-2 text-left font-mono text-[11px] font-normal uppercase tracking-[0.1em] text-t3">
                  {t("device.name")}
                </th>
                <th className="px-1 pb-2 text-left font-mono text-[11px] font-normal uppercase tracking-[0.1em] text-t3">
                  {t("device.platform")}
                </th>
                <th className="px-1 pb-2 text-left font-mono text-[11px] font-normal uppercase tracking-[0.1em] text-t3">
                  {t("device.pairedAt")}
                </th>
                {!compact ? (
                  <th className="px-1 pb-2 text-left font-mono text-[11px] font-normal uppercase tracking-[0.1em] text-t3">
                    {t("device.lastSeen")}
                  </th>
                ) : null}
                <th className="px-1 pb-2" />
              </tr>
            </thead>
            <tbody>
              {rows.map((device) => (
                <tr key={device.id} className="border-b border-sep last:border-b-0">
                  <td className="px-1 py-2.5">
                    <span className="flex items-center gap-2 text-t1">
                      <IconDevice size={15} />
                      <span className="truncate">{device.name || t("device.unnamed")}</span>
                    </span>
                  </td>
                  <td className="px-1 py-2.5 text-t2">{describePlatform(device.platform, t)}</td>
                  <td className="px-1 py-2.5 font-mono text-[11.5px] text-t3">
                    {describePast(relativeSince(device.createdAt), t)}
                  </td>
                  {!compact ? (
                    <td className="px-1 py-2.5 font-mono text-[11.5px] text-t3">
                      {describePast(relativeSince(device.lastSeen), t)}
                    </td>
                  ) : null}
                  <td className="px-1 py-2.5 text-right">
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => revoke(device)}
                      className="font-mono text-[11.5px] text-t3 transition-colors hover:text-danger disabled:opacity-40"
                    >
                      {t("device.revoke")}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {error ? (
        <div className="mt-3">
          <ErrorNote>{error}</ErrorNote>
        </div>
      ) : null}
    </Card>
  );
}

// ─────────────────────────────── 健康 ───────────────────────────────

export function HealthList({ health }: { health?: Health }) {
  const { t } = useI18n();
  if (!health) return null;

  return (
    <Card>
      <CardHead
        title={t("health.title")}
        note={t("health.summary", { passed: health.passed, total: health.total })}
      />
      <ul className="flex flex-col">
        {health.checks.map((check) => (
          <li
            key={check.id}
            className="flex items-center gap-2.5 border-b border-sep py-2 last:border-b-0"
          >
            <span className={check.ok ? "text-ok" : "text-danger"}>
              {check.ok ? <IconCheck size={14} /> : <IconAlert size={14} />}
            </span>
            <span className="text-[12.5px] text-t1">{describeCheck(check.id, t)}</span>
            <span className="ml-auto text-right font-mono text-[11.5px] text-t3">
              {describeCheckValue(check, t)}
              {!check.ok && check.reason ? ` · ${describeReason(check.reason, t)}` : ""}
            </span>
          </li>
        ))}
      </ul>
    </Card>
  );
}

// ─────────────────────────────── 媒体库 ───────────────────────────────

export function LibraryList({ libraries, compact = false }: { libraries?: Library[]; compact?: boolean }) {
  const { t } = useI18n();
  const rows = libraries ?? [];
  const unreadable = rows.filter((library) => !library.exists).length;

  return (
    <Card className="flex min-h-0 flex-col">
      <CardHead
        title={t("nav.libraries")}
        note={t("library.readableCount", { readable: rows.length - unreadable, total: rows.length })}
      />
      {rows.length === 0 ? (
        <Empty>{t("library.none")}</Empty>
      ) : (
        <ul className="flex flex-col">
          {rows.map((library) => (
            <li key={library.id} className="flex items-center gap-3 border-b border-sep py-2 last:border-b-0">
              <div className="min-w-0 flex-1">
                <p className="truncate text-[13px] text-t1">{library.name}</p>
                {!compact ? (
                  <p className="mt-0.5 truncate font-mono text-[11.5px] text-t3" title={library.path}>
                    {library.path}
                  </p>
                ) : null}
              </div>
              {library.exists ? (
                <Badge tone="ok">{t("library.readable")}</Badge>
              ) : (
                <Badge tone="danger">{t("library.unreadable")}</Badge>
              )}
            </li>
          ))}
        </ul>
      )}
      {/* 空列表时上面那句 Empty 已经把「只能由 App 添加」说清楚了，这里不再重复。 */}
      {rows.length > 0 ? (
        <p className="mt-3 font-mono text-[11.5px] leading-relaxed text-t3">
          {unreadable > 0 ? t("library.unreadableHint") : t("library.managedInApp")}
        </p>
      ) : null}
    </Card>
  );
}

// ─────────────────────────────── 缓存 ───────────────────────────────

export function CachePanel({
  cache,
  reload,
  compact = false,
}: {
  cache?: CacheInfo;
  reload: () => void;
  compact?: boolean;
}) {
  const { t } = useI18n();
  const { busy, error, run } = useAction(reload);
  const used = cache?.thumbBytes ?? 0;
  const limit = cache?.thumbLimitBytes ?? 0;
  const ratio = limit > 0 ? Math.min(1, used / limit) : 0;
  const bytes = formatBytes(used);
  const limitBytes = formatBytes(limit);

  const clear = () => {
    if (!window.confirm(t("cache.clearConfirm"))) return;
    void run(t("cache.clear"), controlApi.clearThumbs);
  };

  return (
    <Card className="flex min-h-0 flex-col">
      <CardHead
        title={t("nav.cache")}
        note={limit > 0 ? t("cache.percent", { percent: Math.round(ratio * 100) }) : t("cache.unlimited")}
      />
      <div className="flex items-baseline gap-2">
        <span className="tabular font-mono text-[27px] font-light leading-none text-t1">
          {bytes ? bytes.value : "—"}
        </span>
        <span className="font-mono text-[14px] text-t3">{bytes?.unit ?? ""}</span>
        {limitBytes ? (
          <span className="ml-1 font-mono text-[11.5px] text-t3">
            / {limitBytes.value} {limitBytes.unit}
          </span>
        ) : null}
      </div>
      <div className="mt-3">
        <Bar value={limit > 0 ? ratio : 0} tone={ratio > 0.9 ? "warn" : "tech"} />
      </div>
      {!compact ? (
        <div className="mt-3">
          <KeyValue label={t("cache.files")} value={formatNumber(cache?.thumbFiles)} />
          <KeyValue label={t("cache.limit")} value={limitBytes ? `${limitBytes.value} ${limitBytes.unit}` : t("cache.unlimited")} />
        </div>
      ) : null}
      <p className="mt-3 font-mono text-[11.5px] leading-relaxed text-t3">{t("cache.clearHint")}</p>
      <div className="mt-auto flex flex-wrap gap-2.5 pt-4">
        <Button variant="ghost" disabled={busy} onClick={clear}>
          {t("cache.clear")}
        </Button>
      </div>
      {error ? (
        <div className="mt-3">
          <ErrorNote>{error}</ErrorNote>
        </div>
      ) : null}
    </Card>
  );
}
