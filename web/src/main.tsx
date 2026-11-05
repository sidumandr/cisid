// cisid web UI. Everything from the API is rendered as text by React,
// never as HTML.

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router";

import { App } from "./App";
import { applyLang } from "./lib/i18n";
import { loadMe } from "./lib/session";
import { applySavedCursor } from "./ui/cursor";
import { applySavedTheme } from "./ui/Shell";
import "./styles/app.css";
import "./styles/site.css";

applySavedTheme();
applySavedCursor();
applyLang();

// Who is signed in decides what the first page shows, so it is known first.
void loadMe().then(() => {
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </StrictMode>,
  );
});
