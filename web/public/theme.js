// Applies the saved colour theme, accent, density, width and motion before the first paint, so that the panel doesn't flash.
// It is a file rather than an inline script because the Content Security Policy forbids inline scripts.
try {
  const root = document.documentElement
  const theme = localStorage.getItem("theme")
  const dark = theme === "dark" || (theme !== "light" && matchMedia("(prefers-color-scheme: dark)").matches)
  root.classList.toggle("dark", dark)
  root.dataset.accent = localStorage.getItem("noryx-accent") ?? "emerald"
  root.dataset.density = localStorage.getItem("noryx-density") ?? "comfortable"
  root.dataset.width = localStorage.getItem("noryx-width") ?? "limited"
  root.dataset.motion = localStorage.getItem("noryx-motion") ?? "system"
} catch {
  // Storage can be blocked; the light theme is used then.
}
