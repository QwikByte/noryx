#!/usr/bin/env bash
# Writes the notes of a release in Markdown: the "Release notes" sections of the pull requests merged
# since the previous release, grouped by their kind. A pull request without the section shows its title.
# The first release, which has no tag before it, introduces Noryx instead.
#
#   scripts/release-notes.sh v1.2.0 > notes.md
#
# Needs the tag and its history in the clone, jq, and gh signed in, e.g. with GH_TOKEN.
set -Eeuo pipefail

tag=${1:?Usage: $0 <tag>}
# A release covers everything since the previous release, a pre-release since the previous tag.
[[ $tag == *-* ]] && exclude=() || exclude=(--exclude '*-*')
prev=$(git describe --tags --abbrev=0 --match 'v*' ${exclude[@]+"${exclude[@]}"} "$tag^" 2>/dev/null) || prev=
repo="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-QwikByte/noryx}"

if [ -z "$prev" ]; then
  cat <<EOF
The first release of Noryx, which manages Minecraft servers and whole networks from one admin panel.

Read [how to install it]($repo/blob/$tag/docs/installation.md) and [what it can do]($repo/blob/$tag/README.md).
EOF
  exit
fi

# Pull requests merged into the main line, as merge commits or squashed.
prs=$(git log --first-parent --reverse --format=%s "$prev..$tag" |
  sed -nE 's/^Merge pull request #([0-9]+) .*/\1/p; s/.* \(#([0-9]+)\)$/\1/p')

for pr in $prs; do
  gh pr view "$pr" --json author,title,body |
    jq -r '"P\t\(.author.login)\t\(.title)", (.body // "" | gsub("\r"; "") | split("\n")[] | "B\t\(.)")'
done | awk -F '\t' '
  function add(kind, text) { notes[kind] = notes[kind] "- " text "\n" }
  function finish() {
    if (author == "dependabot[bot]") deps = 1
    else if (author != "" && !found) add("Other changes", title)
  }
  $1 == "P" { finish(); author = $2; title = substr($0, length($2) + 4); found = on = comment = 0; next }
  { line = substr($0, 3) }
  comment || line ~ /<!--/ { comment = line !~ /-->/; next }
  line ~ /^##[ \t]/ { on = tolower(line) ~ /^##[ \t]+release notes[ \t]*$/; found = found || on; next }
  on && sub(/^[ \t]*[-*][ \t]+/, "", line) {
    kind = "Other changes"
    if (line ~ /^(Security|New|Improved|Fixed):/) { kind = substr(line, 1, index(line, ":") - 1); sub(/^[^:]*:[ \t]*/, "", line) }
    if (line != "" && tolower(line) !~ /^(none|n\/a)\.?$/) add(kind, line)
  }
  END {
    finish()
    if (deps) add("Other changes", "Updated dependencies.")
    split("Security New Improved Fixed", kinds, " "); kinds[5] = "Other changes"
    for (i = 1; i <= 5; i++) if (kinds[i] in notes) { printf "%s### %s\n\n%s", sep, kinds[i], notes[kinds[i]]; sep = "\n" }
    if (!sep) print "This release only has internal changes."
  }'

printf '\n**Full changelog:** [%s...%s](%s/compare/%s...%s)\n' "$prev" "$tag" "$repo" "$prev" "$tag"
