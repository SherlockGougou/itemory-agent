"use client";

import { useEffect, useMemo, useState } from "react";
import { Card, CardHead, Empty } from "@/components/ui";
import { useI18n } from "@/components/I18nProvider";
import { useLogs } from "@/lib/api";
import { describeLogLevel } from "@/lib/labels";
import { formatLogTime, parseLogLine } from "@/lib/format";

const LEVELS: { value: string; label: string }[] = [
  { value: "", label: "ALL" },
  { value: "debug", label: "DEBUG" },
  { value: "info", label: "INFO" },
  { value: "warn", label: "WARN" },
  { value: "error", label: "ERROR" },
];

const LEVEL_TONE: Record<string, string> = {
  DEBUG: "text-t3",
  INFO: "text-tech",
  WARN: "text-warn",
  ERROR: "text-danger",
};

/**
 * 日志页。
 *
 * 服务端下发的是 slog 的 JSON 行——解析、过滤、级别着色都在前端做。
 * 过滤是在**服务端**执行的（?level / ?q），因为 Ring 里有两千行，
 * 每次都把全量传到浏览器再筛是浪费。
 */
export default function LogsPage() {
  const { t } = useI18n();
  const [level, setLevel] = useState("");
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [live, setLive] = useState(true);

  // 关键字防抖：不然每敲一个字符就发一次请求。
  useEffect(() => {
    const id = window.setTimeout(() => setDebounced(query.trim()), 300);
    return () => window.clearTimeout(id);
  }, [query]);

  const { data, isLoading, error } = useLogs(true, { level, q: debounced, tail: 300 }, live ? 5000 : 0);

  const rows = useMemo(() => (data?.lines ?? []).map(parseLogLine), [data]);

  return (
    <Card>
      <CardHead title={t("logs.title")} note={t("logs.count", { count: rows.length })}>
        <div className="flex flex-wrap items-center gap-1">
          {LEVELS.map((item) => (
            <button
              key={item.value || "all"}
              type="button"
              onClick={() => setLevel(item.value)}
              className={`rounded-ctl px-2.5 py-1.5 font-mono text-[11px] transition-colors ${
                level === item.value ? "bg-raise text-t1" : "text-t3 hover:text-t1"
              }`}
            >
              {item.label}
            </button>
          ))}
        </div>
      </CardHead>

      <div className="mb-3 flex flex-wrap items-center gap-3">
        <input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder={t("logs.searchPlaceholder")}
          spellCheck={false}
          className="min-h-[34px] min-w-[200px] flex-1 rounded-ctl border border-line bg-canvas px-3 font-mono text-[12px] text-t1 outline-none focus:border-tech"
        />
        <button
          type="button"
          onClick={() => setLive((current) => !current)}
          className={`rounded-ctl border px-3 py-1.5 font-mono text-[11px] transition-colors ${
            live ? "border-tech/40 text-tech" : "border-line text-t3 hover:text-t1"
          }`}
        >
          {live ? t("logs.liveOn") : t("logs.liveOff")}
        </button>
      </div>

      {error ? (
        <Empty>{t("logs.loadFailed")}</Empty>
      ) : rows.length === 0 ? (
        <Empty>{isLoading ? t("boot.loading") : t("logs.empty")}</Empty>
      ) : (
        <ul className="max-h-[calc(100vh-320px)] min-h-[240px] overflow-auto rounded-ctl border border-sep bg-canvas p-2">
          {rows.map((row, position) => (
            <li
              key={`${position}-${row.time ?? ""}`}
              className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 border-b border-sep px-1.5 py-1.5 font-mono text-[11.5px] leading-relaxed last:border-b-0"
            >
              <span className="tabular shrink-0 text-t3">{formatLogTime(row.time)}</span>
              <span className={`shrink-0 ${LEVEL_TONE[row.level ?? ""] ?? "text-t3"}`}>
                {row.level ? describeLogLevel(row.level) : "----"}
              </span>
              <span className="min-w-0 flex-1 break-words text-t1">{row.msg}</span>
              {row.fields.map((field) => (
                <span key={field.key} className="shrink-0 text-t3">
                  <span className="text-t2">{field.key}</span>=<span className="text-t2">{field.value}</span>
                </span>
              ))}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
