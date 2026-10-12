package logging

import "testing"

// Commands that may carry a password are logged without what follows the word that introduces
// it; others as they are.
func TestCommand(t *testing.T) {
	for command, want := range map[string]string{
		"say Hello":                             "say Hello",
		"op Steve":                              "op Steve",
		"authme register Steve hunter2":         "authme register …",
		"authme changepassword Steve hunter2":   "authme changepassword …",
		"/login hunter2":                        "/login …",
		"l hunter2":                             "l …",
		"nlogin register Steve hunter2 hunter2": "nlogin register …",
		"librelogin user pass-change Steve pw":  "librelogin user pass-change …",
		"tebex secret 0123456789abcdef":         "tebex secret …",
		"server command abc authme cp Steve pw": "server command abc authme cp …",
		"register":                              "register",
		"":                                      "",
	} {
		if got := Command(command).Value.String(); got != want {
			t.Errorf("Command(%q) = %q, want %q", command, got, want)
		}
	}
}
