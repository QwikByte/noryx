import i18next from "i18next"

// The tests see the panel as a browser that prefers American English does, before any module reads its
// languages, and texts as their English keys with their values filled in.
Object.defineProperty(navigator, "languages", { value: ["en-US"], configurable: true })

await i18next.init({
  lng: "en",
  resources: {},
  keySeparator: false,
  nsSeparator: false,
  returnEmptyString: false,
  interpolation: { escapeValue: false },
})
