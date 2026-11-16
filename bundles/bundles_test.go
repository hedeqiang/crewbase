package bundles

import (
	"testing"

	"github.com/hedeqiang/crewbase/kernel"
)

func has(b kernel.Bundle, name string) bool {
	for _, e := range b.Entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

// The computer capability needs permissions given to Crewbase.app: the
// desktop profile has it, base and server do not.
func TestComputerDesktopOnly(t *testing.T) {
	const name = "@crewbase/computer-macos"
	if !has(Desktop, name) {
		t.Fatal("desktop bundle lacks computer-macos")
	}
	if has(Base, name) || has(Server, name) {
		t.Fatal("computer-macos must not load in base or server")
	}
}
