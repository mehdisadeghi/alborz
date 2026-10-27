package main

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32         = windows.NewLazySystemDLL("user32.dll")
	shell32        = windows.NewLazySystemDLL("shell32.dll")
	kernel32       = windows.NewLazySystemDLL("kernel32.dll")
	defWindowProc  = user32.NewProc("DefWindowProcW")
	postMessage    = user32.NewProc("PostMessageW")
	destroyWindow  = user32.NewProc("DestroyWindow")
	notifyIcon     = shell32.NewProc("Shell_NotifyIconW")
	nativeActions  uiActions
	nativeContext  context.Context
	trayIcon       notificationIcon
	taskbarCreated uint32
)

const (
	wmClose   = 0x0010
	wmDestroy = 0x0002
	wmCommand = 0x0111
	wmTray    = 0x8001
	wmReady   = 0x8002
	menuOpen  = 1
	menuCopy  = 2
	menuQuit  = 3
)

type point struct{ X, Y int32 }
type message struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          point
	Private        uint32
}
type windowClass struct {
	Size, Style                        uint32
	WindowProc                         uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}
type notificationIcon struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Timeout             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                windows.GUID
	BalloonIcon         uintptr
}

func wide(s string) []uint16 {
	return append(utf16.Encode([]rune(strings.ReplaceAll(s, "\x00", ""))), 0)
}

func runUI(ctx context.Context, actions uiActions) error {
	nativeActions = actions
	nativeContext = ctx
	className := wide("AlborzDesktop")
	instance, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	icon, _, err := user32.NewProc("LoadIconW").Call(instance, 1)
	if icon == 0 {
		return fmt.Errorf("load Alborz icon: %w", err)
	}
	class := windowClass{Instance: instance, WindowProc: windows.NewCallback(windowProc), ClassName: &className[0], Icon: icon}
	class.Size = uint32(unsafe.Sizeof(class))
	if result, _, err := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class))); result == 0 {
		return fmt.Errorf("register desktop window: %w", err)
	}
	defer user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(&className[0])), instance)
	window, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(&className[0])), uintptr(unsafe.Pointer(&className[0])), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if window == 0 {
		return fmt.Errorf("create desktop window: %w", err)
	}
	defer destroyWindow.Call(window)
	trayIcon = notificationIcon{Window: window, ID: 1, Flags: 1 | 2 | 4, Callback: wmTray, Icon: icon}
	trayIcon.Size = uint32(unsafe.Sizeof(trayIcon))
	copy(trayIcon.Tip[:], wide("Alborz"))
	if result, _, err := notifyIcon.Call(0, uintptr(unsafe.Pointer(&trayIcon))); result == 0 {
		return fmt.Errorf("create notification icon: %w", err)
	}
	trayIcon.Timeout = 4 // NOTIFYICON_VERSION_4 enables keyboard activation.
	notifyIcon.Call(4, uintptr(unsafe.Pointer(&trayIcon)))
	defer notifyIcon.Call(2, uintptr(unsafe.Pointer(&trayIcon)))
	taskbarName := wide("TaskbarCreated")
	registered, _, _ := user32.NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(&taskbarName[0])))
	taskbarCreated = uint32(registered)
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			postMessage.Call(window, wmClose, 0, 0)
		case <-finished:
		}
	}()
	postMessage.Call(window, wmReady, 0, 0)
	getMessage := user32.NewProc("GetMessageW")
	translate := user32.NewProc("TranslateMessage")
	dispatch := user32.NewProc("DispatchMessageW")
	for {
		var msg message
		result, _, err := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == -1 {
			return fmt.Errorf("desktop message loop: %w", err)
		}
		if result == 0 {
			return nil
		}
		translate.Call(uintptr(unsafe.Pointer(&msg)))
		dispatch.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func windowProc(window uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmReady:
		if nativeContext.Err() != nil {
			return 0
		}
		if code := nativeActions.SetupCode(); code != "" {
			body := "Enter this one-time code in the setup page:\n\n" + code + "\n\nAlborz stays in the notification area while you use it in your browser. Copy the code to the clipboard now?"
			if messageBox(window, body, "Set up Alborz", 0x24) == 6 {
				copySetupCode(window)
			}
		}
		openAlborz()
		return 0
	case wmTray:
		switch uint32(lParam) & 0xffff {
		case 0x0400, 0x0401:
			openAlborz() // Mouse or keyboard activation.
		case 0x0205, 0x007b:
			showMenu(window) // Right-click or keyboard menu.
		}
		return 0
	case wmCommand:
		switch wParam & 0xffff {
		case menuOpen:
			openAlborz()
		case menuCopy:
			copySetupCode(window)
		case menuQuit:
			destroyWindow.Call(window)
		}
		return 0
	case wmClose:
		destroyWindow.Call(window)
		return 0
	case wmDestroy:
		user32.NewProc("PostQuitMessage").Call(0)
		return 0
	}
	if taskbarCreated != 0 && msg == taskbarCreated {
		notifyIcon.Call(0, uintptr(unsafe.Pointer(&trayIcon)))
		notifyIcon.Call(4, uintptr(unsafe.Pointer(&trayIcon)))
		return 0
	}
	result, _, _ := defWindowProc.Call(window, uintptr(msg), wParam, lParam)
	return result
}

