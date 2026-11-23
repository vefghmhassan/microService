//go:build !linux

package updater

import "errors"

func freeDiskMB(string) (uint64, error) { return 0, errors.New("not supported") }
