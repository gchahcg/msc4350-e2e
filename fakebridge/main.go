// fakebridge is a minimal bridgev2 network connector used to exercise MSC4350
// ghost impersonation end-to-end. It has no remote network: messages are
// injected through a small HTTP endpoint (see inject.go).
package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"
)

func main() {
	m := mxmain.BridgeMain{
		Name:        "msc4350-fakebridge",
		URL:         "https://example.invalid/msc4350-fakebridge",
		Description: "Fake bridge for MSC4350 end-to-end tests.",
		Version:     "0.0.0",
		Connector:   &Connector{},
	}
	m.Run()
}
