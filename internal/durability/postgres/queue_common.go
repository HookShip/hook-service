package postgres

import (
	"fmt"
	"time"
)

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boundedDurationMicroseconds(
	name string,
	value, maximum time.Duration,
	allowZero bool,
) (int64, error) {
	if value < 0 || (!allowZero && value == 0) {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	if value > maximum {
		return 0, fmt.Errorf("%s must not exceed %s", name, maximum)
	}
	microseconds := value.Microseconds()
	if !allowZero && microseconds == 0 {
		return 0, fmt.Errorf("%s must be at least one microsecond", name)
	}
	return microseconds, nil
}
