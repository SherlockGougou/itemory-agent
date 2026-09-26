"use client";

import { useEffect, useMemo, useState } from "react";
import { useConsole } from "@/components/Shell";
import { Button, Card, CardHead, ErrorNote, Notice } from "@/components/ui";
import { useI18n } from "@/components/I18nProvider";
import { controlApi, type SettingsShape } from "@/lib/api";

interface Draft {
  preset: string;
  thumbSize: number;
  cacheGb: number;
  scanSchedule: string;
  logLevel: string;
  transcode: string;
  motionPhoto: boolean;
  rawPreview: boolean;
}

const GIB = 1 << 30;

/** 与后端 config.Validate 的 clamp 边界保持一致：超出范围会被静默夹住，表单上先说清。 */
const THUMB_SIZES = [128, 256, 384, 512, 768, 1024, 2048];
const CACHE_GIB = [1, 2, 5, 10, 20, 50];
const PRESETS = ["light", "balanced", "performance"];
const LOG_LEVELS = ["debug", "info", "warn", "error"];
const TRANSCODES = ["off", "onDemand", "all"];

function fromSettings(settings: SettingsShape): Draft {
  return {
    preset: settings.preset || "balanced",
    thumbSize: settings.thumbSize || 512,
    cacheGb: Math.round(((settings.thumbCacheLimitBytes || 0) / GIB) * 10) / 10,
    scanSchedule: settings.scanSchedule || "",
    logLevel: settings.logLevel || "info",
    transcode: settings.transcode || "off",
    motionPhoto: !!settings.motionPhoto,
    rawPreview: !!settings.rawPreview,
  };
}

/**
 * 设置页。
 *
 * 只提交**改动过**的字段，并且只走 PATCH 白名单——后端 Update() 是整份替换，
 * 而这里拿不到 libraries / excludePatterns，整份回传会把它们抹掉。
 */
