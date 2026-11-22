//go:build !windows

package operator

import (
	"context"
)

func Serve(ctx context.Context, root string, process func(context.Context, Intent) Reply) error {
	return nil
}
func Call(ctx context.Context, root string, body []byte) (Reply, error) {
	return Reply{}, ErrNotConnected
}
