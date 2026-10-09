package cron

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

type Parser struct {
	parser cron.Parser
}

func NewParser() *Parser {
	return &Parser{
		parser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
	}
}

func (p *Parser) Parse(expression string, timezone string) (Schedule, error) {
	sched, err := p.parseSpec(expression)
	if err != nil {
		return nil, fmt.Errorf("parse cron: %w", err)
	}

	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone: %w", err)
	}

	return &schedule{sched: sched, loc: loc}, nil
}

func (p *Parser) parseSpec(expression string) (cron.Schedule, error) {
	// robfig v3 slices the timezone prefix at its first space. Without a space
	// (e.g. "TZ="), malformed user input would panic before returning an error.
	if strings.HasPrefix(expression, "TZ=") || strings.HasPrefix(expression, "CRON_TZ=") {
		space, equal := strings.IndexByte(expression, ' '), strings.IndexByte(expression, '=')
		if space <= equal+1 {
			return nil, fmt.Errorf("invalid cron timezone prefix")
		}
	}
	return p.parser.Parse(expression)
}

type Schedule interface {
	Next(after time.Time) time.Time
}

type schedule struct {
	sched cron.Schedule
	loc   *time.Location
}

func (s *schedule) Next(after time.Time) time.Time {
	return s.sched.Next(after.In(s.loc))
}
