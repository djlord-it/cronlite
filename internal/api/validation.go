package api

import (
	"time"

	"github.com/djlord-it/cronlite/internal/webhookurl"
)

func validateTimezone(tz string) error {
	_, err := time.LoadLocation(tz)
	return err
}

func validateWebhookURL(rawURL string) error {
	return webhookurl.Validate(rawURL)
}
