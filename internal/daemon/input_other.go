//go:build !linux

package daemon

import (
	"context"
	"io"
)

func readInteractiveInput(context.Context, io.Reader, io.Writer, func(string), *toolStatusBridge) (bool, error) {
	return false, nil
}
