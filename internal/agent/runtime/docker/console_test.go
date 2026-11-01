package docker

import (
	"errors"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	mcnet "github.com/QwikByte/noryx/internal/agent/network"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

func TestReloaded(t *testing.T) {
	velocity, _ := mcnet.ProxyOf(noryxv1.ServerType_SERVER_TYPE_VELOCITY)
	bungee, _ := mcnet.ProxyOf(noryxv1.ServerType_SERVER_TYPE_BUNGEECORD)
	for _, tt := range []struct {
		name  string
		proxy mcnet.Proxy
		lines []string // as the consoles of Velocity 4.2 and BungeeCord answered
		err   string   // empty if the proxy reloaded
	}{
		{"velocity", velocity, []string{"[09:03:40 INFO]: Done (0.69s)!", "[09:03:40 INFO]: Velocity configuration successfully reloaded."}, ""},
		{"velocity fails", velocity, []string{
			"[09:03:42 ERROR]: Fallback server nope is not registered in your configuration!",
			"[09:03:42 INFO]: Unable to reload your Velocity configuration. Check the console for more details.",
		}, "Fallback server nope is not registered in your configuration!"},
		{"bungeecord", bungee, []string{
			"\x1b[m>greload09:03:50 [\x1b[0;34;1mINFO\x1b[m] Closing listener [id: 0x3c3742ff, L:/0.0.0.0:25577]",
			"\x1b[m09:03:50 [\x1b[0;34;1mINFO\x1b[m] \x1b[21m\x1b[0;31;1mBungeeCord has been reloaded. This is NOT advisable and you will not be supported with any issues that arise! Please restart BungeeCord ASAP.",
		}, ""},
		{"bungeecord fails", bungee, []string{
			"\x1b[m>> 09:04:30 [\x1b[0;34;1mINFO\x1b[m] \x1b[0;31;1mAn internal error occurred whilst executing this command, please check the console log for details.",
			"\x1b[m> 09:04:30 [\x1b[0;33;1mWARNING\x1b[m] Error in dispatching command: greload",
			"java.lang.IllegalArgumentException: Server nope (priority 0) is not defined",
			"\tat com.google.common.base.Preconditions.checkArgument(Preconditions.java:413)",
		}, "Server nope (priority 0) is not defined"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			answer, done, err := reloaded(tt.proxy), false, error(nil)
			for _, line := range tt.lines {
				if done, err = answer(line); done {
					break
				}
			}
			switch {
			case !done:
				t.Fatal("the answer wasn't recognised")
			case tt.err == "" && err != nil, tt.err != "" && (!errors.Is(err, runtime.ErrReload) || err.Error() != runtime.ErrReload.Error()+": "+tt.err):
				t.Fatalf("err = %v, want %q", err, tt.err)
			}
		})
	}
}

func TestSent(t *testing.T) {
	for _, tt := range []struct {
		name  string
		lines []string // as BungeeCord answers send commands, without and with its modules (here of its fork Waterfall 615)
		err   error
		msg   string
	}{
		{"sent", []string{"[14:49:12 INFO]: Attempting to send 0 players to lobby"}, nil, ""},
		{"no module", []string{"[14:48:53 INFO]: Command not found"}, runtime.ErrNoSend, runtime.ErrNoSend.Error()},
		{"offline", []string{"[14:49:08 INFO]: That user is not online."}, runtime.ErrNotSent, runtime.ErrNotSent.Error() + ": That user is not online."},
		{"unknown server", []string{"[14:49:10 INFO]: The specified server does not exist."}, runtime.ErrNotSent, runtime.ErrNotSent.Error() + ": The specified server does not exist."},
		{"coloured", []string{"\x1b[m> 10:12:01 [\x1b[0;34;1mINFO\x1b[m] \x1b[0;31;1mThat user is not online."}, runtime.ErrNotSent, runtime.ErrNotSent.Error() + ": That user is not online."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			done, err := false, error(nil)
			for _, l := range tt.lines {
				if done, err = sent(l); done {
					break
				}
			}
			switch {
			case !done:
				t.Fatal("the answer wasn't recognised")
			case !errors.Is(err, tt.err), err != nil && err.Error() != tt.msg:
				t.Fatalf("err = %v, want %q", err, tt.msg)
			}
		})
	}
	if done, _ := sent("[14:49:16 INFO]: Closing listener [id: 0xe1ef5df3, L:/0.0.0.0:25577]"); done {
		t.Fatal("other lines end the answer")
	}
}

func TestVelocitySent(t *testing.T) {
	answer := velocitySent("Alex", "lobby")
	for line, want := range map[string]string{ // velocity.command.player-not-found and server-does-not-exist
		"[10:12:01 INFO]: The specified player Alex does not exist.":  "The specified player Alex does not exist.",
		"[10:12:01 INFO]: The specified server lobby does not exist.": "The specified server lobby does not exist.",
	} {
		if done, err := answer(line); !done || !errors.Is(err, runtime.ErrNotSent) || err.Error() != runtime.ErrNotSent.Error()+": "+want {
			t.Errorf("answer(%q) = %v, %v; want %q", line, done, err, want)
		}
	}
	// Answers to other sends, and other lines, don't end it.
	for _, line := range []string{"[10:12:01 INFO]: The specified player Steve does not exist.", "[10:12:01 INFO]: Alex has connected"} {
		if done, _ := answer(line); done {
			t.Errorf("answer(%q) ended the answer", line)
		}
	}
}
