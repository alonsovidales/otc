// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/session"
)

// initOwnerPassword sets the device's owner password from stdin, once:
// what the first sign-in does (session.New derives the key from it and
// stores the validator), run by install.sh with the password the setup
// wizard collected, so the owner never has to choose it later.
func initOwnerPassword(d *dao.Dao) int {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(os.Stderr, "init-owner-password: no password on stdin")
		return 2
	}
	pw := strings.TrimRight(line, "\r\n")
	if len(pw) < 8 {
		fmt.Fprintln(os.Stderr, "init-owner-password: the password must have 8 characters or more")
		return 2
	}
	defined, err := d.IsSecretDefined()
	if err != nil {
		fmt.Fprintln(os.Stderr, "init-owner-password:", err)
		return 1
	}
	if defined {
		fmt.Println("init-owner-password: this device already has a password - left as it is")
		return 0
	}
	if _, err := session.New("setup", pw, true, d); err != nil {
		fmt.Fprintln(os.Stderr, "init-owner-password:", err)
		return 1
	}
	fmt.Println("init-owner-password: owner password set")
	return 0
}
