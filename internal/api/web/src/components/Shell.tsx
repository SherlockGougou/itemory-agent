"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { createContext, useContext, useEffect, useState, type ComponentType, type ReactNode } from "react";import { useTheme } from "next-themes";
import { LANGUAGE_NAMES, useI18n } from "@/components/I18nProvider";
import { AdminAuth } from "@/components/AdminAuth";
import { BrandMark } from "@/components/BrandMark";
import { Badge, Button } from "@/components/ui";
import {
  IconCache,
  IconClose,
  IconDiagnostics,
  IconFolder,
  IconGlobe,
  IconLogs,
  IconLogout,
  IconMenu,
  IconMoon,
  IconOverview,
  IconPairing,
  IconScan,
  IconSettings,
  IconSun,
} from "@/components/icons";
import { controlApi, useAdminStatus, useConsoleData, type ConsoleData } from "@/lib/api";
import { formatUptime } from "@/lib/format";

interface ConsoleContextValue {
  data?: ConsoleData;
  authenticated: boolean;
  loading: boolean;
  reload: () => void;
}

const ConsoleContext = createContext<ConsoleContextValue>({
  authenticated: false,
  loading: true,
  reload: () => {},
});

/** 页面组件通过它拿聚合数据，不必各自再发一次请求。 */
export function useConsole(): ConsoleContextValue {
  return useContext(ConsoleContext);
}

type NavItem = {
  href: string;
  key: string;
  Icon: ComponentType<{ size?: number }>;
};
type NavGroup = { key: string; items: NavItem[] };

const NAV: NavGroup[] = [
  {
    key: "nav.group.monitor",
    items: [
      { href: "/", key: "nav.overview", Icon: IconOverview },
      { href: "/pair", key: "nav.pairing", Icon: IconPairing },
    ],
  },
  {
    key: "nav.group.data",
    items: [
      { href: "/libraries", key: "nav.libraries", Icon: IconFolder },
      { href: "/scan", key: "nav.scan", Icon: IconScan },
      { href: "/cache", key: "nav.cache", Icon: IconCache },
    ],
  },
  {
    key: "nav.group.system",
    items: [
      { href: "/settings", key: "nav.settings", Icon: IconSettings },
      { href: "/logs", key: "nav.logs", Icon: IconLogs },
      { href: "/diagnostics", key: "nav.diagnostics", Icon: IconDiagnostics },
    ],
  },
];

/**
 * 健康判定里「需要用户动手」的那几项。
 * pairing / schedule 不参与计数——没配对过设备是正常的，不该在侧边栏当待办盯着你。
 */
const ACTIONABLE = new Set(["libraries", "volumes", "index", "cache", "tools", "lastScan"]);

function isActive(pathname: string, href: string): boolean {
  if (href === "/") return pathname === "/" || pathname === "";
  return pathname === href || pathname.startsWith(`${href}/`);
}

export function Shell({ children }: { children: ReactNode }) {
  const { t } = useI18n();
  const pathname = usePathname() || "/";
  const { data: admin, error: adminError, mutate: mutateAdmin } = useAdminStatus();
  const authenticated = !!admin?.authenticated;
  const { data, error, isLoading, mutate } = useConsoleData(authenticated);
  const [navOpen, setNavOpen] = useState(false);

  useEffect(() => {
    setNavOpen(false);
  }, [pathname]);

  const handleLogout = async () => {
    try {
      await controlApi.logout();
    } catch {
      // 退出失败也要把本地状态刷新回未登录，否则界面会停在假登录态。
    }
    await mutateAdmin();
  };

  if (adminError) {
    return <FatalScreen message={t("error.connect")} detail={adminError.message} onRetry={() => mutateAdmin()} />;
  }
  if (!admin) {
    return <BootScreen label={t("boot.checking")} />;
  }
  if (!admin.authenticated) {
    return <AdminAuth admin={admin} onMutate={mutateAdmin} />;
  }

  const failing = data?.health ? data.health.checks.filter((check) => !check.ok) : [];
  const pending = failing.filter((check) => ACTIONABLE.has(check.id)).length;

  return (
    <ConsoleContext.Provider value={{ data, authenticated, loading: isLoading, reload: () => void mutate() }}>
      <div className="flex min-h-screen bg-canvas">
        <SideNav
          pathname={pathname}
          pending={pending}
          username={admin.username || ""}
          open={navOpen}
          onClose={() => setNavOpen(false)}
          onLogout={handleLogout}
        />
        <div className="flex min-w-0 flex-1 flex-col">
          <TopBar
            pathname={pathname}
            data={data}
            authenticated={authenticated}
            onOpenNav={() => setNavOpen(true)}
            onLogout={handleLogout}
          />
          <main className="min-w-0 flex-1 px-4 py-5 sm:px-6">
            {error && !data ? (
              <FatalScreen message={t("error.load")} detail={error.message} onRetry={() => void mutate()} inline />
            ) : (
              children
            )}
          </main>
        </div>
      </div>
    </ConsoleContext.Provider>
  );
}

