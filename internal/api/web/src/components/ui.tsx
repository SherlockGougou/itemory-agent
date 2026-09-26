"use client";

import type { ReactNode } from "react";

/** 卡片：控制台里所有内容的容器。圆角与描边是全站统一的 12px / --line。 */
export function Card({
  children,
  className = "",
  padded = true,
}: {
  children: ReactNode;
  className?: string;
  padded?: boolean;
}) {
  return (
    <section
      className={`rounded-card border border-line bg-surface ${padded ? "px-4 py-3.5" : ""} ${className}`}
    >
      {children}
    </section>
  );
}

/** 卡片标题行：左侧标题，右侧可选说明或状态。 */
export function CardHead({
  title,
  note,
  children,
}: {
  title: string;
  note?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <header className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2">
      <h2 className="text-[13px] font-medium text-t1">{title}</h2>
      {note ? <span className="font-mono text-[11.5px] text-t3">{note}</span> : null}
      <div className="ml-auto flex items-center gap-2">{children}</div>
    </header>
  );
}

type Tone = "neutral" | "ok" | "warn" | "danger" | "tech" | "brand";

const TONE_CLASS: Record<Tone, string> = {
  neutral: "bg-raise text-t2",
  ok: "bg-ok/15 text-ok",
  warn: "bg-warn/15 text-warn",
  danger: "bg-danger/15 text-danger",
  tech: "bg-tech/15 text-tech",
  brand: "bg-brand/15 text-brand",
};

/** 状态胶囊。文字用对应的语义色，底色是同色的低透明度——深色下也够清晰。 */
export function Badge({
  tone = "neutral",
  dot = false,
  children,
}: {
  tone?: Tone;
  dot?: boolean;
  children: ReactNode;
}) {
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 font-mono text-[11px] leading-none ${TONE_CLASS[tone]}`}
    >
      {dot ? <i className="h-1.5 w-1.5 rounded-full bg-current" /> : null}
      {children}
    </span>
  );
}

/** 按钮：三个层级。primary 用 text-canvas 作前景——它在深浅两档下都与品牌色有足够对比。 */
export function Button({
  children,
  variant = "ghost",
  type = "button",
  disabled,
  onClick,
  title,
  className = "",
}: {
  children: ReactNode;
  variant?: "primary" | "ghost" | "solid" | "danger";
  type?: "button" | "submit";
  disabled?: boolean;
  onClick?: () => void;
  title?: string;
  className?: string;
}) {
  const variants = {
    primary: "bg-brand text-canvas border border-brand hover:opacity-90",
    ghost: "bg-transparent text-t2 border border-line hover:text-t1 hover:bg-raise",
    solid: "bg-raise text-t1 border border-line hover:border-t3",
    danger: "bg-transparent text-danger border border-danger/40 hover:bg-danger/10",
  } as const;
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      title={title}
      className={`inline-flex min-h-[36px] items-center justify-center gap-1.5 rounded-ctl px-3.5 text-[12.5px] transition-colors disabled:pointer-events-none disabled:opacity-40 ${variants[variant]} ${className}`}
    >
      {children}
    </button>
  );
}

/**
 * 进度条。
 *
 * value 为 undefined 时进入不确定态——扫描没有总量分母，给不出百分比，
 * 用滑动光带表达「在动」比编一个假的百分比诚实。
 */
export function Bar({
  value,
  indeterminate = false,
  tone = "tech",
}: {
  value?: number;
  indeterminate?: boolean;
  tone?: "tech" | "brand" | "ok" | "warn" | "danger";
}) {
  const toneClass = {
    tech: "bg-tech",
    brand: "bg-brand",
    ok: "bg-ok",
    warn: "bg-warn",
    danger: "bg-danger",
  }[tone];
  const pct = Math.max(0, Math.min(1, value ?? 0)) * 100;
  return (
    <div className="h-1 w-full overflow-hidden rounded-full bg-raise">
      {indeterminate ? (
        <div className={`scan-bar h-full w-2/5 rounded-full ${toneClass}`} />
      ) : (
        <div className={`h-full rounded-full transition-all duration-500 ${toneClass}`} style={{ width: `${pct}%` }} />
      )}
    </div>
  );
}

/** 大盘数字。字号固定、等宽、字宽锁定，避免计数跳动时行宽抖动。 */
export function Stat({
  label,
  value,
  unit,
  detail,
  footer,
}: {
  label: string;
  value: ReactNode;
  unit?: string;
  detail?: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <div className="flex flex-col rounded-card border border-line bg-surface px-4 py-3.5">
      <span className="font-mono text-[11px] uppercase tracking-[0.1em] text-t3">{label}</span>
      <div className="mt-auto pt-2">
        <span className="tabular font-mono text-[27px] font-light leading-none text-t1">{value}</span>
        {unit ? <span className="ml-1 font-mono text-[14px] text-t3">{unit}</span> : null}
      </div>
      {detail ? <div className="mt-1.5 font-mono text-[11.5px] leading-[1.5] text-t3">{detail}</div> : null}
      {footer}
    </div>
  );
}

/** 键值行，用于运行环境、诊断明细这类「标签 → 值」的清单。 */
export function KeyValue({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-b border-sep py-2 last:border-b-0">
      <span className="text-[12.5px] text-t2">{label}</span>
      <span className="tabular text-right font-mono text-[12px] text-t1">{value}</span>
    </div>
  );
}

/** 空状态：控制台里大量区块在「没有媒体库 / 没有设备」时是空的，必须说清楚下一步。 */
export function Empty({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-card border border-dashed border-line px-4 py-6 text-center font-mono text-[12px] leading-relaxed text-t3">
      {children}
    </p>
  );
}

/**
 * 行内反馈条。动作结果（已开始 / 已完成 / 失败）必须在原地给出色调明确的回执，
 * 不能只靠控制台角落的灰色小字——那会被当成装饰直接忽略。
 */
export function Feedback({
  tone = "tech",
  children,
  role = "status",
}: {
  tone?: "ok" | "warn" | "danger" | "tech";
  children: ReactNode;
  role?: "status" | "alert";
}) {
  const toneClass = {
    ok: "border-ok/40 bg-ok/10 text-ok",
    warn: "border-warn/40 bg-warn/10 text-warn",
    danger: "border-danger/40 bg-danger/10 text-danger",
    tech: "border-tech/40 bg-tech/10 text-tech",
  }[tone];
  return (
    <p role={role} className={`rounded-ctl border px-3 py-2 font-mono text-[12px] leading-relaxed ${toneClass}`}>
      {children}
    </p>
  );
}

/** 行内错误。控制台的动作会失败（401 / 409 / 429），必须在原地说清原因。 */
export function ErrorNote({ children }: { children: ReactNode }) {
  return (
    <Feedback tone="danger" role="alert">
      {children}
    </Feedback>
  );
}

/** 中性提示，用于「这个动作会造成什么」这类前置说明。 */
export function Notice({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-ctl border border-line bg-raise px-3 py-2 text-[12px] leading-relaxed text-t2">
      {children}
    </p>
  );
}
