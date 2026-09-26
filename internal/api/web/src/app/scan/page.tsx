"use client";

import { useConsole } from "@/components/Shell";
import { ScanPanel } from "@/components/panels";
import { Card, CardHead, KeyValue, Notice } from "@/components/ui";
import { useI18n } from "@/components/I18nProvider";
import { formatDateTime, formatNumber } from "@/lib/format";
import { describeKind } from "@/lib/labels";

export default function ScanPage() {
  const { t } = useI18n();
  const { data, reload } = useConsole();
  const index = data?.index;
  const byKind = index?.byKind ?? {};
  const lastScanAt = index?.lastScanAt ? new Date(index.lastScanAt * 1000).toISOString() : undefined;

  return (
    <div className="flex flex-col gap-4">
      <ScanPanel progress={data?.scan} schedule={data?.schedule} reload={reload} />

      <div className="grid gap-4 xl:grid-cols-2">
        <Card>
          <CardHead title={t("scanPanel.indexTitle")} />
          <KeyValue label={t("stat.entries")} value={formatNumber(index?.entries)} />
          {/* 按实际扫到的类别逐行列，和概览页同一口径：硬编码三类会漏掉
              动态照片与实况片段，总数就对不上。 */}
          {Object.entries(byKind)
            .filter(([, count]) => count > 0)
            .sort((a, b) => b[1] - a[1])
            .map(([kind, count]) => (
              <KeyValue key={kind} label={describeKind(kind, t)} value={formatNumber(count)} />
            ))}
          <KeyValue label={t("index.removed")} value={formatNumber(index?.removed)} />
          <KeyValue label={t("index.lastScan")} value={lastScanAt ? formatDateTime(lastScanAt) : t("time.never")} />
          <KeyValue label={t("index.generation")} value={formatNumber(index?.lastScanGen)} />
        </Card>

        <Card>
          <CardHead title={t("scanPanel.behaviourTitle")} />
          <div className="flex flex-col gap-3">
            <Notice>{t("scanPanel.incrementalHint")}</Notice>
            <Notice>{t("scanPanel.fullHint")}</Notice>
            <Notice>{t("scanPanel.cancelHint")}</Notice>
          </div>
        </Card>
      </div>
    </div>
  );
}
