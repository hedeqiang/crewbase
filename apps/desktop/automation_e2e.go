//go:build e2e

// Built only with -tags e2e (never in releases): MyGo's automation socket,
// which MYGO_AUTOMATION=/path/to.sock turns on, for driving the app in
// end-to-end checks (see docs/desktop.md).
package main

import _ "github.com/egoist/mygo/plugins/automation"
