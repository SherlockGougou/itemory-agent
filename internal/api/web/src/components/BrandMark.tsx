"use client";

import { useTheme } from "next-themes";
import { useEffect, useState } from "react";
import lightIcon from "@/components/itemory-icon-light.png";
import darkIcon from "@/components/itemory-icon-dark.png";

/**
 * Itemory App 品牌图标。白天/夜晚各一张，跟随控制台主题切换，
 * 与 iOS AppIcon 的两套外观保持一致，不再用 CSS 色块占位。
 */
export function BrandMark({ size = 30 }: { size?: number }) {
  const { resolvedTheme } = useTheme();
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  const src = mounted && resolvedTheme === "dark" ? darkIcon.src : lightIcon.src;

  return (
    <img
      src={src}
      alt=""
      width={size}
      height={size}
      aria-hidden="true"
      className="shrink-0 rounded-[7px] object-cover"
      style={{ width: size, height: size }}
    />
  );
}
