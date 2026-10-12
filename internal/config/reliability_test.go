package config

import (
	"strconv"
	"strings"
	"testing"
)

func TestValidateRejectsUnsafeDatabaseSettings(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"DB_OP_TIMEOUT", "invalid"}, {"DB_OP_TIMEOUT", "0s"}, {"DB_OP_TIMEOUT", "-1s"},
		{"DB_POLL_INTERVAL", "invalid"}, {"DB_POLL_INTERVAL", "0s"}, {"DB_POLL_INTERVAL", "-1s"},
		{"DB_MAX_OPEN_CONNS", "1"},
		{"DISPATCHER_WORKERS", strconv.Itoa(int(^uint(0) >> 1))},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/test")
			t.Setenv("DISPATCH_MODE", "db")
			t.Setenv(tc.name, tc.value)
			err := Validate(Load())
			if err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("unsafe %s=%s accepted: %v", tc.name, tc.value, err)
			}
		})
	}
}

func TestParseIntRejectsOverflow(t *testing.T) {
	if _, err := parseInt("18446744073709551617"); err == nil {
		t.Fatal("integer overflow silently wrapped")
	}
	maximum := int(^uint(0) >> 1)
	if got, err := parseInt(strconv.Itoa(maximum)); err != nil || got != maximum {
		t.Fatalf("valid maximum integer rejected: %d %v", got, err)
	}
}
