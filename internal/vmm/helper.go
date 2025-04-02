// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package vmm

import (
	"fmt"
	"net/http"
)

func validateStatus(status int) error {
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return nil
	default:
		return fmt.Errorf("invalid status: %d", status)
	}
}
