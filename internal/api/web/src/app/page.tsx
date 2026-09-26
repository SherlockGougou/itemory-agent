"use client";

import { useConsole } from "@/components/Shell";
import { CachePanel, DeviceTable, LibraryList, ScanPanel } from "@/components/panels";
import { Pairing } from "@/components/Pairing";
import { Card, CardHead, Empty, Stat } from "@/components/ui";
import { useI18n } from "@/components/I18nProvider";
import { formatBytes, formatNumber } from "@/lib/format";
import { describeKind } from "@/lib/labels";

/**
 * 概览：控制台首屏。
 *
 * 排布对应「一屏看全」这个目标：四格指标 → 扫描与配对（最常盯的两块）→
 * 设备 → 媒体库与缓存。日志与诊断有自己的页面，不挤在这里。
 */
export default function OverviewPage() {
  const { t } = useI18n();
  const { data, reload } = useConsole();

  const index = data?.index;
  const byKind = index?.byKind ?? {};
  const cache = data?.cache;
  const libraries = data?.libraries ?? [];
  const tools = data?.tools ?? {};
  const toolEntries = Object.entries(tools);
  const toolsOk = toolEntries.filter(([, ok]) => ok).length;

  const cacheBytes = formatBytes(cache?.thumbBytes);
  const cacheLimit = formatBytes(cache?.thumbLimitBytes);
  const unreadable = libraries.filter((library) => !library.exists).length;

  // 按**实际扫到的**类别列明细，而不是硬编码「照片 / 视频 / RAW」三类。
  // byKind 是服务端按库里真实内容分组的——这台机器上它是
  // {image, video, motion, motion-clip}，硬编码会静默漏掉动态照片与实况片段，
  // 于是 KPI 的总数和明细对不上（515 vs 304）。
  const kindBreakdown = Object.entries(byKind)
    .filter(([, count]) => count > 0)
    .sort((a, b) => b[1] - a[1])
    .map(([kind, count]) => `${formatNumber(count)} ${describeKind(kind, t)}`);

  if (!data) {
    return (
      <Card>
        <Empty>{t("boot.loading")}</Empty>
      </Card>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Stat
          label={t("stat.entries")}
          value={formatNumber(index?.entries)}
          detail={kindBreakdown.length > 0 ? kindBreakdown.join(" · ") : "—"}
        />
        <Stat
          label={t("stat.cache")}
          value={cacheBytes ? cacheBytes.value : "—"}
          unit={cacheBytes?.unit ?? ""}
          detail={
            cacheLimit
              ? t("stat.cacheLimit", { limit: `${cacheLimit.value} ${cacheLimit.unit}` })
              : t("cache.unlimited")
          }
        />
        <Stat
          label={t("stat.libraries")}
          value={formatNumber(libraries.length)}
          detail={
            unreadable > 0 ? (
              <span className="text-danger">{t("stat.librariesBad", { count: unreadable })}</span>
            ) : (
              t("stat.librariesOk")
            )
          }
        />
        <Stat
          label={t("stat.tools")}
          value={`${toolsOk}`}
          unit={`/ ${toolEntries.length}`}
          detail={toolEntries.length === 0 ? t("stat.toolsUnknown") : toolEntries.map(([name]) => name).join(" · ")}
        />
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1.62fr)_minmax(0,1fr)]">
        <ScanPanel progress={data.scan} schedule={data.schedule} reload={reload} />
        <Card className="flex min-h-0 flex-col">
          <CardHead title={t("nav.pairing")} />
          <Pairing pairing={data.pairing} onMutate={reload} compact />
        </Card>
      </div>

      <DeviceTable devices={data.devices} reload={reload} />

      <div className="grid gap-4 xl:grid-cols-2">
        <LibraryList libraries={libraries} />
        <CachePanel cache={cache} reload={reload} compact />
      </div>
    </div>
  );
}
