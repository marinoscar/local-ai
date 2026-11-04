import type { ReactNode } from "react";

// Inline SVG icons (no icon font, no network). Decorative: the button text carries the meaning.

function Svg({ children }: { children: ReactNode }) {
  return (
    <svg
      className="icon"
      viewBox="0 0 24 24"
      width="40"
      height="40"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {children}
    </svg>
  );
}

export const ChatIcon = () => (
  <Svg>
    <path d="M4 5h16v11H9l-5 4z" />
    <path d="M8 9h8M8 12h5" />
  </Svg>
);
export const CameraIcon = () => (
  <Svg>
    <path d="M3 8h4l2-3h6l2 3h4v12H3z" />
    <circle cx="12" cy="13" r="3.5" />
  </Svg>
);
export const MicIcon = () => (
  <Svg>
    <rect x="9" y="3" width="6" height="11" rx="3" />
    <path d="M5 11a7 7 0 0 0 14 0M12 18v3" />
  </Svg>
);
export const BookIcon = () => (
  <Svg>
    <path d="M4 4h7a2 2 0 0 1 1 .3A2 2 0 0 1 13 4h7v15h-7a2 2 0 0 0-1 .3 2 2 0 0 0-1-.3H4z" />
    <path d="M12 4.3V19.3" />
  </Svg>
);
export const MapIcon = () => (
  <Svg>
    <path d="M3 6l6-2 6 2 6-2v14l-6 2-6-2-6 2z" />
    <path d="M9 4v14M15 6v14" />
  </Svg>
);
export const ManualIcon = () => (
  <Svg>
    <path d="M6 3h9l4 4v14H6z" />
    <path d="M14 3v5h5M9 12h7M9 16h7" />
  </Svg>
);
export const BatteryIcon = () => (
  <Svg>
    <rect x="2" y="7" width="17" height="10" rx="2" />
    <path d="M21 10v4M7 12h6" />
  </Svg>
);
export const BrainIcon = () => (
  <Svg>
    <path d="M9 4a3 3 0 0 0-3 3 3 3 0 0 0-2 5 3 3 0 0 0 2 5 3 3 0 0 0 5 2V4a3 3 0 0 0-2 0z" />
    <path d="M15 4a3 3 0 0 1 3 3 3 3 0 0 1 2 5 3 3 0 0 1-2 5 3 3 0 0 1-5 2V4a3 3 0 0 1 2 0z" />
  </Svg>
);

export function LockIcon() {
  return (
    <svg
      className="lock"
      viewBox="0 0 24 24"
      width="24"
      height="24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      aria-hidden="true"
      focusable="false"
    >
      <rect x="5" y="11" width="14" height="9" rx="2" />
      <path d="M8 11V8a4 4 0 0 1 8 0v3" />
    </svg>
  );
}
