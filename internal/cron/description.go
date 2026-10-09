package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	robcron "github.com/robfig/cron/v3"
)

// Describe uses the scheduler's parser and expanded fields, so names, lists,
// ranges, steps, and cron's day-of-month/day-of-week OR rule stay consistent.
// Times describe local clock time, not elapsed intervals across DST changes.
func Describe(expression string) string {
	parsed, err := NewParser().parseSpec(expression)
	if err != nil {
		return "Invalid schedule"
	}
	s, ok := parsed.(*robcron.SpecSchedule)
	if !ok {
		return "Custom schedule"
	}
	minute, hour := fieldValues(s.Minute, 0, 59), fieldValues(s.Hour, 0, 23)
	dom, dow, month := fieldValues(s.Dom, 1, 31), fieldValues(s.Dow, 0, 6), fieldValues(s.Month, 1, 12)
	day := describeDays(s.Dom, s.Dow, dom, dow)
	text := describeClock(minute, hour)
	if day == "" && len(minute) == 1 && len(hour) == 1 {
		text = "Daily at " + fmt.Sprintf("%02d:%02d", hour[0], minute[0])
	} else if day != "" {
		text += " " + day
	}
	if len(month) != 12 {
		text += " in " + describeValues(month, func(n int) string { return time.Month(n).String() })
	}
	// CRON_TZ/TZ prefixes override the job timezone in robfig's parser.
	if s.Location != time.Local {
		text += " (" + s.Location.String() + ")"
	}
	return text
}

func fieldValues(mask uint64, first, last int) []int {
	var values []int
	for n := first; n <= last; n++ {
		if mask&(uint64(1)<<uint(n)) != 0 {
			values = append(values, n)
		}
	}
	return values
}

// Only label a step as "every N" when it starts at zero and divides the full
// clock field. */7 minutes resets at the hour, so it is not every seven minutes.
func regularStep(values []int, size int) int {
	if len(values) < 2 || values[0] != 0 {
		return 0
	}
	step := values[1]
	if size%step != 0 || len(values) != size/step {
		return 0
	}
	for i, n := range values {
		if n != i*step {
			return 0
		}
	}
	return step
}

func describeClock(minute, hour []int) string {
	if len(hour) == 24 {
		if len(minute) == 60 {
			return "Every minute"
		}
		if step := regularStep(minute, 60); step > 0 {
			return fmt.Sprintf("Every %d minutes", step)
		}
		if len(minute) == 1 {
			if minute[0] == 0 {
				return "Every hour"
			}
			return fmt.Sprintf("Every hour at minute %d", minute[0])
		}
		return "At minutes " + describeValues(minute, strconv.Itoa) + " of every hour"
	}
	if len(minute) == 1 {
		if minute[0] == 0 {
			if step := regularStep(hour, 24); step > 0 {
				return fmt.Sprintf("Every %d hours", step)
			}
		}
		var times []string
		for _, h := range hour {
			times = append(times, fmt.Sprintf("%02d:%02d", h, minute[0]))
		}
		return "At " + strings.Join(times, ", ")
	}
	hours := describeValues(hour, func(n int) string { return fmt.Sprintf("%02d", n) })
	if len(minute) == 60 {
		return "Every minute during hours " + hours
	}
	return "At minutes " + describeValues(minute, strconv.Itoa) + " past hours " + hours
}

func describeDays(domMask, dowMask uint64, dom, dow []int) string {
	// robfig treats starred day fields as AND, otherwise as OR.
	and := (domMask|dowMask)&(uint64(1)<<63) != 0
	if (!and && (len(dom) == 31 || len(dow) == 7)) || (len(dom) == 31 && len(dow) == 7) {
		return ""
	}
	var parts []string
	if len(dom) != 31 {
		label := "days "
		if len(dom) == 1 {
			label = "day "
		}
		parts = append(parts, label+describeValues(dom, strconv.Itoa)+" of the month")
	}
	if len(dow) != 7 {
		parts = append(parts, describeValues(dow, func(n int) string { return time.Weekday(n).String() }))
	}
	join := " or "
	if and {
		join = " and "
	}
	return "on " + strings.Join(parts, join)
}

func describeValues(values []int, label func(int) string) string {
	var parts []string
	for i := 0; i < len(values); {
		j := i
		for j+1 < len(values) && values[j+1] == values[j]+1 {
			j++
		}
		if j-i >= 2 {
			parts = append(parts, label(values[i])+"–"+label(values[j]))
		} else {
			for n := i; n <= j; n++ {
				parts = append(parts, label(values[n]))
			}
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}
