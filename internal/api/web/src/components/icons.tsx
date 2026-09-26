"use client";

/**
 * 图标集。统一 24 视框、1.6 线宽、currentColor 描边——与 App 的「极简、扁平、
 * 避免 3D」一致，也让它们在任何 token 配色下自动跟随。
 */
type IconProps = { size?: number; className?: string };

function Svg({ size = 16, className = "", children }: IconProps & { children: React.ReactNode }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={`shrink-0 ${className}`}
    >
      {children}
    </svg>
  );
}

export const IconOverview = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="3" width="7.5" height="7.5" rx="1.8" />
    <rect x="13.5" y="3" width="7.5" height="7.5" rx="1.8" />
    <rect x="3" y="13.5" width="7.5" height="7.5" rx="1.8" />
    <rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.8" />
  </Svg>
);

export const IconPairing = (p: IconProps) => (
  <Svg {...p}>
    <path d="M10 13.6a4 4 0 0 0 5.7 0l2.8-2.8a4 4 0 1 0-5.7-5.7l-1 1" />
    <path d="M14 10.4a4 4 0 0 0-5.7 0l-2.8 2.8a4 4 0 1 0 5.7 5.7l1-1" />
  </Svg>
);

export const IconFolder = (p: IconProps) => (
  <Svg {...p}>
    <path d="M3 7.6A2.1 2.1 0 0 1 5.1 5.5h3.5a2 2 0 0 1 1.4.6l1.1 1.1h7.8A2.1 2.1 0 0 1 21 9.3v8.1a2.1 2.1 0 0 1-2.1 2.1H5.1A2.1 2.1 0 0 1 3 17.4z" />
  </Svg>
);

export const IconScan = (p: IconProps) => (
  <Svg {...p}>
    <path d="M20.6 12a8.6 8.6 0 1 1-2.5-6.1" />
    <polyline points="20.6 3.6 20.6 9.4 14.8 9.4" />
    <circle cx="12" cy="12" r="3.1" />
  </Svg>
);

export const IconCache = (p: IconProps) => (
  <Svg {...p}>
    <ellipse cx="12" cy="6.4" rx="7.6" ry="3.4" />
    <path d="M4.4 6.4v11.2c0 1.9 3.4 3.4 7.6 3.4s7.6-1.5 7.6-3.4V6.4" />
    <path d="M4.4 12c0 1.9 3.4 3.4 7.6 3.4s7.6-1.5 7.6-3.4" />
  </Svg>
);

export const IconSettings = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4 7.4h9M18.6 7.4h1.4M4 16.6h3.4M13 16.6h7" />
    <circle cx="16" cy="7.4" r="2.2" />
    <circle cx="10.4" cy="16.6" r="2.2" />
  </Svg>
);

export const IconLogs = (p: IconProps) => (
  <Svg {...p}>
    <path d="M8.5 6.2h11M8.5 12h11M8.5 17.8h11M4.2 6.2h.01M4.2 12h.01M4.2 17.8h.01" />
  </Svg>
);

export const IconDiagnostics = (p: IconProps) => (
  <Svg {...p}>
    <polyline points="3 12.2 7.4 12.2 10.4 5 13.8 19 16.4 12.2 21 12.2" />
  </Svg>
);

export const IconLogout = (p: IconProps) => (
  <Svg {...p}>
    <path d="M14.6 3.8h3.2a2.2 2.2 0 0 1 2.2 2.2v12a2.2 2.2 0 0 1-2.2 2.2h-3.2" />
    <polyline points="9.6 8 5.6 12 9.6 16" />
    <path d="M5.6 12h8.6" />
  </Svg>
);

export const IconGlobe = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M3.2 12h17.6" />
    <path d="M12 3c2.6 2.8 2.6 15.2 0 18M12 3c-2.6 2.8-2.6 15.2 0 18" />
  </Svg>
);

export const IconMoon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M20.6 13.4A8.6 8.6 0 1 1 10.6 3.4a6.9 6.9 0 0 0 10 10z" />
  </Svg>
);

export const IconSun = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="4.2" />
    <path d="M12 2.6v2.2M12 19.2v2.2M2.6 12h2.2M19.2 12h2.2M5.4 5.4l1.6 1.6M17 17l1.6 1.6M18.6 5.4 17 7M7 17l-1.6 1.6" />
  </Svg>
);

export const IconAlert = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 3.6 21.2 19.4H2.8z" />
    <path d="M12 9.4v4.3M12 16.7h.01" />
  </Svg>
);

export const IconCheck = (p: IconProps) => (
  <Svg {...p}>
    <polyline points="20 6.6 9.6 17 4 11.4" />
  </Svg>
);

export const IconClock = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="8.6" />
    <polyline points="12 6.8 12 12 15.6 14.2" />
  </Svg>
);

export const IconDevice = (p: IconProps) => (
  <Svg {...p}>
    <rect x="6" y="2.5" width="12" height="19" rx="3" />
    <path d="M10.8 5.6h2.4" />
  </Svg>
);

export const IconMenu = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4 7h16M4 12h16M4 17h16" />
  </Svg>
);

export const IconClose = (p: IconProps) => (
  <Svg {...p}>
    <path d="M6.2 6.2l11.6 11.6M17.8 6.2L6.2 17.8" />
  </Svg>
);
