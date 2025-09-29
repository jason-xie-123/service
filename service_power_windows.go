package service

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type SleepStateType int

const (
	SleepUnknown SleepStateType = iota
	SleepS3S4
	SleepS0
)

type PowerEventAction int

const (
	EventUnknown PowerEventAction = iota
	EventSleep
	EventWake
)

type WakeSource int

const (
	WakeSourceUnknown WakeSource = iota
	WakeSourceUser
	WakeSourceAutomatic
)

type PowerEventType struct {
	SleepState SleepStateType
	Event      PowerEventAction
	Source     WakeSource
}

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW                   = user32.NewProc("RegisterClassExW")
	procCreateWindowExW                    = user32.NewProc("CreateWindowExW")
	procDefWindowProcW                     = user32.NewProc("DefWindowProcW")
	procGetMessageW                        = user32.NewProc("GetMessageW")
	procTranslateMessage                   = user32.NewProc("TranslateMessage")
	procDispatchMessageW                   = user32.NewProc("DispatchMessageW")
	procRegisterPowerSettingNotification   = user32.NewProc("RegisterPowerSettingNotification")
	procUnregisterPowerSettingNotification = user32.NewProc("UnregisterPowerSettingNotification")
	procGetModuleHandleW                   = kernel32.NewProc("GetModuleHandleW")
)

type HPOWERNOTIFY windows.Handle

const DEVICE_NOTIFY_WINDOW_HANDLE = 0x00000000
const WM_POWERBROADCAST = 0x0218
const PBT_POWERSETTINGCHANGE = 0x8013

type WNDCLASSEX struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type POWERBROADCAST_SETTING struct {
	PowerSetting windows.GUID
	DataLength   uint32
	Data         [1]byte
}

// 示例 Modern Standby GUID
var GUID_MONITOR_POWER_ON = windows.GUID{0x02731015, 0x4510, 0x4526, [8]byte{0x99, 0xe6, 0xe5, 0xa1, 0x3c, 0xf3, 0x22, 0x03}}

func getModuleHandle() (windows.Handle, error) {
	h, _, err := procGetModuleHandleW.Call(0)
	if err != nil && err.Error() != "The operation completed successfully." {
		return 0, err
	}
	return windows.Handle(h), nil
}

func runHiddenWindowLoop(powerChan chan<- PowerEventType) {
	WriteTempLog("runHiddenWindowLoop 001")
	hInstance, err := getModuleHandle()
	if err != nil {
		WriteTempLog(fmt.Sprintf("getModuleHandle failed: %v", err))
		return
	}
	WriteTempLog("runHiddenWindowLoop 002")
	wc := WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEX{})),
		LpszClassName: syscall.StringToUTF16Ptr("HiddenWindow"),
		HInstance:     hInstance,
		LpfnWndProc: syscall.NewCallback(func(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
			WriteTempLog(fmt.Sprintf("runHiddenWindowLoop 1000: msg=%d wParam=%d", msg, wParam))
			if msg == WM_POWERBROADCAST && wParam == PBT_POWERSETTINGCHANGE {
				ps := (*POWERBROADCAST_SETTING)(unsafe.Pointer(lParam))
				if ps.PowerSetting == GUID_MONITOR_POWER_ON {
					data := *(*uint32)(unsafe.Pointer(&ps.Data[0]))
					switch data {
					case 0:
						powerChan <- PowerEventType{SleepState: SleepS0, Event: EventSleep, Source: WakeSourceUnknown}
					case 1:
						powerChan <- PowerEventType{SleepState: SleepS0, Event: EventWake, Source: WakeSourceUnknown}
					default:
						powerChan <- PowerEventType{SleepState: SleepS0, Event: EventUnknown, Source: WakeSourceUnknown}
					}
				}
			}
			r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
			return r

		}),
	}
	WriteTempLog("runHiddenWindowLoop 003")

	classAtom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if err != nil && err.Error() != "The operation completed successfully." {
		WriteTempLog(fmt.Sprintf("RegisterClassExW failed: %v", err))
		return
	}
	WriteTempLog("runHiddenWindowLoop 004")
	// hwnd, _, _ := procCreateWindowExW.Call(
	// 	0,
	// 	classAtom,
	// 	uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("HiddenWindow"))),
	// 	0,
	// 	0, 0, 0, 0,
	// 	0, 0, uintptr(hInstance), 0,
	// )

	var hwnd uintptr
	if !interactive {
		// 服务模式下，用 Message-only window
		hwnd, _, err = procCreateWindowExW.Call(
			0,
			classAtom,
			uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("HiddenWindow"))),
			0,
			0, 0, 0, 0,
			uintptr(windows.Handle(^uintptr(2))), // HWND_MESSAGE
			0, uintptr(hInstance), 0,
		)
		if err != nil && err.Error() != "The operation completed successfully." {
			WriteTempLog(fmt.Sprintf("CreateWindowExW failed: %v", err))
			return
		}
		WriteTempLog(fmt.Sprintf("Created Message-only window hwnd=0x%x", hwnd))
	} else {
		// CLI 模式下，用普通隐藏窗口
		hwnd, _, err = procCreateWindowExW.Call(
			0,
			classAtom,
			uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("HiddenWindow"))),
			0,
			0, 0, 0, 0,
			0, 0, uintptr(hInstance), 0,
		)
		if err != nil && err.Error() != "The operation completed successfully." {
			WriteTempLog(fmt.Sprintf("CreateWindowExW failed: %v", err))
			return
		}
		WriteTempLog(fmt.Sprintf("Created Hidden window hwnd=0x%x", hwnd))
	}
	WriteTempLog(fmt.Sprintf("Created window hwnd=0x%x", hwnd))

	hNotify, _, err := procRegisterPowerSettingNotification.Call(
		hwnd,
		uintptr(unsafe.Pointer(&GUID_MONITOR_POWER_ON)),
		DEVICE_NOTIFY_WINDOW_HANDLE,
	)
	if err != nil && err.Error() != "The operation completed successfully." {
		WriteTempLog(fmt.Sprintf("RegisterPowerSettingNotification failed: %v", err))
		return
	}
	WriteTempLog("runHiddenWindowLoop 006")
	if hNotify != 0 {
		WriteTempLog("runHiddenWindowLoop 007")
		defer procUnregisterPowerSettingNotification.Call(hNotify)
	}

	var msg struct {
		hwnd    windows.Handle
		message uint32
		wParam  uintptr
		lParam  uintptr
		time    uint32
		ptX     int32
		ptY     int32
	}
	WriteTempLog("runHiddenWindowLoop 008")

	for {
		ret, _, err := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if err != nil && err.Error() != "The operation completed successfully." {
			WriteTempLog(fmt.Sprintf("GetMessageW failed: %v", err))
			break
		}
		if ret == 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		WriteTempLog("runHiddenWindowLoop 009")
	}
}

func runCLI() {
	powerChan := make(chan PowerEventType, 5)
	WriteTempLog("runCLI Starting... runHiddenWindowLoop")
	go runHiddenWindowLoop(powerChan)
	WriteTempLog("runCLI Starting... runHiddenWindowLoop... Done")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	WriteTempLog("runCLI Running...")
	for {
		select {
		case <-sig:
			WriteTempLog("runCLI Exiting on signal")
			return
		case evt := <-powerChan:
			WriteTempLog(fmt.Sprintf("Power Event(runCLI): SleepState=%d, Event=%d, Source=%d", evt.SleepState, evt.Event, evt.Source))
		}
	}
}
