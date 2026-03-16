//go:build windows

package server

import (
	"syscall"
	"time"
)

// systemBootTime returns the system boot time on Windows
// using GetTickCount64. Since GetTickCount64 returns
// milliseconds since boot, we subtract from now.
func systemBootTime() (time.Time, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetTickCount64")
	ms, _, _ := proc.Call()
	uptime := time.Duration(ms) * time.Millisecond
	return time.Now().Add(-uptime), nil
}
