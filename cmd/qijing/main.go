//go:build windows

package main

import (
	"log"

	"github.com/kyfd/qijing/internal/desktop"
)

func main() {
	if err := desktop.Run(); err != nil {
		log.Print(err)
	}
}
