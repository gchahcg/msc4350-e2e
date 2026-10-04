// Copyright (c) 2026 gchahcg
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Command register creates a user on the test homeserver (used by run.sh register).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gchahcg/msc4350-e2e/internal/e2e"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: register <username> <password>")
		os.Exit(2)
	}
	hs := e2e.Env("E2E_HS", "http://127.0.0.1:18008")
	if err := e2e.RegisterUser(context.Background(), hs, os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "register failed:", err)
		os.Exit(1)
	}
	fmt.Println("registered", e2e.UserID(os.Args[1]))
}