function SideNav({
  pathname,
  pending,
  username,
  open,
  onClose,
  onLogout,
}: {
  pathname: string;
  pending: number;
  username: string;
  open: boolean;
  onClose: () => void;
  onLogout: () => void;
}) {
  const { t } = useI18n();
  const { theme, setTheme } = useTheme();
  const { locale, setLocale } = useI18n();
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  return (
    <>
      {open ? (
        <button
          type="button"
          aria-label={t("nav.close")}
          onClick={onClose}
          className="fixed inset-0 z-30 bg-black/50 lg:hidden"
        />
      ) : null}

      {/*
        lg 起用 sticky + h-screen，而不是 static。
        static 会让侧栏成为普通 flex 项、高度被撑到**整页**高度，于是账号/主题/退出
        那一块跟着落到文档最底部——在设置页这种长页面上得滚到底才看得见。
        sticky 把它钉在视口上，导航区自己内部滚动，底部始终在手边。
      */}
      <aside
        className={`fixed inset-y-0 left-0 z-40 flex w-[248px] flex-col border-r border-line bg-rail px-3.5 pb-4 pt-5 transition-transform lg:sticky lg:top-0 lg:h-screen lg:translate-x-0 ${
          open ? "translate-x-0" : "-translate-x-full"
        }`}
      >
        <div className="mb-5 flex items-center gap-2.5 px-2">
          <BrandMark />
          <div className="min-w-0">
            <p className="truncate text-[14px] font-medium leading-tight text-t1">Itemory</p>
            <p className="mt-0.5 text-[11px] leading-tight text-t3">{t("app.title.short")}</p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={t("nav.close")}
            className="ml-auto text-t3 lg:hidden"
          >
            <IconClose size={18} />
          </button>
        </div>

        <nav className="min-h-0 flex-1 overflow-y-auto">
          {NAV.map((group) => (
            <div key={group.key} className="mb-1">
              <p className="px-2.5 pb-1.5 pt-3 font-mono text-[10px] uppercase tracking-[0.16em] text-t3">
                {t(group.key)}
              </p>
              {group.items.map((item) => {
                const active = isActive(pathname, item.href);
                return (
                  <Link
                    key={item.href}
                    href={item.href}
                    aria-current={active ? "page" : undefined}
                    className={`mb-0.5 flex min-h-[40px] items-center gap-2.5 rounded-[9px] px-2.5 text-[13.5px] transition-colors ${
                      active ? "bg-raise text-t1" : "text-t2 hover:bg-raise/60 hover:text-t1"
                    }`}
                  >
                    <span className={active ? "text-tech" : "text-t3"}>
                      <item.Icon size={16} />
                    </span>
                    <span className="truncate">{t(item.key)}</span>
                    {item.href === "/" && pending > 0 ? (
                      <span className="ml-auto rounded-full bg-brand px-1.5 py-0.5 font-mono text-[11px] leading-none text-canvas">
                        {pending}
                      </span>
                    ) : null}
                  </Link>
                );
              })}
            </div>
          ))}
        </nav>

        <div className="mt-3 border-t border-sep px-2.5 pt-3">
          <div className="flex items-center gap-2.5">
            <span className="flex h-[30px] w-[30px] shrink-0 items-center justify-center rounded-[8px] bg-raise text-[13px] text-t1">
              {(username || "?").slice(0, 1).toUpperCase()}
            </span>
            <div className="min-w-0 flex-1">
              <p className="truncate text-[13px] text-t1">{username || t("admin.unknown")}</p>
              <p className="mt-0.5 font-mono text-[11px] leading-tight text-t3">{t("admin.role")}</p>
            </div>
          </div>

          <div className="mt-3 flex items-center gap-2">
            <select
              value={locale}
              onChange={(event) => setLocale(event.target.value as typeof locale)}
              aria-label={t("app.language")}
              className="min-w-0 flex-1 rounded-ctl border border-line bg-transparent px-2 py-1.5 font-mono text-[11.5px] text-t2 outline-none"
            >
              {Object.entries(LANGUAGE_NAMES).map(([code, name]) => (
                <option key={code} value={code} className="bg-surface text-t1">
                  {name}
                </option>
              ))}
            </select>
            {mounted ? (
              <button
                type="button"
                onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
                title={theme === "dark" ? t("app.theme.toLight") : t("app.theme.toDark")}
                aria-label={theme === "dark" ? t("app.theme.toLight") : t("app.theme.toDark")}
                className="flex h-[32px] w-[32px] shrink-0 items-center justify-center rounded-ctl border border-line text-t3 transition-colors hover:text-t1"
              >
                {theme === "dark" ? <IconSun size={15} /> : <IconMoon size={15} />}
              </button>
            ) : null}
          </div>

          <button
            type="button"
            onClick={onLogout}
            className="mt-2 flex min-h-[36px] w-full items-center gap-2 rounded-ctl px-1 text-[12.5px] text-t3 transition-colors hover:text-danger"
          >
            <IconLogout size={15} />
            {t("admin.logout")}
          </button>
        </div>
      </aside>
    </>
  );
}

