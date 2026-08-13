//go:build windows

package main

import (
	"log"

	"fileecosystem/internal/desktop"
)

func main() {
	if err := desktop.Run(); err != nil {
		log.Print(err)
	}
}
