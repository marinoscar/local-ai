/** A microphone, inline SVG (no network). Decorative: the button text carries the meaning. */
export function MicIcon() {
  return (
    <svg
      className="mic-icon"
      viewBox="0 0 24 24"
      width="64"
      height="64"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      <rect x="9" y="2" width="6" height="12" rx="3" />
      <path d="M5 11a7 7 0 0 0 14 0" />
      <path d="M12 18v4" />
      <path d="M8 22h8" />
    </svg>
  );
}
