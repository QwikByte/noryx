import type { ReactNode } from "react"

// Release notes and the changelogs of plugins are Markdown. Its common parts become React
// elements, so that no HTML of the text reaches the page; everything else stays text. Only http
// and https links are followed, and no images are loaded.

const span =
  /`([^`]+)`|\*\*(.+?)\*\*|\*([^*\s][^*]*?)\*|\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)|(https?:\/\/[^\s<>]*[^\s<>.,:;!?)])/g
const heading = /^#{1,6}\s+/
const item = /^\s*(?:[-*+]|(\d+)[.)])\s+(.*)$/
const rule = /^\s*([-*_])(\s*\1){2,}\s*$/

function Inline({ text }: { text: string }) {
  const parts: ReactNode[] = []
  let at = 0
  for (const m of text.matchAll(span)) {
    const [all, code, strong, em, label, href, url] = m
    parts.push(text.slice(at, m.index))
    if (code) parts.push(<code key={m.index} className="rounded bg-muted px-1 font-mono text-xs">{code}</code>)
    else if (strong) parts.push(<strong key={m.index}><Inline text={strong} /></strong>)
    else if (em) parts.push(<em key={m.index}><Inline text={em} /></em>)
    else
      parts.push(
        <a key={m.index} href={href ?? url} target="_blank" rel="noreferrer" className="font-medium text-primary underline-offset-4 hover:underline">
          {label ?? url}
        </a>,
      )
    at = m.index + all.length
  }
  parts.push(text.slice(at))
  return parts
}

// An HTML comment, or one that isn't closed, which hides the rest.
const comment = /<!--[\s\S]*?(?:-->|$)/g

/** Drops HTML comments, e.g. the hints of the pull request template, until removing one can't form another. */
function withoutComments(text: string) {
  for (let last = ""; last !== text; ) [last, text] = [text, text.replace(comment, "")]
  return text
}

const kind = (line: string) =>
  !line.trim() ? "blank" : line.startsWith("```") ? "code" : heading.test(line) ? "heading" : rule.test(line) ? "rule" : item.test(line) ? "item" : "text"

/** Renders Markdown of an untrusted source, such as release notes. */
export function Markdown({ text }: { text: string }) {
  const lines = withoutComments(text).split(/\r?\n/)
  const blocks: ReactNode[] = []
  let i = 0
  const take = (k: string) => {
    const start = i
    while (i < lines.length && kind(lines[i]) === k) i++
    return lines.slice(start, i)
  }
  while (i < lines.length) {
    const key = i
    switch (kind(lines[i])) {
      case "blank":
        i++
        break
      case "code": {
        const end = lines.findIndex((l, j) => j > i && l.startsWith("```"))
        const code = lines.slice(i + 1, end < 0 ? undefined : end)
        blocks.push(<pre key={key} className="overflow-x-auto rounded-lg bg-muted p-3 font-mono text-xs">{code.join("\n")}</pre>)
        i = end < 0 ? lines.length : end + 1
        break
      }
      case "heading":
        blocks.push(<h3 key={key} className="font-heading font-semibold"><Inline text={lines[i++].replace(heading, "")} /></h3>)
        break
      case "rule":
        blocks.push(<hr key={key} />)
        i++
        break
      case "item": {
        const List = item.exec(lines[i])![1] ? "ol" : "ul"
        blocks.push(
          <List key={key} className={`space-y-1 pl-5 ${List === "ol" ? "list-decimal" : "list-disc"}`}>
            {take("item").map((l, j) => <li key={j}><Inline text={item.exec(l)![2]} /></li>)}
          </List>,
        )
        break
      }
      default:
        blocks.push(<p key={key}><Inline text={take("text").map((l) => l.trim()).join(" ")} /></p>)
    }
  }
  return <div className="space-y-3 text-sm leading-relaxed">{blocks}</div>
}
