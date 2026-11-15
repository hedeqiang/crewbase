import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { getLang, loadLang, setLang } from "./i18n";
import "./styles.css";
import { openSession, setState } from "./store";
import { initCompanion } from "./companion";

// Dev-only hook for the screenshot scripts.
if (import.meta.env.DEV) (window as any).__cb = { setState, openSession };


// installable companion: service worker + notification taps open the conversation
initCompanion((url) => {
  location.hash = new URL(url, location.origin).hash;
});

// the English catalog is its own chunk: fetch it before the first paint
const lang = getLang();
void loadLang(lang)
  .catch(() => {}) // falls back to Chinese strings rather than a blank page
  .then(() => {
    setLang(lang);
    createRoot(document.getElementById("root")!).render(
      <StrictMode>
        <App />
      </StrictMode>,
    );
  });
