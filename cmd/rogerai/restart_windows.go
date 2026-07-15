//go:build windows

package main

import "fmt"

// execRestart: Windows has no exec(2); the freshly installed binary runs on the
// next launch, so just say that.
func execRestart() error {
	fmt.Println("upgraded - start roger again to run the new version")
	return nil
}
