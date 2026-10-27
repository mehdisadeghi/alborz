//go:build unix

package main

import (
	"os"
	"syscall"
)

func processSignals() []os.Signal {
	return []os.Signal{syscall.SIGUSR1, os.Interrupt, syscall.SIGTERM}
}

func isReloadSignal(sig os.Signal) bool { return sig == syscall.SIGUSR1 }
