//go:build !windows && !linux

package clipboard

import (
	"context"
	"errors"
)

type unsupported struct{}

func System() Platform { return unsupported{} }

func (unsupported) Watch(context.Context, func(string)) error {
	return errors.New("当前平台不支持剪贴板同步")
}

func (unsupported) Write(string) error { return errors.New("当前平台不支持剪贴板同步") }
