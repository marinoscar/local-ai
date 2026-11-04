import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { initLang } from "./i18n/i18n";
import { initScale } from "./prefs";
import "./styles/portal.css";

initLang();
initScale();

const root = document.getElementById("root");
if (root) {
  createRoot(root).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
}
