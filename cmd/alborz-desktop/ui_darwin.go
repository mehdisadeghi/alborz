package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "ui_darwin.h"
*/
import "C"

import (
	"context"
	"os/exec"
	"unsafe"
)

var nativeActions uiActions
var nativeContext context.Context
var nativeCancel context.CancelFunc

func runUI(ctx context.Context, actions uiActions) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	nativeActions = actions
	nativeContext = ctx
	nativeCancel = cancel
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			C.alborz_stop()
		case <-finished:
		}
	}()
	C.alborz_run()
	return nil
}

func showError(message string) {
	text := C.CString(message)
	defer C.free(unsafe.Pointer(text))
	C.alborz_error(text)
}

func openBrowser(url string) error { return exec.Command("/usr/bin/open", url).Run() }

//export alborz_open
func alborz_open() {
	if nativeContext.Err() != nil {
		return
	}
	if err := nativeActions.Open(); err != nil {
		showError(err.Error())
	}
}

//export alborz_quit
func alborz_quit() {
	nativeCancel()
	if err := nativeActions.Close(); err != nil {
		showError(err.Error())
	}
}

//export alborz_code
func alborz_code() *C.char { return C.CString(nativeActions.SetupCode()) }
