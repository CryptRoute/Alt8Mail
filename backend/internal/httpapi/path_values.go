package httpapi

import (
	"errors"
	"strconv"
	"strings"
)

// parseInt64Path parses a positive integer path segment.
func parseInt64Path(raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 1 {
		return 0, errors.New("invalid version")
	}
	return value, nil
}