func showMenu(window uintptr) {
	menu, _, _ := user32.NewProc("CreatePopupMenu").Call()
	if menu == 0 {
		return
	}
	defer user32.NewProc("DestroyMenu").Call(menu)
	add := func(id uintptr, label string) {
		text := wide(label)
		user32.NewProc("AppendMenuW").Call(menu, 0, id, uintptr(unsafe.Pointer(&text[0])))
	}
	add(menuOpen, "Open Alborz")
	if nativeActions.SetupCode() != "" {
		add(menuCopy, "Copy setup code")
	}
	user32.NewProc("AppendMenuW").Call(menu, 0x800, 0, 0)
	add(menuQuit, "Quit Alborz")
	var pos point
	user32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&pos)))
	user32.NewProc("SetForegroundWindow").Call(window)
	user32.NewProc("TrackPopupMenu").Call(menu, 0x0002, uintptr(pos.X), uintptr(pos.Y), 0, window, 0)
	postMessage.Call(window, 0, 0, 0)
}

func openAlborz() {
	if nativeContext.Err() != nil {
		return
	}
	if err := nativeActions.Open(); err != nil {
		showError(err.Error())
	}
}

func messageBox(window uintptr, body, title string, flags uintptr) uintptr {
	text, caption := wide(body), wide(title)
	result, _, _ := user32.NewProc("MessageBoxW").Call(window, uintptr(unsafe.Pointer(&text[0])), uintptr(unsafe.Pointer(&caption[0])), flags)
	return result
}

func showError(message string) { messageBox(0, message, "Alborz could not continue", 0x10) }

func openBrowser(url string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole32 := windows.NewLazySystemDLL("ole32.dll")
	status, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 2|4)
	if int32(status) >= 0 {
		defer ole32.NewProc("CoUninitialize").Call()
	} else if uint32(status) != 0x80010106 { // This thread may already use another COM apartment.
		return fmt.Errorf("initialize browser integration: HRESULT %#x", uint32(status))
	}
	verb, target := wide("open"), wide(url)
	result, _, _ := shell32.NewProc("ShellExecuteW").Call(0, uintptr(unsafe.Pointer(&verb[0])), uintptr(unsafe.Pointer(&target[0])), 0, 0, 1)
	if result <= 32 {
		return fmt.Errorf("open browser: Windows error %d", result)
	}
	return nil
}

func copySetupCode(window uintptr) {
	code := nativeActions.SetupCode()
	if code == "" {
		return
	}
	if err := setClipboard(window, code); err != nil {
		showError(err.Error())
	}
}

func setClipboard(window uintptr, text string) error {
	if result, _, err := user32.NewProc("OpenClipboard").Call(window); result == 0 {
		return fmt.Errorf("open clipboard: %w", err)
	}
	defer user32.NewProc("CloseClipboard").Call()
	content := wide(text)
	size := uintptr(len(content) * 2)
	handle, _, err := kernel32.NewProc("GlobalAlloc").Call(0x0002, size)
	if handle == 0 {
		return fmt.Errorf("allocate clipboard: %w", err)
	}
	owned := true
	defer func() {
		if owned {
			kernel32.NewProc("GlobalFree").Call(handle)
		}
	}()
	ptr, _, err := kernel32.NewProc("GlobalLock").Call(handle)
	if ptr == 0 {
		return fmt.Errorf("lock clipboard: %w", err)
	}
	kernel32.NewProc("RtlMoveMemory").Call(ptr, uintptr(unsafe.Pointer(&content[0])), size)
	kernel32.NewProc("GlobalUnlock").Call(handle)
	if result, _, err := user32.NewProc("EmptyClipboard").Call(); result == 0 {
		return fmt.Errorf("empty clipboard: %w", err)
	}
	if result, _, err := user32.NewProc("SetClipboardData").Call(13, handle); result == 0 {
		return fmt.Errorf("copy setup code: %w", err)
	}
	owned = false // Windows owns the memory after SetClipboardData.
	runtime.KeepAlive(content)
	return nil
}
