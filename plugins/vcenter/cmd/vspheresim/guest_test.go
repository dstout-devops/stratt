package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// hostnameSafe guards a shell boundary, so it gets the attention a shell boundary
// deserves. The VM name is chosen by the CLIENT and is interpolated into a command
// line the simulator hands to `bash -c`; anything that is not obviously a hostname
// label must be refused rather than escaped or trimmed into shape.
//
// The rejected cases below are written as bare metacharacters on purpose. The
// property under test is "no shell metacharacter survives", not "these particular
// exploits are blocked" — a denylist of realistic payloads would pass while the next
// unlisted character walked straight through.
func TestHostnameSafe(t *testing.T) {
	for _, n := range []string{"web-01", "WEB-01", "a", "web01", strings.Repeat("a", 63)} {
		if !hostnameSafe(n) {
			t.Errorf("%q is a legal RFC 1123 hostname label and must pass", n)
		}
	}

	// Shape violations: legal characters, illegal placement or length.
	for _, n := range []string{"", "-web", "web-", "web_01", "web.01", strings.Repeat("a", 64)} {
		if hostnameSafe(n) {
			t.Errorf("%q is not a hostname label and must be refused", n)
		}
	}

	// Every character that means something to a shell, each appended to an otherwise
	// valid name so only the metacharacter is under test.
	for _, meta := range []string{
		";", "&", "|", "$", "`", "(", ")", "<", ">", "\\", "\"", "'", "*", "?", "[", "]",
		"{", "}", "~", "!", "#", "\n", "\r", "\t", " ", "/", ":", "=", ",",
	} {
		n := "web" + meta + "x"
		if hostnameSafe(n) {
			t.Errorf("%q contains the shell metacharacter %q and must be refused — it reaches bash -c", n, meta)
		}
	}

	// A name that is entirely a docker flag would be read as an option, not a value.
	for _, n := range []string{"--privileged", "-v"} {
		if hostnameSafe(n) {
			t.Errorf("%q would be parsed as a docker option and must be refused", n)
		}
	}
}

// The RUN.container value is the simulator's "opts and image" contract. These pin
// what we put in it, because getting it wrong is silent: the simulator would create
// a container with the wrong command or no hostname and simply report no guest.
func TestBackingArgv(t *testing.T) {
	g := GuestBacking{Image: "alpine:3", Args: []string{"sleep", "infinity"}}

	t.Run("without a domain the image stands alone", func(t *testing.T) {
		got := decode(t, g.optsAndImage("web-01"), g.Args)
		if got[0] != "alpine:3" {
			t.Errorf("args[0] = %q, want the bare image", got[0])
		}
		if strings.Join(got[1:], " ") != "sleep infinity" {
			t.Errorf("command = %v, want the guest args", got[1:])
		}
	})

	t.Run("with a domain the guest gets a DOTTED hostname", func(t *testing.T) {
		g.Domain = "dev.stratt.test"
		got := decode(t, g.optsAndImage("WEB-01"), g.Args)
		if got[0] != "--hostname web-01.dev.stratt.test alpine:3" {
			t.Errorf("args[0] = %q; the name must be lowercased and dotted, or a client's "+
				"name-first path can never be exercised against this simulator", got[0])
		}
	})

	t.Run("an unsafe name yields NO hostname rather than a sanitised one", func(t *testing.T) {
		g.Domain = "dev.stratt.test"
		got := decode(t, g.optsAndImage("web;x"), g.Args)
		if strings.Contains(got[0], "--hostname") {
			t.Fatalf("args[0] = %q — an unsafe name must be refused outright, never cleaned up and used", got[0])
		}
		if got[0] != "alpine:3" {
			t.Errorf("args[0] = %q, want the bare image", got[0])
		}
	})
}

func decode(t *testing.T, optsAndImage string, args []string) []string {
	t.Helper()
	raw, err := json.Marshal(append([]string{optsAndImage}, args...))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
