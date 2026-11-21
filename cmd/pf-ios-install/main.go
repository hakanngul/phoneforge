// pf-ios-install installs a signed .app (or .ipa) onto a connected iOS device
// using go-ios zipconduit. Usage: pf-ios-install <udid> <path/to/App.app>
package main

import (
	"fmt"
	"os"

	"phoneforge/internal/iosbridge"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: pf-ios-install <udid> <path/to/App.app>")
		os.Exit(2)
	}
	udid, app := os.Args[1], os.Args[2]
	if err := iosbridge.InstallApp(udid, app); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("installed", app)
}
