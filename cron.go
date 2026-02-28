package worker

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronMatches checks if the given cron expression matches the current time.
// Supports standard 5-field cron: minute hour day-of-month month day-of-week.
// Fields support: *, exact numbers, */N step values.
func cronMatches(expr string, t time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}

	values := []int{t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())}

	for i, field := range fields {
		if !fieldMatches(field, values[i]) {
			return false
		}
	}
	return true
}

func fieldMatches(field string, value int) bool {
	if field == "*" {
		return true
	}

	// Step: */N
	if strings.HasPrefix(field, "*/") {
		step, err := strconv.Atoi(field[2:])
		if err != nil || step <= 0 {
			return false
		}
		return value%step == 0
	}

	// Comma-separated values
	for _, part := range strings.Split(field, ",") {
		// Range: N-M
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			low, err1 := strconv.Atoi(bounds[0])
			high, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil {
				continue
			}
			if value >= low && value <= high {
				return true
			}
			continue
		}

		// Exact value
		n, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		if n == value {
			return true
		}
	}

	return false
}

// ValidateCron checks if a cron expression is a valid 5-field format.
// Fields: minute(0-59) hour(0-23) day(1-31) month(1-12) weekday(0-6).
func ValidateCron(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("cron expression must have exactly 5 fields (minute hour day month weekday)")
	}

	limits := []struct {
		name string
		min  int
		max  int
	}{
		{"minute", 0, 59},
		{"hour", 0, 23},
		{"day", 1, 31},
		{"month", 1, 12},
		{"weekday", 0, 6},
	}

	for i, field := range fields {
		if err := validateCronField(field, limits[i].min, limits[i].max, limits[i].name); err != nil {
			return err
		}
	}
	return nil
}

func validateCronField(field string, min, max int, name string) error {
	if field == "*" {
		return nil
	}
	if strings.HasPrefix(field, "*/") {
		step, err := strconv.Atoi(field[2:])
		if err != nil || step <= 0 {
			return fmt.Errorf("invalid step value in %s field: %s", name, field)
		}
		return nil
	}
	for _, part := range strings.Split(field, ",") {
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			low, err1 := strconv.Atoi(bounds[0])
			high, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil {
				return fmt.Errorf("invalid range in %s field: %s", name, part)
			}
			if low < min || high > max || low > high {
				return fmt.Errorf("range out of bounds in %s field: %s (allowed %d-%d)", name, part, min, max)
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return fmt.Errorf("invalid value in %s field: %s", name, part)
		}
		if n < min || n > max {
			return fmt.Errorf("value out of range in %s field: %d (allowed %d-%d)", name, n, min, max)
		}
	}
	return nil
}
