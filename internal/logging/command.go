package logging

import (
	"log/slog"
	"regexp"
	"strings"
)

// secretWord matches a word of a command after which a password or another secret may follow,
// e.g. in "authme register Steve <password>", "login <password>", "librelogin user pass-change
// Steve <password>" or "tebex secret <key>". The panel's prompts keep commands with such a word
// out of their histories by the same rule (web/src/lib/use-command-history.ts).
var secretWord = regexp.MustCompile(`(?i)^/?(l|log|login|reg|register|unregister|unreg|cp|pw|.*pass.*|.*secret.*|.*token.*|.*key)$`)

// Command returns the attribute of a console command for the log. What follows a word after
// which a secret may follow is left out, so that the log keeps who ran which command but no
// password that a plugin takes on the console.
func Command(command string) slog.Attr {
	words := strings.Fields(command)
	for i, w := range words[:max(len(words)-1, 0)] {
		if secretWord.MatchString(w) {
			command = strings.Join(words[:i+1], " ") + " …"
			break
		}
	}
	return slog.String("command", command)
}
