//go:build !darwin

package macos

import "github.com/hedeqiang/crewbase/capabilities/computer"

// newPlatform: operating apps is macOS only (for now); the plugin then
// provides nothing.
func newPlatform() (platform, error) { return nil, computer.ErrUnsupported }
