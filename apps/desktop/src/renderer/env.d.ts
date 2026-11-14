// Side-effect CSS imports (the bundled @fontsource fonts) have no type declarations.
declare module '*.css';

// Images bundled by Vite (the Corona mark of the top bar): the import is the built file's URL, served from localai://app.
declare module '*.svg' {
  const url: string;
  export default url;
}

// The shell bridge (src/preload/shell.ts); absent when a page is opened in a plain browser.
interface Window {
  localaiDesktop?: import('../shared/types').LocalaiDesktop;
  // The PIN window's own bridge (src/preload/pin.ts).
  localaiPin?: import('../shared/types').LocalaiPin;
}
