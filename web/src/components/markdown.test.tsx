import { renderToStaticMarkup } from "react-dom/server"
import { describe, expect, it } from "vitest"
import { Markdown } from "./markdown"

const render = (text: string) => renderToStaticMarkup(<Markdown text={text} />)

/** The HTML without the classes of its elements, which only style them. */
const plain = (text: string) => render(text).replace(/ class="[^"]*"/g, "")

/** The elements of HTML that React rendered, whose texts and attribute values it escaped. */
const elements = (html: string) =>
  [...html.matchAll(/<([a-z0-9]+)((?:\s[a-z-]+="[^"]*")*)\s*\/?>/g)].map(([, tag, attributes]) => ({
    tag,
    attributes: Object.fromEntries([...attributes.matchAll(/\s([a-z-]+)="([^"]*)"/g)].map(([, name, value]) => [name, value])),
  }))

describe("Markdown", () => {
  it("renders headings, paragraphs and rules", () => {
    expect(plain("## What's new\nThe panel\nrestarts.\n\nIt is faster.\n\n---")).toBe(
      "<div><h3>What&#x27;s new</h3><p>The panel restarts.</p><p>It is faster.</p><hr/></div>",
    )
  })

  it("renders lists, numbered or not", () => {
    expect(plain("- one\n* two\n+ three")).toBe("<div><ul><li>one</li><li>two</li><li>three</li></ul></div>")
    expect(plain("1. one\n2) two")).toBe("<div><ol><li>one</li><li>two</li></ol></div>")
  })

  it("renders bold, emphasis, code and links in a line", () => {
    expect(plain("**New:** *faster* `serve` [docs](https://example.com/docs) and https://example.com/a.")).toBe(
      "<div><p><strong>New:</strong> <em>faster</em> <code>serve</code> " +
        '<a href="https://example.com/docs" target="_blank" rel="noreferrer">docs</a> and ' +
        '<a href="https://example.com/a" target="_blank" rel="noreferrer">https://example.com/a</a>.</p></div>',
    )
  })

  it("keeps code blocks as they are, up to the end without a closing fence", () => {
    expect(plain("```\n**not bold**\n<b>x</b>\n```\nafter")).toBe("<div><pre>**not bold**\n&lt;b&gt;x&lt;/b&gt;</pre><p>after</p></div>")
    expect(plain("```\nopen")).toBe("<div><pre>open</pre></div>")
  })

  it("leaves out HTML comments, e.g. those of the pull request template", () => {
    expect(plain("before <!-- a\nhidden --> after")).toBe("<div><p>before  after</p></div>")
  })

  // Release notes come from GitHub and changelogs from Modrinth and Hangar: whatever they hold, the panel only
  // shows text, the elements above and links to http and https.
  it.each([
    "<script>alert(1)</script>",
    "<img src=x onerror=alert(1)>",
    "![logo](https://example.com/logo.png)",
    '<a href="javascript:alert(1)">x</a>',
    "[x](javascript:alert(1))",
    "[x](JavaScript:alert(1))",
    "[x]( javascript:alert(1))",
    "[x](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)",
    "[x](//example.com)",
    '[x](https://example.com/"onmouseover="alert(1))',
    "https://example.com/<script>alert(1)</script>",
    '<iframe src="https://example.com"></iframe>',
    "<svg onload=alert(1)>",
    "# <style>*{display:none}</style>",
    "- **<script>alert(1)</script>**",
    "```\n<script>alert(1)</script>\n```",
    "<scr<!-- -->ipt>alert(1)</script>",
    "<details open ontoggle=alert(1)>",
  ])("shows %j as text", (text) => {
    const html = render(text)
    for (const { tag, attributes } of elements(html)) {
      expect(["div", "p", "h3", "ul", "ol", "li", "strong", "em", "code", "pre", "hr", "a"]).toContain(tag)
      expect(Object.keys(attributes).every((a) => ["class", "href", "target", "rel"].includes(a))).toBe(true)
      if (tag === "a") expect(attributes.href).toMatch(/^https?:\/\/[^\s<>"]+$/)
    }
    // Every other < is text.
    expect(html.match(/</g)?.length).toBe(html.match(/<\/?[a-z0-9]+[\s/>]/g)?.length)
  })

  it("links images instead of loading them", () => {
    expect(plain("![logo](https://example.com/logo.png)")).toBe(
      '<div><p>!<a href="https://example.com/logo.png" target="_blank" rel="noreferrer">logo</a></p></div>',
    )
  })
})
