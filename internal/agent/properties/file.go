package properties

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// entry is a logical line of a .properties file: one or more physical lines, joined
// by a backslash at the end of all but the last.
type entry struct {
	raw   string // as in the file, including line breaks
	key   string // empty for comments and blank lines
	value string
}

// parse reads a file in the format of java.util.Properties.
func parse(data string) []entry {
	var entries []entry
	lines := strings.SplitAfter(data, "\n")
	for i := 0; i < len(lines) && lines[i] != ""; i++ {
		raw := lines[i]
		logical := strings.TrimLeft(strings.TrimRight(raw, "\r\n"), " \t\f")
		if logical == "" || logical[0] == '#' || logical[0] == '!' {
			entries = append(entries, entry{raw: raw})
			continue
		}
		for continues(logical) && i+1 < len(lines) && lines[i+1] != "" {
			i++
			raw += lines[i]
			logical = logical[:len(logical)-1] + strings.TrimLeft(strings.TrimRight(lines[i], "\r\n"), " \t\f")
		}
		key, value := split(logical)
		entries = append(entries, entry{raw: raw, key: unescape(key), value: unescape(value)})
	}
	return entries
}

// Entries returns the keys and values of a file in the format of java.util.Properties, as
// Java reads them: lines that end with a backslash continue on the next.
func Entries(data string) iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		for _, e := range parse(data) {
			if (e.key != "" || e.value != "") && !yield(e.key, e.value) { // not a comment
				return
			}
		}
	}
}

// continues reports whether a line ends with an odd number of backslashes.
func continues(line string) bool {
	n := len(line) - len(strings.TrimRight(line, "\\"))
	return n%2 == 1
}

// split separates the key, which ends at an unescaped '=', ':' or whitespace.
func split(line string) (key, value string) {
	end := len(line)
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' {
			i++
		} else if strings.IndexByte("=: \t\f", line[i]) >= 0 {
			end = i
			break
		}
	}
	rest := strings.TrimLeft(line[end:], " \t\f")
	if rest != "" && (rest[0] == '=' || rest[0] == ':') {
		rest = strings.TrimLeft(rest[1:], " \t\f")
	}
	return line[:end], rest
}

func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var units []uint16 // \u escapes are UTF-16 code units, e.g. surrogate pairs for emoji
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) {
			i++
			switch r = runes[i]; {
			case r == 'u' && i+4 < len(runes):
				if unit, err := strconv.ParseUint(string(runes[i+1:i+5]), 16, 16); err == nil {
					units = append(units, uint16(unit))
					i += 4
					continue
				}
			case strings.ContainsRune("tnrf", r):
				r = rune("\t\n\r\f"[strings.IndexRune("tnrf", r)])
			}
		}
		units = utf16.AppendRune(units, r)
	}
	return string(utf16.Decode(units))
}

// escape formats a key or value like java.util.Properties.store, with characters
// outside of ASCII as \u escapes, which all Minecraft versions read.
func escape(s string, key bool) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == ' ' && (key || i == 0):
			b.WriteString(`\ `)
		case strings.ContainsRune("\\=:#!", r):
			b.WriteByte('\\')
			b.WriteRune(r)
		case strings.ContainsRune("\t\n\r\f", r):
			b.WriteByte('\\')
			b.WriteByte("tnrf"[strings.IndexRune("\t\n\r\f", r)])
		case r < 0x20 || r > 0x7e:
			for _, unit := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%04X`, unit)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// update sets properties, keeping comments, order and the formatting of other lines.
// New properties are appended.
func update(entries []entry, changes map[string]string) string {
	var b strings.Builder
	written := map[string]bool{}
	for _, e := range entries {
		value, changed := changes[e.key]
		switch {
		case e.key == "" || !changed:
			b.WriteString(e.raw)
		case !written[e.key]: // a repeated key is written once, where it first appeared
			b.WriteString(escape(e.key, true) + "=" + escape(value, false) + "\n")
			written[e.key] = true
		}
	}
	if s := b.String(); s != "" && !strings.HasSuffix(s, "\n") {
		b.WriteString("\n")
	}
	for _, key := range slices.Sorted(maps.Keys(changes)) {
		if !written[key] {
			b.WriteString(escape(key, true) + "=" + escape(changes[key], false) + "\n")
		}
	}
	return b.String()
}
