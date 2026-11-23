//go:build windows

package pvf

import (
	"os"

	"golang.org/x/sys/windows"
)

const stableSource = true

func openSource(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_RANDOM_ACCESS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open PVF", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
