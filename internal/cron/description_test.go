package cron

import (
	"strings"
	"testing"
)

func TestDescribe(t *testing.T) {
	for _, tc := range []struct{ expression, want string }{
		{"* * * * *", "Every minute"},
		{"*/5 * * * *", "Every 5 minutes"},
		{"0/5 * * * *", "Every 5 minutes"},
		{"*/15 * * * *", "Every 15 minutes"},
		{"0 * * * *", "Every hour"},
		{"15 * * * *", "Every hour at minute 15"},
		{"0 */2 * * *", "Every 2 hours"},
		{"30 */6 * * *", "At 00:30, 06:30, 12:30, 18:30"},
		{"0 9 * * *", "Daily at 09:00"},
		{"30 2 * * *", "Daily at 02:30"},
		{"0 9 * * 1-5", "At 09:00 on Monday–Friday"},
		{"0 9 * * MON-FRI", "At 09:00 on Monday–Friday"},
		{"0 12 15 * *", "At 12:00 on day 15 of the month"},
		{"0 0 1 JAN *", "At 00:00 on day 1 of the month in January"},
		{"0 9 1 * MON", "At 09:00 on day 1 of the month or Monday"},
		{"0 9 1-31 * MON", "Daily at 09:00"},
		{"0 9 1 * 0-6", "Daily at 09:00"},
		{"0 9 */2 * MON", "At 09:00 on days 1, 3, 5, 7, 9, 11, 13, 15, 17, 19, 21, 23, 25, 27, 29, 31 of the month or Monday"},
		{"*/7 * * * *", "At minutes 0, 7, 14, 21, 28, 35, 42, 49, 56 of every hour"},
		{"5/15 * * * *", "At minutes 5, 20, 35, 50 of every hour"},
		{"0,15 9-11 * MAR-MAY MON,WED,FRI", "At minutes 0, 15 past hours 09–11 on Monday, Wednesday, Friday in March–May"},
		{"CRON_TZ=America/New_York 0 9 * * *", "Daily at 09:00 (America/New_York)"},
		{"0 0 31 2 *", "At 00:00 on day 31 of the month in February"},
		{"bad", "Invalid schedule"},
		{"@daily", "Invalid schedule"},
		{"TZ=", "Invalid schedule"},
		{"CRON_TZ=UTC", "Invalid schedule"},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			if got := Describe(tc.expression); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParserRejectsIncompleteTimezonePrefix(t *testing.T) {
	for _, expression := range []string{"TZ=", "TZ=UTC", "CRON_TZ=", "CRON_TZ=UTC", "TZ= * * * * *"} {
		if _, err := NewParser().Parse(expression, "UTC"); err == nil {
			t.Fatalf("accepted malformed cron %q", expression)
		}
	}
}

func FuzzDescribe(f *testing.F) {
	for _, expression := range []string{"*/5 * * * *", "0 9 1 * MON", "", "CRON_TZ=UTC * * * * *", "* * * * *"} {
		f.Add(expression)
	}
	f.Fuzz(func(t *testing.T, expression string) {
		text := Describe(expression)
		if text == "" || strings.Contains(text, "<") {
			t.Fatalf("invalid description %q", text)
		}
	})
}
