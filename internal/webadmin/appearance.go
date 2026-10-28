package webadmin

import (
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/djlord-it/cronlite/internal/cron"
	"github.com/djlord-it/cronlite/internal/domain"
)

// themeCookieName holds the admin colour theme chosen in Settings. Without it
// the pages follow the operating system.
const themeCookieName = "cronlite_admin_theme"

const themeCookieMaxAge = 365 * 24 * 60 * 60

// themeFromRequest returns "light" or "dark" when the viewer picked one, or ""
// to follow the operating system.
func themeFromRequest(r *http.Request) string {
	cookie, err := r.Cookie(themeCookieName)
	if err != nil {
		return ""
	}
	switch cookie.Value {
	case "light", "dark":
		return cookie.Value
	}
	return ""
}

func (h *Handler) saveAppearance(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireMutation(w, r); !ok {
		return
	}
	cookie := &http.Cookie{
		Name: themeCookieName, Path: "/admin", HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode,
	}
	switch theme := r.FormValue("theme"); theme {
	case "light", "dark":
		cookie.Value, cookie.MaxAge = theme, themeCookieMaxAge
	case "auto":
		cookie.MaxAge, cookie.Expires = -1, time.Unix(1, 0)
	default:
		http.Error(w, "invalid theme", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/admin/settings?notice=appearance", http.StatusSeeOther)
}

// nextRunText describes a job's next scheduled run relative to now, e.g.
// "in 12 min" or "in 3 h", with the exact time for a tooltip.
type nextRunText struct{ Text, Exact string }

func nextRun(now func() time.Time) func(domain.JobWithSchedule) nextRunText {
	parser := cron.NewParser()
	return func(row domain.JobWithSchedule) nextRunText {
		if !row.Job.Enabled {
			return nextRunText{Text: "Paused"}
		}
		schedule, err := parser.Parse(row.Schedule.CronExpression, row.Schedule.Timezone)
		if err != nil {
			return nextRunText{Text: "—"}
		}
		current := now()
		next := schedule.Next(current)
		if next.IsZero() {
			return nextRunText{Text: "—"}
		}
		exact := next.UTC().Format("2006-01-02 15:04:05 UTC")
		switch d := next.Sub(current); {
		case d < time.Minute:
			return nextRunText{"in under a minute", exact}
		case d < time.Hour:
			return nextRunText{fmt.Sprintf("in %d min", int(d.Minutes())), exact}
		case d < 48*time.Hour:
			return nextRunText{fmt.Sprintf("in %d h", int(d.Hours())), exact}
		default:
			return nextRunText{next.UTC().Format("Jan 2 15:04 UTC"), exact}
		}
	}
}

// icons are inline SVG paths drawn on a 24px grid with one stroke weight.
var icons = map[string]string{
	"clock":    `<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`,
	"activity": `<path d="M3 12h4l3 8 4-16 3 8h4"/>`,
	"list":     `<path d="M9 6h12M9 12h12M9 18h12M4 6h.01M4 12h.01M4 18h.01"/>`,
	"key":      `<circle cx="7.5" cy="15.5" r="4.5"/><path d="M10.7 12.3 20 3M16 7l3 3M14 9l2 2"/>`,
	"calendar": `<rect x="3" y="4.5" width="18" height="17" rx="2"/><path d="M16 2.5v4M8 2.5v4M3 10h18"/>`,
	"network":  `<circle cx="5" cy="12" r="2"/><circle cx="19" cy="5" r="2"/><circle cx="19" cy="19" r="2"/><circle cx="12" cy="12" r="2"/><path d="M7 12h3M13.5 10.5l4-4M13.5 13.5l4 4"/>`,
	"sliders":  `<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6"/>`,
	"flag":     `<path d="M5 22V4M5 4h12l-2 4 2 4H5"/>`,
	"plus":     `<path d="M12 5v14M5 12h14"/>`,
	"search":   `<circle cx="11" cy="11" r="7"/><path d="m20 20-3.6-3.6"/>`,
	"arrow":    `<path d="M5 12h14M13 6l6 6-6 6"/>`,
	"back":     `<path d="M19 12H5M11 6l-6 6 6 6"/>`,
	"chevron":  `<path d="m9 6 6 6-6 6"/>`,
	"check":    `<path d="M20 6 9 17l-5-5"/>`,
	"alert":    `<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/><path d="M12 9v4M12 17h.01"/>`,
	"info":     `<circle cx="12" cy="12" r="9"/><path d="M12 16v-4M12 8h.01"/>`,
	"pause":    `<rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/>`,
	"play":     `<path d="M7 4.5v15l12-7.5z"/>`,
	"zap":      `<path d="M13 2 4 14h7l-1 8 9-12h-7z"/>`,
	"sun":      `<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>`,
	"moon":     `<path d="M20.5 13.2A8.5 8.5 0 1 1 10.8 3.5a6.6 6.6 0 0 0 9.7 9.7z"/>`,
	"monitor":  `<rect x="2.5" y="3.5" width="19" height="13" rx="2"/><path d="M8 21h8M12 16.5V21"/>`,
	"logout":   `<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>`,
	"trash":    `<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v6M14 11v6"/>`,
	"lock":     `<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>`,
	"refresh":  `<path d="M21 12a9 9 0 1 1-2.6-6.4L21 8M21 3v5h-5"/>`,
	"edit":     `<path d="M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>`,
	"menu":     `<path d="M4 6h16M4 12h16M4 18h16"/>`,
	"bookmark": `<path d="M19 21l-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2z"/>`,
}

// icon renders a named icon. Names are template literals, never user input, so
// the markup is trusted.
func icon(name string) template.HTML {
	return template.HTML(`<svg class="icon" aria-hidden="true" focusable="false" viewBox="0 0 24 24">` + icons[name] + `</svg>`)
}