export default function SettingsPage() {
  const { t } = useI18n();
  const { data, reload } = useConsole();
  const settings = data?.settings;

  const [draft, setDraft] = useState<Draft | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (settings && !draft) setDraft(fromSettings(settings));
  }, [settings, draft]);

  const base = settings ? fromSettings(settings) : null;

  const patch = useMemo(() => {
    if (!base || !draft) return {} as Record<string, unknown>;
    const out: Record<string, unknown> = {};
    if (draft.preset !== base.preset) out.preset = draft.preset;
    if (draft.thumbSize !== base.thumbSize) out.thumbSize = draft.thumbSize;
    if (draft.cacheGb !== base.cacheGb) out.thumbCacheLimitBytes = Math.round(draft.cacheGb * GIB);
    if (draft.scanSchedule !== base.scanSchedule) out.scanSchedule = draft.scanSchedule;
    if (draft.logLevel !== base.logLevel) out.logLevel = draft.logLevel;
    if (draft.transcode !== base.transcode) out.transcode = draft.transcode;
    if (draft.motionPhoto !== base.motionPhoto) out.motionPhoto = draft.motionPhoto;
    if (draft.rawPreview !== base.rawPreview) out.rawPreview = draft.rawPreview;
    return out;
  }, [base, draft]);

  const dirty = Object.keys(patch).length > 0;
  const presetChanged = !!base && !!draft && draft.preset !== base.preset;

  const update = (partial: Partial<Draft>) => {
    setDraft((current) => (current ? { ...current, ...partial } : current));
    setSaved(false);
  };

  const save = async () => {
    if (!dirty) return;
    setBusy(true);
    setError("");
    try {
      const response = (await controlApi.patchSettings(patch)) as SettingsShape;
      setDraft(fromSettings(response));
      setSaved(true);
      reload();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : t("error.unknown"));
    } finally {
      setBusy(false);
    }
  };

  if (!draft || !settings) {
    return (
      <Card>
        <p className="font-mono text-[12px] text-t3">{t("boot.loading")}</p>
      </Card>
    );
  }

  return (
    <div className="flex max-w-[720px] flex-col gap-4">
      <Card>
        <CardHead title={t("settings.resourceTitle")} note={t("settings.presetNote")} />

        <Field label={t("settings.preset")} hint={t("settings.presetHint")}>
          <Select
            value={draft.preset}
            onChange={(value) => update({ preset: value })}
            options={PRESETS.map((preset) => ({ value: preset, label: t(`settings.preset.${preset}`) }))}
          />
        </Field>

        {presetChanged ? (
          <div className="mb-3">
            <Notice>{t("settings.presetOverwrite")}</Notice>
          </div>
        ) : null}

        <Field label={t("settings.thumbSize")} hint={t("settings.thumbSizeHint")}>
          <Select
            value={String(draft.thumbSize)}
            onChange={(value) => update({ thumbSize: Number(value) })}
            options={THUMB_SIZES.map((size) => ({ value: String(size), label: `${size} px` }))}
          />
        </Field>

        <Field label={t("settings.cacheLimit")} hint={t("settings.cacheLimitHint")}>
          <Select
            value={String(draft.cacheGb)}
            onChange={(value) => update({ cacheGb: Number(value) })}
            options={CACHE_GIB.map((gb) => ({ value: String(gb), label: `${gb} GB` }))}
          />
        </Field>
      </Card>

      <Card>
        <CardHead title={t("settings.scheduleTitle")} />

        <Field label={t("settings.scanSchedule")} hint={t("settings.scanScheduleHint")}>
          <div className="flex items-center gap-3">
            <input
              type="time"
              value={draft.scanSchedule}
              onChange={(event) => update({ scanSchedule: event.target.value })}
              className="min-h-[36px] rounded-ctl border border-line bg-canvas px-3 font-mono text-[12.5px] text-t1 outline-none focus:border-tech"
            />
            <button
              type="button"
              onClick={() => update({ scanSchedule: "" })}
              className="font-mono text-[11.5px] text-t3 transition-colors hover:text-t1"
            >
              {t("settings.manualOnly")}
            </button>
          </div>
        </Field>
        {!draft.scanSchedule ? <Notice>{t("settings.scheduleManual")}</Notice> : null}
      </Card>

      <Card>
        <CardHead title={t("settings.runtimeTitle")} />

        <Field label={t("settings.logLevel")} hint={t("settings.logLevelHint")}>
          <Select
            value={draft.logLevel}
            onChange={(value) => update({ logLevel: value })}
            options={LOG_LEVELS.map((level) => ({ value: level, label: level.toUpperCase() }))}
          />
        </Field>

        <Field label={t("settings.transcode")} hint={t("settings.transcodeHint")}>
          <Select
            value={draft.transcode}
            onChange={(value) => update({ transcode: value })}
            options={TRANSCODES.map((mode) => ({ value: mode, label: t(`settings.transcode.${mode}`) }))}
          />
        </Field>

        <Toggle
          label={t("settings.motionPhoto")}
          hint={t("settings.motionPhotoHint")}
          checked={draft.motionPhoto}
          onChange={(checked) => update({ motionPhoto: checked })}
        />
        <Toggle
          label={t("settings.rawPreview")}
          hint={t("settings.rawPreviewHint")}
          checked={draft.rawPreview}
          onChange={(checked) => update({ rawPreview: checked })}
        />
      </Card>

      <Notice>{t("settings.whitelistNote")}</Notice>

      <div className="sticky bottom-0 flex flex-wrap items-center gap-3 border-t border-line bg-canvas/95 py-3 backdrop-blur">
        <Button variant="primary" onClick={save} disabled={!dirty || busy}>
          {busy ? t("action.working") : t("action.save")}
        </Button>
        <Button
          variant="ghost"
          onClick={() => {
            setDraft(fromSettings(settings));
            setSaved(false);
            setError("");
          }}
          disabled={!dirty || busy}
        >
          {t("action.reset")}
        </Button>
        {dirty ? <span className="font-mono text-[11.5px] text-t3">{t("settings.dirtyCount", { count: Object.keys(patch).length })}</span> : null}
        {saved && !dirty ? <span className="font-mono text-[11.5px] text-ok">{t("settings.saved")}</span> : null}
      </div>

      {error ? <ErrorNote>{error}</ErrorNote> : null}
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="mb-4 last:mb-0">
      <p className="mb-1.5 font-mono text-[11px] uppercase tracking-[0.14em] text-t3">{label}</p>
      {children}
      {hint ? <p className="mt-1.5 font-mono text-[11px] leading-relaxed text-t3">{hint}</p> : null}
    </div>
  );
}

function Select({
  value,
  options,
  onChange,
}: {
  value: string;
  options: { value: string; label: string }[];
  onChange: (value: string) => void;
}) {
  return (
    <select
      value={value}
      onChange={(event) => onChange(event.target.value)}
      className="min-h-[36px] w-full max-w-[280px] rounded-ctl border border-line bg-canvas px-3 font-mono text-[12.5px] text-t1 outline-none focus:border-tech"
    >
      {options.map((option) => (
        <option key={option.value} value={option.value} className="bg-surface text-t1">
          {option.label}
        </option>
      ))}
    </select>
  );
}

function Toggle({
  label,
  hint,
  checked,
  onChange,
}: {
  label: string;
  hint?: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-sep py-3 last:border-b-0">
      <div className="min-w-0">
        <p className="text-[12.5px] text-t1">{label}</p>
        {hint ? <p className="mt-0.5 font-mono text-[11px] leading-relaxed text-t3">{hint}</p> : null}
      </div>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        onClick={() => onChange(!checked)}
        className={`relative h-[22px] w-[40px] shrink-0 rounded-full border transition-colors ${
          checked ? "border-brand bg-brand" : "border-line bg-raise"
        }`}
      >
        <span
          className={`absolute top-[2px] block h-[16px] w-[16px] rounded-full bg-canvas transition-all ${
            checked ? "left-[20px]" : "left-[2px]"
          }`}
        />
      </button>
    </div>
  );
}
