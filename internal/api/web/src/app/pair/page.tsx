"use client";

import { useConsole } from "@/components/Shell";
import { Pairing } from "@/components/Pairing";
import { DeviceTable } from "@/components/panels";
import { Card, CardHead } from "@/components/ui";
import { useI18n } from "@/components/I18nProvider";

export default function PairPage() {
  const { t } = useI18n();
  const { data, reload } = useConsole();

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHead title={t("pair.title")} />
        <Pairing pairing={data?.pairing} onMutate={reload} />
      </Card>
      <DeviceTable devices={data?.devices} reload={reload} />
    </div>
  );
}
