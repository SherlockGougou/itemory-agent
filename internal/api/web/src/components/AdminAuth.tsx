"use client";

import { useState } from "react";
import { BrandMark } from "@/components/BrandMark";
import { useI18n } from "@/components/I18nProvider";
import { Button, ErrorNote } from "@/components/ui";
import type { AdminStatus } from "@/lib/api";

/**
 * 管理员登录 / 首次设置。
 *
 * 这是控制台唯一的闸门：所有管理面能力（扫描、缓存、设置、日志、设备）
 * 都以这里拿到的会话 cookie 为前提，所以它是全屏的，不与侧边栏共存。
 */
export function AdminAuth({ admin, onMutate }: { admin: AdminStatus; onMutate: () => void }) {
  const { t } = useI18n();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const isSetup = !admin?.configured;

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setError("");
    setBusy(true);
    try {
      const response = await fetch(isSetup ? "/api/v1/admin/setup" : "/api/v1/admin/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: username.trim(), password }),
      });
      const payload = (await response.json().catch(() => ({}))) as { message?: string };
      if (!response.ok) {
        throw new Error(payload?.message || t("admin.error"));
      }
      setUsername("");
      setPassword("");
      onMutate();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : t("admin.error"));
    } finally {
      setBusy(false);
    }
  };

  const canSubmit = !busy && username.trim().length > 0 && password.length > 0;

  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas px-5 py-10">
      <div className="w-full max-w-[380px]">
        <div className="mb-7 flex items-center gap-3">
          <BrandMark size={30} />
          <div>
            <p className="text-[15px] font-medium leading-tight text-t1">Itemory</p>
            <p className="mt-0.5 text-[11.5px] leading-tight text-t3">{t("app.title.sub")}</p>
          </div>
        </div>

        <div className="rounded-card border border-line bg-surface px-5 py-6">
          <h1 className="text-[15px] font-medium text-t1">
            {isSetup ? t("admin.setupTitle") : t("admin.loginTitle")}
          </h1>
          <p className="mt-2 text-[13px] leading-relaxed text-t2">
            {isSetup ? t("admin.setupIntro") : t("admin.loginIntro")}
          </p>

          <form onSubmit={submit} className="mt-5 flex flex-col gap-4">
            <label className="flex flex-col gap-1.5">
              <span className="font-mono text-[11px] uppercase tracking-[0.14em] text-t3">
                {t("admin.username")}
              </span>
              <input
                type="text"
                required
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                spellCheck={false}
                autoCapitalize="off"
                autoComplete="username"
                className="min-h-[40px] rounded-ctl border border-line bg-canvas px-3 font-mono text-[13px] text-t1 outline-none transition-colors focus:border-tech"
              />
            </label>

            <label className="flex flex-col gap-1.5">
              <span className="font-mono text-[11px] uppercase tracking-[0.14em] text-t3">
                {t("admin.password")}
              </span>
              <input
                type="password"
                required
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                autoComplete={isSetup ? "new-password" : "current-password"}
                className="min-h-[40px] rounded-ctl border border-line bg-canvas px-3 font-mono text-[13px] text-t1 outline-none transition-colors focus:border-tech"
              />
              {isSetup ? (
                <span className="font-mono text-[11px] text-t3">{t("admin.passwordRule")}</span>
              ) : null}
            </label>

            {error ? <ErrorNote>{error}</ErrorNote> : null}

            <Button type="submit" variant="primary" disabled={!canSubmit} className="mt-1 min-h-[42px]">
              {busy ? t("action.working") : isSetup ? t("admin.setup") : t("admin.login")}
            </Button>
          </form>
        </div>

        <p className="mt-5 text-center font-mono text-[11px] leading-relaxed text-t3">
          {t("footer.lanOnly")}
        </p>
      </div>
    </div>
  );
}
