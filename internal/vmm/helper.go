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
