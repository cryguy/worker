package worker

import (
	"testing"
	"time"
)

func TestCronMatches(t *testing.T) {
	tests := []struct {
		name  string
		expr  string
		time  time.Time
		match bool
	}{
		{"every minute", "* * * * *", time.Date(2024, 1, 1, 12, 30, 0, 0, time.UTC), true},
		{"exact match", "30 12 1 1 1", time.Date(2024, 1, 1, 12, 30, 0, 0, time.UTC), true},
		{"no match minute", "0 12 1 1 *", time.Date(2024, 1, 1, 12, 30, 0, 0, time.UTC), false},
		{"step */5 match", "*/5 * * * *", time.Date(2024, 1, 1, 12, 15, 0, 0, time.UTC), true},
		{"step */5 no match", "*/5 * * * *", time.Date(2024, 1, 1, 12, 13, 0, 0, time.UTC), false},
		{"range match", "0-30 * * * *", time.Date(2024, 1, 1, 12, 15, 0, 0, time.UTC), true},
		{"range no match", "0-10 * * * *", time.Date(2024, 1, 1, 12, 15, 0, 0, time.UTC), false},
		{"comma list match", "0,15,30,45 * * * *", time.Date(2024, 1, 1, 12, 15, 0, 0, time.UTC), true},
		{"comma list no match", "0,30,45 * * * *", time.Date(2024, 1, 1, 12, 15, 0, 0, time.UTC), false},
		{"invalid field count", "* * *", time.Date(2024, 1, 1, 12, 30, 0, 0, time.UTC), false},
		{"weekday Sunday=0", "* * * * 0", time.Date(2024, 1, 7, 12, 0, 0, 0, time.UTC), true},
		{"step */15 at 0", "*/15 * * * *", time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC), true},
		{"step */15 at 45", "*/15 * * * *", time.Date(2024, 1, 1, 12, 45, 0, 0, time.UTC), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cronMatches(tt.expr, tt.time)
			if got != tt.match {
				t.Errorf("cronMatches(%q, %v) = %v, want %v", tt.expr, tt.time, got, tt.match)
			}
		})
	}
}

func TestValidateCron(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"valid every minute", "* * * * *", false},
		{"valid specific", "30 12 1 1 1", false},
		{"valid step", "*/5 * * * *", false},
		{"valid range", "0-30 * * * *", false},
		{"valid comma", "0,15,30 * * * *", false},
		{"valid combo", "0,30 */2 * * 1-5", false},
		{"too few fields", "* * *", true},
		{"too many fields", "* * * * * *", true},
		{"minute out of range", "60 * * * *", true},
		{"hour out of range", "* 24 * * *", true},
		{"day out of range", "* * 32 * *", true},
		{"month out of range", "* * * 13 *", true},
		{"weekday out of range", "* * * * 7", true},
		{"invalid step", "*/0 * * * *", true},
		{"invalid value", "* abc * * *", true},
		{"invalid range reversed", "* * 10-5 * *", true},
		{"day zero", "* * 0 * *", true},
		{"month zero", "* * * 0 *", true},
		{"empty string", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCron(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCron(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
		})
	}
}

func TestFieldMatches_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value int
		want  bool
	}{
		{"wildcard", "*", 42, true},
		{"step divide evenly", "*/10", 30, true},
		{"step no match", "*/10", 33, false},
		{"step at zero", "*/15", 0, true},
		{"comma single match", "5,10,15", 10, true},
		{"comma no match", "5,10,15", 7, false},
		{"range start", "10-20", 10, true},
		{"range end", "10-20", 20, true},
		{"range middle", "10-20", 15, true},
		{"range below", "10-20", 9, false},
		{"range above", "10-20", 21, false},
		{"exact match", "42", 42, true},
		{"exact no match", "42", 43, false},
		{"invalid step", "*/0", 5, false},
		{"invalid step negative", "*/-5", 5, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fieldMatches(tt.field, tt.value)
			if got != tt.want {
				t.Errorf("fieldMatches(%q, %d) = %v, want %v", tt.field, tt.value, got, tt.want)
			}
		})
	}
}

func TestFieldMatches_CommaWithRange(t *testing.T) {
	// "1-5,10,20-25" should match 3, 10, 22 but not 7
	tests := []struct {
		value int
		want  bool
	}{
		{3, true},
		{10, true},
		{22, true},
		{7, false},
		{26, false},
		{1, true},
		{5, true},
		{20, true},
		{25, true},
	}
	for _, tt := range tests {
		got := fieldMatches("1-5,10,20-25", tt.value)
		if got != tt.want {
			t.Errorf("fieldMatches('1-5,10,20-25', %d) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestFieldMatches_InvalidRange(t *testing.T) {
	// Invalid range bounds should not match.
	if fieldMatches("abc-def", 5) {
		t.Error("fieldMatches with invalid range should return false")
	}
	// Invalid single value should not match.
	if fieldMatches("xyz", 5) {
		t.Error("fieldMatches with invalid value should return false")
	}
}

func TestFieldMatches_StepNonNumeric(t *testing.T) {
	if fieldMatches("*/abc", 5) {
		t.Error("fieldMatches with non-numeric step should return false")
	}
}

func TestCronMatches_AllFields(t *testing.T) {
	// Tuesday Jan 14, 2025 at 14:30
	// minute=30, hour=14, day=14, month=1, weekday=2 (Tuesday)
	tm := time.Date(2025, 1, 14, 14, 30, 0, 0, time.UTC)

	if !cronMatches("30 14 14 1 2", tm) {
		t.Error("exact match for all 5 fields should match")
	}
	if cronMatches("30 14 14 1 3", tm) {
		t.Error("wrong weekday should not match")
	}
	if cronMatches("31 14 14 1 2", tm) {
		t.Error("wrong minute should not match")
	}
}

func TestValidateCron_StepValues(t *testing.T) {
	tests := []struct {
		expr    string
		wantErr bool
	}{
		{"*/1 * * * *", false},
		{"*/59 * * * *", false},
		{"*/abc * * * *", true},
		{"*/-1 * * * *", true},
	}
	for _, tt := range tests {
		err := ValidateCron(tt.expr)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateCron(%q) err=%v, wantErr=%v", tt.expr, err, tt.wantErr)
		}
	}
}

func TestValidateCron_RangeEdges(t *testing.T) {
	tests := []struct {
		expr    string
		wantErr bool
	}{
		{"0-59 0-23 1-31 1-12 0-6", false}, // all ranges at limits
		{"0-0 * * * *", false},             // single-value range
		{"59-59 * * * *", false},           // single-value range at max
	}
	for _, tt := range tests {
		err := ValidateCron(tt.expr)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateCron(%q) err=%v, wantErr=%v", tt.expr, err, tt.wantErr)
		}
	}
}


// CronRunner tests have been moved to internal/workeradapter/ where CronRunner is now defined.
