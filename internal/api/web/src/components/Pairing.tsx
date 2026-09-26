"use client";

import { useEffect, useState } from "react";
import { useI18n } from "@/components/I18nProvider";
import { Badge, Button, ErrorNote, KeyValue, Notice } from "@/components/ui";
import { IconPairing } from "@/components/icons";
import { controlApi, type Pairing as PairingState } from "@/lib/api";
import { formatClock } from "@/lib/format";

/**
 * 配对面板。
 *
 * 设备名、平台、令牌都由服务端管；这个组件只负责一次性配对窗口的开关与展示。
 *
 * 一个必须诚实处理的细节：服务端**从不下发**已开启窗口里的配对码（它只存在于
 * 生成那一瞬间的响应里）。所以页面刷新后我们知道「窗口开着」但不知道码——
 * 这时不编一个假的码，而是把二维码照常给出（码就在里面），并说明可以重新生成。
 */
export function Pairing({
  pairing,
  onMutate,
  compact = false,
}: {
  pairing?: PairingState;
  onMutate: () => void;
  compact?: boolean;
}) {
  const { t } = useI18n();
  const [code, setCode] = useState("");
  const [hostOverride, setHostOverride] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [remaining, setRemaining] = useState<number | null>(null);
  const [qrStamp, setQrStamp] = useState(() => Date.now());
  const [qrFailed, setQrFailed] = useState(false);

  const armed = !!pairing?.armed;
  const paired = pairing?.pairedDevices ?? 0;
  const expiresAt = pairing?.expiresAt ? Date.parse(pairing.expiresAt) : Number.NaN;

  // 概览页那张卡窄一些，二维码用小一档。两种排布都必须给码——
  // 「开始配对」之后看不到码就没法完成配对，而这是这个面板唯一的用处。
  const qrSize = compact ? 132 : 176;

  useEffect(() => {
    if (!armed || Number.isNaN(expiresAt)) {
      setRemaining(null);
      return;
    }
    const tick = () => {
      const left = Math.max(0, Math.round((expiresAt - Date.now()) / 1000));
      setRemaining(left);
      if (left === 0) onMutate();
    };
    tick();
    const id = window.setInterval(tick, 1000);
    return () => window.clearInterval(id);
  }, [armed, expiresAt, onMutate]);

  // 每次窗口重新开启都换一个 stamp：二维码图片会被浏览器缓存，
  // 不换 stamp 的话「重新生成」看着像没生效。
  useEffect(() => {
    if (armed) {
      setQrStamp(Date.now());
      setQrFailed(false);
    }
  }, [armed, pairing?.expiresAt]);

  const arm = async () => {
    setError("");
    setBusy(true);
    try {
      const payload = (await controlApi.armPairing()) as { code?: string };
      setCode(payload?.code || "");
      onMutate();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : t("pair.error"));
    } finally {
      setBusy(false);
    }
  };

  const disarm = async () => {
    setError("");
    setBusy(true);
    try {
      await controlApi.disarmPairing();
      setCode("");
      onMutate();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : t("pair.error"));
    } finally {
      setBusy(false);
    }
  };

  const qrUrl = `/api/v1/claim/qr.svg?stamp=${qrStamp}${
    hostOverride ? `&host=${encodeURIComponent(hostOverride.trim())}` : ""
  }`;

  const state = armed
    ? { tone: "brand" as const, label: t("pair.state.armed") }
    : paired > 0
    ? { tone: "ok" as const, label: t("pair.state.paired", { count: paired }) }
    : { tone: "neutral" as const, label: t("pair.state.idle") };

  return (
    <div className="flex flex-wrap items-start gap-5">
      <div className="min-w-0 flex-1">
        <div className="mb-3 flex flex-wrap items-center gap-3">
          <Badge tone={state.tone} dot>
            {state.label}
          </Badge>
          {remaining !== null && remaining > 0 ? (
            <span className="tabular font-mono text-[11.5px] text-t3">
              {t("pair.expiresIn", { time: formatClock(remaining) })}
            </span>
          ) : null}
        </div>

        {armed ? (
          <>
            <p className="font-mono text-[12px] uppercase tracking-[0.14em] text-t3">{t("pair.codeLabel")}</p>
            <p className="tabular mt-1.5 font-mono text-[30px] leading-none tracking-[0.22em] text-brand">
              {code || "______"}
            </p>
            {!code ? <p className="mt-2 text-[12px] text-t3">{t("pair.codeHidden")}</p> : null}
          </>
        ) : (
          <p className="text-[13px] leading-relaxed text-t2">{t("pair.intro")}</p>
        )}

        {!compact ? (
          <div className="mt-5">
            <label className="flex flex-col gap-1.5">
              <span className="font-mono text-[11px] uppercase tracking-[0.14em] text-t3">
                {t("pair.hostLabel")}
              </span>
              <input
                type="text"
                value={hostOverride}
                onChange={(event) => setHostOverride(event.target.value)}
                placeholder={t("pair.hostPlaceholder")}
                spellCheck={false}
                className="min-h-[36px] max-w-[280px] rounded-ctl border border-line bg-canvas px-3 font-mono text-[12.5px] text-t1 outline-none transition-colors focus:border-tech"
              />
            </label>
            <p className="mt-1.5 font-mono text-[11px] leading-relaxed text-t3">{t("pair.hostHint")}</p>
          </div>
        ) : null}

        <div className="mt-5 flex flex-wrap gap-2.5">
          {armed ? (
            <>
              <Button variant="primary" onClick={arm} disabled={busy}>
                {t("pair.rotate")}
              </Button>
              <Button variant="ghost" onClick={disarm} disabled={busy}>
                {t("pair.stop")}
              </Button>
            </>
          ) : (
            <Button variant="primary" onClick={arm} disabled={busy}>
              <IconPairing size={15} />
              {busy ? t("action.working") : t("pair.start")}
            </Button>
          )}
        </div>

        {error ? (
          <div className="mt-3">
            <ErrorNote>{error}</ErrorNote>
          </div>
        ) : null}

        {!compact ? (
          <div className="mt-5">
            <Notice>{t("pair.scanHint")}</Notice>
            <div className="mt-3">
              <KeyValue label={t("pair.devicesLabel")} value={paired} />
              <KeyValue label={t("pair.windowLabel")} value={`${pairing?.windowSeconds ?? 0}s`} />
              <KeyValue label={t("pair.codeLengthLabel")} value={pairing?.codeLength ?? 6} />
            </div>
          </div>
        ) : null}
      </div>

      {armed ? (
        <div className="shrink-0">
          {/* 二维码画在白底上：它是给相机读的，深色主题下反色没有意义。 */}
          <div className="rounded-card bg-white p-2.5">
            {qrFailed ? (
              // 加载失败要说出来。旧控制台在这里有一个兜底占位图，重写时丢了——
              // 结果是二维码加载不出来时页面只是「少了一块」，看不出哪里不对。
              <div
                className="flex items-center justify-center px-2 text-center"
                style={{ width: qrSize, height: qrSize }}
              >
                <span className="font-mono text-[11px] leading-snug text-danger">{t("pair.qrFailed")}</span>
              </div>
            ) : (
              /* eslint-disable-next-line @next/next/no-img-element */
              <img
                src={qrUrl}
                alt={t("pair.qrAlt")}
                width={qrSize}
                height={qrSize}
                className="block select-none"
                style={{ width: qrSize, height: qrSize }}
                onError={() => setQrFailed(true)}
              />
            )}
          </div>
          <p className="mt-2 text-center font-mono text-[11px] leading-snug text-t3">
            {t("pair.scanHintShort")}
          </p>
        </div>
      ) : null}
    </div>
  );
}