function TopBar({
  pathname,
  data,
  authenticated,
  onOpenNav,
  onLogout,
}: {
  pathname: string;
  data?: ConsoleData;
  authenticated: boolean;
  onOpenNav: () => void;
  onLogout: () => void;
}) {
  const { t } = useI18n();
  const [clock, setClock] = useState("");

  useEffect(() => {
    const tick = () =>
      setClock(
        new Date().toLocaleTimeString(undefined, {
          hour: "2-digit",
          minute: "2-digit",
          second: "2-digit",
          hour12: false,
        })
      );
    tick();
    const id = window.setInterval(tick, 1000);
    return () => window.clearInterval(id);
  }, []);

  const active = NAV.flatMap((group) => group.items).find((item) => isActive(pathname, item.href));
  const title = active ? t(active.key) : t("nav.overview");

  const health = data?.health;
  const failing = health ? health.checks.filter((check) => !check.ok).length : 0;
  const tone = failing === 0 ? "ok" : failing <= 2 ? "warn" : "danger";
  const healthLabel = !health
    ? t("health.public")
    : failing === 0
    ? t("health.allPass")
    : t("health.failing", { count: failing });

  const uptime = data?.service?.uptimeSeconds;

  return (
    <header className="sticky top-0 z-20 flex min-h-[56px] flex-wrap items-center gap-x-4 gap-y-2 border-b border-line bg-canvas/95 px-4 py-2 backdrop-blur sm:px-6">
      <button
        type="button"
        onClick={onOpenNav}
        aria-label={t("nav.open")}
        className="text-t2 lg:hidden"
      >
        <IconMenu size={20} />
      </button>

      <h1 className="text-[16px] font-medium text-t1">{title}</h1>

      <div className="ml-auto flex flex-wrap items-center gap-x-4 gap-y-2">
        {data ? <Badge tone={tone} dot>{healthLabel}</Badge> : null}
        {data?.service ? (
          <span className="hidden font-mono text-[11.5px] text-t2 sm:inline">
            v{data.service.version} · {t("service.uptimeShort")} {formatUptime(uptime)}
          </span>
        ) : null}
        <span className="tabular hidden font-mono text-[11.5px] text-t3 md:inline">{clock}</span>
        <span className="flex items-center gap-3 text-t3">
          <IconGlobe size={15} />
          {authenticated ? (
            <button type="button" onClick={onLogout} aria-label={t("admin.logout")} title={t("admin.logout")}>
              <IconLogout size={15} />
            </button>
          ) : null}
        </span>
      </div>
    </header>
  );
}

/** 首屏引导：鉴权状态还没回来之前不闪错误，也不白屏。 */
function BootScreen({ label }: { label: string }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas">
      <p className="font-mono text-[12px] text-t3">{label}</p>
    </div>
  );
}

function FatalScreen({
  message,
  detail,
  onRetry,
  inline = false,
}: {
  message: string;
  detail?: string;
  onRetry: () => void;
  inline?: boolean;
}) {
  const { t } = useI18n();
  const body = (
    <div className="mx-auto max-w-md rounded-card border border-line bg-surface px-5 py-6 text-center">
      <p className="text-[13.5px] text-t1">{message}</p>
      {detail ? <p className="mt-2 font-mono text-[11.5px] text-t3">{detail}</p> : null}
      <div className="mt-4 flex justify-center">
        <Button variant="solid" onClick={onRetry}>
          {t("action.retry")}
        </Button>
      </div>
    </div>
  );
  if (inline) return body;
  return <div className="flex min-h-screen items-center justify-center bg-canvas px-6">{body}</div>;
}
