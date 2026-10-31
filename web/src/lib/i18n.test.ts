import { afterEach, describe, expect, it, vi } from "vitest"
import { languageName, languages, resolve } from "./i18n"

/** Lets the browser prefer languages, in this order. */
const prefer = (...tags: string[]) => Object.defineProperty(navigator, "languages", { value: tags, configurable: true })

/** Keeps values in the browser's storage, e.g. the panel's default language. */
const store = (values: Record<string, string>) => vi.stubGlobal("localStorage", { getItem: (key: string) => values[key] ?? null })

afterEach(() => prefer("en-US"))

describe("resolve", () => {
  it("knows the languages with a file of translations", () => {
    expect(languages).toEqual(["en", "de"])
  })

  it("shows the chosen language", () => {
    prefer("en-US")
    store({ "noryx-default-language": "en" })
    expect(resolve("de")).toBe("de")
  })

  it("shows the panel's default for a language it doesn't have", () => {
    prefer("en-US")
    store({ "noryx-default-language": "de" })
    expect(resolve("fr")).toBe("de")
    expect(resolve("")).toBe("de")
  })

  it("shows the first of the browser's languages it has, fully or by its language alone", () => {
    store({ "noryx-default-language": "xx" })
    prefer("fr-FR", "de-AT", "en-US")
    expect(resolve("")).toBe("de")
    prefer("DE")
    expect(resolve("")).toBe("de")
    prefer("en-GB", "de")
    expect(resolve("")).toBe("en")
  })

  it("falls back to English", () => {
    store({})
    prefer("fr-FR", "it")
    expect(resolve("")).toBe("en")
    prefer()
    expect(resolve("")).toBe("en")
  })

  it("follows the browser when its storage is blocked", () => {
    vi.stubGlobal("localStorage", {
      getItem: () => {
        throw new DOMException("The operation is insecure.", "SecurityError")
      },
    })
    prefer("de-DE")
    expect(resolve("")).toBe("de")
  })
})

describe("languageName", () => {
  it("names a language in itself", () => {
    expect(languageName("de")).toBe("Deutsch")
    expect(languageName("en")).toBe("English")
  })
})
