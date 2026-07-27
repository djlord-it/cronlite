//go:build integration

package webadmin

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/djlord-it/cronlite/internal/domain"
)

func TestIntegrationConcurrentBootstrapCreatesExactlyOneKey(t *testing.T) {
	rt := newIntegrationRuntime(t)

	const (
		rounds     = 3
		contenders = 8
	)
	for round := 0; round < rounds; round++ {
		rt.reset(t)

		ready := make(chan struct{}, contenders)
		start := make(chan struct{})
		results := make(chan error, contenders)
		for contender := 0; contender < contenders; contender++ {
			go func(round, contender int) {
				ready <- struct{}{}
				<-start

				ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
				defer cancel()
				_, err := rt.service.BootstrapFirstAPIKey(
					ctx,
					fmt.Sprintf("bootstrap-round-%d-team-%d", round, contender),
					"owner",
				)
				results <- err
			}(round, contender)
		}

		for contender := 0; contender < contenders; contender++ {
			<-ready
		}
		close(start)

		roundErrors := make([]error, 0, contenders)
		for contender := 0; contender < contenders; contender++ {
			roundErrors = append(roundErrors, <-results)
		}

		var succeeded, rejected int
		var unexpected []error
		for _, err := range roundErrors {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, domain.ErrBootstrapAlreadyCompleted):
				rejected++
			default:
				unexpected = append(unexpected, err)
			}
		}

		if len(unexpected) != 0 {
			t.Fatalf("bootstrap round %d returned %d unexpected errors: %v", round, len(unexpected), unexpected)
		}
		if succeeded != 1 || rejected != contenders-1 {
			t.Fatalf(
				"bootstrap round %d: succeeded=%d rejected=%d, want 1 and %d",
				round,
				succeeded,
				rejected,
				contenders-1,
			)
		}
		if got := countRows(t, rt.db, `SELECT COUNT(*) FROM api_keys`); got != 1 {
			t.Fatalf("bootstrap round %d API key count = %d, want 1", round, got)
		}
	}

	t.Log("ADMIN_INTEGRATION_OK")
}
