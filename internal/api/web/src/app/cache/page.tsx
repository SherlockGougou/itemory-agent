"use client";

import { useConsole } from "@/components/Shell";
import { CachePanel } from "@/components/panels";
import { Card, CardHead, KeyValue } from "@/components/ui";
import { useI18n } from "@/components/I18nProvider";
import { formatNumber } from "@/lib/format";

export default function CachePage() {
  const { t } = useI18n();
  const { data, reload } = useConsole();
  const cache = data?.cache;
  const settings = data?.settings;

  return (
    <div className="flex flex-col gap-4">
      <CachePanel cache={cache} reload={reload} />

      <Card>
        <CardHead title={t("cache.detailTitle")} />
        <KeyValue label={t("cache.files")} value={formatNumber(cache?.thumbFiles)} />
        <KeyValue label={t("cache.usage")} value={formatNumber(cache?.thumbBytes)} />
        <KeyValue label={t("cache.limit")} value={formatNumber(cache?.thumbLimitBytes)} />
        <KeyValue label={t("settings.thumbSize")} value={`${settings?.thumbSize ?? "—"} px`} />
      </Card>
    </div>
  );
}
