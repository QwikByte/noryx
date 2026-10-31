import { defineConfig, recommendedAcceptedAttributes } from 'i18next-cli'

// The panel's English texts are the keys of their translations. `npm run i18n` lists them in
// src/locales/en.json and adds new ones to the other languages untranslated (empty, so that they
// show in English), which `npm run lint` checks. msg marks texts outside of components, which
// these translate with t.
export default defineConfig({
  locales: ['en', 'de'],
  extract: {
    input: ['src/**/*.{ts,tsx}'],
    ignore: ['src/**/*.test.{ts,tsx}', 'src/test-setup.ts'],
    output: 'src/locales/{{language}}.json',
    defaultNS: false,
    keySeparator: false,
    nsSeparator: false,
    functions: ['t', '*.t', 'msg'],
    extractFromComments: false,
  },
  // Texts in the panel's own components count too, e.g. in buttons and in the props of dialogs.
  lint: {
    acceptedTags: 'all',
    acceptedAttributes: [...recommendedAcceptedAttributes, 'action', 'submitLabel', 'everything'],
  },
})
