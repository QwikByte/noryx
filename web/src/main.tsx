import "./index.css"
import { QueryClientProvider } from "@tanstack/react-query"
import { RouterProvider } from "@tanstack/react-router"
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { Motion } from "./components/motion-config"
import { TooltipProvider } from "./components/ui/tooltip"
import { setUpI18n } from "./lib/i18n"
import { queryClient, router } from "./router"

// After an update, the master no longer has the parts of the old panel that load on demand, e.g.
// the editor's languages. The panel then reloads once to get the new ones, but not again for the
// same part if that doesn't help.
window.addEventListener("vite:preloadError", (event) => {
  const key = `noryx-reload:${event.payload.message}`
  try {
    if (sessionStorage.getItem(key)) return
    sessionStorage.setItem(key, "1")
  } catch {
    return // without storage, it couldn't tell whether it reloaded already
  }
  location.reload()
})

// The panel renders once its language is chosen. A top-level await instead would split it into many more chunks.
void setUpI18n().then(() =>
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <Motion>
          <TooltipProvider delayDuration={300}>
            <RouterProvider router={router} />
          </TooltipProvider>
        </Motion>
      </QueryClientProvider>
    </StrictMode>,
  ),
)
