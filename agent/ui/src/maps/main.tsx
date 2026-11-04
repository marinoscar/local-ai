import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { initLang } from "./lang";
import "./maps.css";

// Entry of the offline map viewer, served at /maps/ (companion-services.md section 4.4.3).
initLang();
createRoot(document.getElementById("root") as HTMLElement).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
