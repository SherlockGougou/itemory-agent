"use client";

import { useConsole } from "@/components/Shell";
import { HealthList } from "@/components/panels";
import { Card, CardHead, KeyValue, Notice } from "@/components/ui";
import { useI18n } from "@/components/I18nProvider";
import { formatDateTime, formatNumber, formatUptime } from "@/lib/format";

export default function DiagnosticsPage() {
  const { t } = useI18n();
  const { data } = useConsole();
  const service = data?.service;
  const settings = data?.settings;
  const tools = data?.tools ?? {};
  const hosts = data?.hosts;
  const volumes = data?.volumes ?? [];

  return (
    <div className="flex flex-col gap-4">
      <HealthList health={data?.health} />

      <div className="grid gap-4 xl:grid-cols-2">
        <Card>
          <CardHead title={t("diagnostics.serviceTitle")} />
          <KeyValue label={t("diagnostics.name")} value={service?.name ?? "—"} />
          <KeyValue label={t("diagnostics.version")} value={service?.version ?? "—"} />
          <KeyValue label={t("diagnostics.apiVersion")} value={formatNumber(service?.apiVersion)} />
          <KeyValue label={t("diagnostics.serverId")} value={service?.serverId ?? "—"} />
          <KeyValue label={t("diagnostics.uptime")} value={formatUptime(service?.uptimeSeconds)} />
          <KeyValue
            label={t("diagnostics.generatedAt")}
            value={service?.generatedAt ? formatDateTime(service.generatedAt) : "—"}
          />
        </Card>

        <Card>
          <CardHead title={t("diagnostics.hostsTitle")} />
          <KeyValue label={t("diagnostics.scheme")} value={hosts?.scheme ?? "—"} />
          <KeyValue label={t("diagnostics.host")} value={hosts?.host ?? "—"} />
          <KeyValue label={t("diagnostics.port")} value={hosts?.port ?? "—"} />
          <KeyValue label={t("diagnostics.loopback")} value={hosts?.loopback ? t("common.yes") : t("common.no")} />
          {hosts?.loopback ? (
            <div className="mt-3">
              <Notice>{t("diagnostics.loopbackHint")}</Notice>
            </div>
          ) : null}
          {hosts?.candidates?.length ? (
            <p className="mt-3 break-words font-mono text-[11.5px] leading-relaxed text-t3">
              {t("diagnostics.candidates")}: {hosts.candidates.join(" · ")}
            </p>
          ) : null}
        </Card>

        <Card>
          <CardHead
            title={t("diagnostics.toolsTitle")}
            note={t("diagnostics.toolsNote", {
              ok: Object.values(tools).filter(Boolean).length,
              total: Object.keys(tools).length,
            })}
          />
          <ul className="flex flex-col">
            {Object.entries(tools).map(([name, ok]) => (
              <li key={name} className="flex items-center justify-between border-b border-sep py-2 last:border-b-0">
                <code className="font-mono text-[12.5px] text-t1">{name}</code>
                <span className={`font-mono text-[11.5px] ${ok ? "text-ok" : "text-danger"}`}>
                  {ok ? t("common.available") : t("common.missing")}
                </span>
              </li>
            ))}
          </ul>
          <p className="mt-3 font-mono text-[11.5px] leading-relaxed text-t3">{t("diagnostics.toolsHint")}</p>
        </Card>

        <Card>
          <CardHead title={t("diagnostics.settingsTitle")} />
          <KeyValue label={t("settings.preset")} value={settings?.preset ?? "—"} />
          <KeyValue label={t("settings.thumbSize")} value={`${settings?.thumbSize ?? "—"} px`} />
          <KeyValue label={t("settings.scanSchedule")} value={settings?.scanSchedule || t("scan.manualOnly")} />
          <KeyValue label={t("settings.logLevel")} value={settings?.logLevel?.toUpperCase() ?? "—"} />
          <KeyValue label={t("settings.transcode")} value={settings?.transcode ?? "—"} />
          <KeyValue label={t("settings.concurrency")} value={formatNumber(settings?.concurrency)} />
          <KeyValue label={t("settings.excludePatterns")} value={settings?.excludePatterns?.length ?? 0} />
        </Card>
      </div>

      <Card>
        <CardHead title={t("diagnostics.volumesTitle")} note={t("volume.count", { count: volumes.length })} />
        <ul className="flex flex-col">
          {volumes.map((volume) => (
            <li
              key={volume.containerPath}
              className="flex flex-wrap items-center gap-x-3 border-b border-sep py-2 last:border-b-0"
            >
              <code className="truncate font-mono text-[12px] text-t1">{volume.containerPath}</code>
              <span className={`font-mono text-[11.5px] ${volume.readable ? "text-ok" : "text-danger"}`}>
                {volume.readable ? t("volume.readable") : t("volume.unreadable")}
              </span>
              <span className="ml-auto font-mono text-[11.5px] text-t3">
                {volume.ownerUid >= 0 ? `uid:gid ${volume.ownerUid}:${volume.ownerGid}` : "—"}
              </span>
            </li>
          ))}
        </ul>
      </Card>
    </div>
  );
}
