//go:build !windows && !darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

func runUI(context.Context, uiActions) error {
	return errors.New("the desktop launcher supports Windows and macOS")
}
func showError(message string) { fmt.Fprintln(os.Stderr, message) }
func openBrowser(string) error { return errors.New("the desktop launcher supports Windows and macOS") }
