//go:build !windows

package pvf

import "os"

const stableSource = false

func openSource(path string) (*os.File, error) { return os.Open(path) }
