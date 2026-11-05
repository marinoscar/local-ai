// Side-effect CSS imports (the bundled @fontsource fonts) have no type declarations.
declare module '*.css';

// The shell bridge (src/preload/shell.ts); absent when a page is opened in a plain browser.
interface Window {
  localaiDesktop?: import('../shared/types').LocalaiDesktop;
  // The PIN window's own bridge (src/preload/pin.ts).
  localaiPin?: import('../shared/types').LocalaiPin;
}
