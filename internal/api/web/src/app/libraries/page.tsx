"use client";

import { useState } from "react";
import { useConsole } from "@/components/Shell";
import { LibraryList } from "@/components/panels";
import { Button, Card, CardHead, Empty, ErrorNote, KeyValue, Notice } from "@/components/ui";
import { useI18n } from "@/components/I18nProvider";
import { controlApi, type VolumeInfo } from "@/lib/api";
import { formatNumber } from "@/lib/format";

export default function LibrariesPage() {
  const { t } = useI18n();
  const { data, reload } = useConsole();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const volumes: VolumeInfo[] = data?.volumes ?? [];

  /**
   * 挂载探测带 15 秒缓存（它对每个挂载根都要 ReadDir 一轮），
   * 所以「重新检测」要绕过去——插了块盘就得立刻看见。
   */
  const rescan = async () => {
    setBusy(true);
    setError("");
    try {
      await controlApi.refreshVolumes();
      reload();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : t("error.unknown"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-4">
      <LibraryList libraries={data?.libraries} />
      <Notice>{t("library.managedInApp")}</Notice>

      <Card>
        <CardHead title={t("volume.title")} note={t("volume.count", { count: volumes.length })}>
          <Button variant="ghost" onClick={rescan} disabled={busy}>
            {t("volume.rescan")}
          </Button>
        </CardHead>

        {volumes.length === 0 ? (
          <Empty>{t("volume.none")}</Empty>
        ) : (
          <ul className="flex flex-col">
            {volumes.map((volume) => (
              <li key={volume.containerPath} className="border-b border-sep py-3 last:border-b-0">
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <code className="truncate font-mono text-[12.5px] text-t1">{volume.containerPath}</code>
                  <span
                    className={`font-mono text-[11.5px] ${volume.readable ? "text-ok" : "text-danger"}`}
                  >
                    {volume.readable ? t("volume.readable") : t("volume.unreadable")}
                  </span>
                </div>
                {volume.readable ? (
                  <p className="mt-1.5 font-mono text-[11.5px] leading-relaxed text-t3">
                    {t("volume.detail", {
                      items: formatNumber(volume.topLevelItems),
                      media: formatNumber(volume.mediaFolders?.length ?? 0),
                    })}
                    {volume.ownerUid >= 0 ? ` · uid:gid ${volume.ownerUid}:${volume.ownerGid}` : ""}
                  </p>
                ) : volume.error ? (
                  <p className="mt-1.5 font-mono text-[11.5px] leading-relaxed text-t3">{volume.error}</p>
                ) : null}
              </li>
            ))}
          </ul>
        )}

        {data?.suggestedUser ? (
          <div className="mt-3">
            <KeyValue label={t("volume.suggestedUser")} value={data.suggestedUser} />
            <p className="mt-2 font-mono text-[11.5px] leading-relaxed text-t3">{t("volume.suggestedUserHint")}</p>
          </div>
        ) : null}

        {error ? (
          <div className="mt-3">
            <ErrorNote>{error}</ErrorNote>
          </div>
        ) : null}
      </Card>
    </div>
  );
}
