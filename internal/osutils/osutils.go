// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package osutils

import (
	"errors"
	"fmt"
	"os"
)

func checkStatExists(filename string, check func(stat os.FileInfo) error) (bool, error) {
	stat, err := os.Stat(filename)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		return false, nil
	}
	if err := check(stat); err != nil {
		return false, err
	}
	return true, nil
}

func RegularFileExists(filename string) (bool, error) {
	return checkStatExists(filename, func(stat os.FileInfo) error {
		if !stat.Mode().IsRegular() {
			return fmt.Errorf("no regular file at %s", filename)
		}
		return nil
	})
}
