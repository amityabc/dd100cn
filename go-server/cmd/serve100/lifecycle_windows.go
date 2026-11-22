//go:build windows

package main

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"syscall"
)

func isReparse(f os.FileInfo) bool {
	d, ok := f.Sys().(*syscall.Win32FileAttributeData)
	return f.Mode()&os.ModeSymlink != 0 || ok && d.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func processIdentity(pid int) (string, int64, error) {
	if pid <= 0 {
		return "", 0, errors.New("invalid process identity")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", 0, err
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err = windows.GetExitCodeProcess(h, &code); err != nil {
		return "", 0, err
	}
	if code != 259 {
		return "", 0, errors.New("process has exited")
	}
	var created, exited, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return "", 0, err
	}
	var size uint32 = 32768
	buf := make([]uint16, size)
	if err = windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", 0, err
	}
	ticks := int64(created.HighDateTime)<<32 | int64(created.LowDateTime)
	return windows.UTF16ToString(buf[:size]), ticks + 504911232000000000, nil
}

func replaceFile(src, dst string) error {
	return moveLifecycleFile(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func publishNewFile(src, dst string) error {
	return moveLifecycleFile(src, dst, windows.MOVEFILE_WRITE_THROUGH)
}

func moveLifecycleFile(src, dst string, flags uint32) error {
	a, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	b, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(a, b, flags)
}
