import { expect, test } from "@playwright/test"

// TestPanelInBrowser in internal/e2e passes the account of an administrator and a running server of its fake node.
const { NORYX_PANEL_USERNAME: username = "", NORYX_PANEL_PASSWORD: password = "", NORYX_PANEL_SERVER: server = "" } =
  process.env

test("signs in, opens the overview and the console of a server", async ({ page }) => {
  // Errors of scripts and violations of the Content Security Policy fail the test; failed requests, e.g. with 401
  // before signing in, are expected.
  const errors: string[] = []
  page.on("pageerror", (error) => errors.push(error.message))
  page.on("console", (message) => {
    if (message.type() === "error" && !message.text().startsWith("Failed to load resource")) errors.push(message.text())
  })

  await page.goto("/")
  await expect(page).toHaveURL(/\/login\?redirect=/)
  await page.getByLabel("Username").fill(username)
  await page.getByLabel("Password").fill(password)
  await page.getByRole("button", { name: "Sign in" }).click()
  await expect(page).toHaveTitle(/^Overview · /)

  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Servers" }).click()
  await expect(page).toHaveTitle(/^Servers · /)
  await page.getByRole("link", { name: server, exact: true }).first().click()
  await expect(page).toHaveTitle(new RegExp(`^${server} · Servers · `))
  const output = page.getByRole("log", { name: `Console output of ${server}` })
  await expect(output).toContainText("[INFO]: Done")

  const prompt = page.getByPlaceholder("Type a command, e.g. say Hello")
  await prompt.fill("say Hello")
  await prompt.press("Enter")
  await expect(output).toContainText("ran say Hello")

  // The master serves the panel at the addresses of its pages too.
  await page.reload()
  await expect(output).toContainText("[INFO]: Done")
  expect(errors).toEqual([])
})
