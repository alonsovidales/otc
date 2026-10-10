// Fixture: the otc-sync command line stays English: not counted.
package main

import (
	"errors"
	"fmt"
)

func main() {
	fmt.Println("usage: otc-sync <command> [flags]")
	fmt.Printf("removed %s (nothing was deleted)\n", "x")
	_ = errors.New("no such folder here")
}
