// Command icon-resource generates the Windows resource object consumed by the
// ecosystem-desktop package. Run it from the repository root after replacing
// internal/appicon/appicon.ico.
package main

import (
	"fmt"
	"os"

	"github.com/tc-hib/winres"
)

func main() {
	iconFile, err := os.Open("internal/appicon/appicon.ico")
	if err != nil {
		fatal(err)
	}
	defer iconFile.Close()

	icon, err := winres.LoadICO(iconFile)
	if err != nil {
		fatal(fmt.Errorf("load icon: %w", err))
	}
	resources := winres.ResourceSet{}
	if err := resources.SetIcon(winres.RT_ICON, icon); err != nil {
		fatal(fmt.Errorf("set icon resource: %w", err))
	}

	output, err := os.Create("cmd/ecosystem-desktop/appicon_windows_amd64.syso")
	if err != nil {
		fatal(err)
	}
	defer output.Close()
	if err := resources.WriteObject(output, winres.ArchAMD64); err != nil {
		fatal(fmt.Errorf("write resource: %w", err))
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
