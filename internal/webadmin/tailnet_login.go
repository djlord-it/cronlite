package webadmin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/tailnet"
)

// tailnetWhoIsTimeout bounds one WhoIs lookup.
const tailnetWhoIsTimeout = 2 * time.Second

// tailnetSignIn is what Tailscale identity sign-in offers one request.
type tailnetSignIn struct {
	// Offered reports that TS_IDENTITY_LOGIN is on and the request arrived
	// on the tailnet listener. The other fields are only set when it is.
	Offered bool
	// Unavailable reports that the peer could not be identified.
	Unavailable bool
	// Tagged reports a tagged device, which belongs to no person.
	Tagged      bool
	Login       string
	DisplayName string
	// Namespaces are the namespaces the tailnet policy grants Login.
	Namespaces []string
	// BadGrant reports a malformed AdminCapability value.
	BadGrant   bool
	Capability string
}

// tailnetSignIn identifies the tailnet peer behind r. Only requests marked
// by the tailnet listener are looked up, by the connection's RemoteAddr.
func (h *Handler) tailnetSignIn(r *http.Request) tailnetSignIn {
	cfg := h.tailnet
	if !cfg.IdentityLogin || cfg.Node == nil || !tailnet.ViaTailnet(r.Context()) {
		return tailnetSignIn{}
	}
	si := tailnetSignIn{Offered: true, Capability: tailnet.AdminCapability}
	ctx, cancel := context.WithTimeout(r.Context(), tailnetWhoIsTimeout)
	defer cancel()
	id, err := cfg.Node.WhoIs(ctx, r.RemoteAddr)
	if err != nil {
		h.logger.Printf("webadmin: event=tailnet_whois_failed error=%q", err)
		si.Unavailable = true
		return si
	}
	if id.Tagged {
		si.Tagged = true
		return si
	}
	if id.LoginName == "" {
		si.Unavailable = true
		return si
	}
	si.Login, si.DisplayName = id.LoginName, id.DisplayName
	ns, err := id.AdminNamespaces()
	if err != nil {
		h.logger.Printf("webadmin: event=tailnet_grant_invalid login=%q error=%q", id.LoginName, err)
		si.BadGrant = true
		return si
	}
	si.Namespaces = ns
	return si
}

// verifyTailnetSession checks that the person who started an identity
// session is still the one making r, and still granted its namespace.
func (h *Handler) verifyTailnetSession(r *http.Request, key domain.APIKey) error {
	si := h.tailnetSignIn(r)
	var reason string
	switch {
	case !h.tailnet.IdentityLogin:
		reason = "identity_login_off"
	case !si.Offered:
		reason = "not_on_tailnet_listener"
	case si.Unavailable:
		reason = "peer_unidentified"
	case si.Tagged:
		reason = "tagged_device"
	case si.Login != key.TailnetLogin:
		reason = "identity_changed"
	case si.BadGrant:
		reason = "grant_invalid"
	case !slices.Contains(si.Namespaces, key.Namespace.String()):
		reason = "grant_removed"
	default:
		return nil
	}
	h.logger.Printf("webadmin: event=tailnet_session_ended login=%q namespace=%q reason=%s", key.TailnetLogin, key.Namespace, reason)
	return errors.New(reason)
}

func (h *Handler) tailnetLogin(w http.ResponseWriter, r *http.Request) {
	si := h.tailnetSignIn(r)
	if !si.Offered {
		http.NotFound(w, r)
		return
	}
	if !h.validPublicCSRF(r) {
		h.renderLogin(w, r, si, "The form expired. Please try again.", http.StatusForbidden)
		return
	}
	ns := r.FormValue("namespace")
	if si.Login == "" || !slices.Contains(si.Namespaces, ns) {
		h.renderLogin(w, r, si, "Your Tailscale identity may not sign in to that namespace.", http.StatusForbidden)
		return
	}
	key, err := h.service.TailnetKey(r.Context(), domain.Namespace(ns), si.Login)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if !key.Enabled {
		h.renderLogin(w, r, si, "Your Tailscale identity may not sign in to that namespace.", http.StatusForbidden)
		return
	}
	if err := h.sessions.startSession(r.Context(), w, r, key); err != nil {
		h.internalError(w, r, err)
		return
	}
	h.logger.Printf("webadmin: event=tailnet_login login=%q namespace=%q", si.Login, ns)
	h.clearPublicCSRF(w)
	http.Redirect(w, r, "/admin/jobs", http.StatusSeeOther)
}

// renderLogin re-renders the sign-in page with an error and a fresh form token.
func (h *Handler) renderLogin(w http.ResponseWriter, r *http.Request, si tailnetSignIn, msg string, status int) {
	w.Header().Set("Cache-Control", "no-store")
	h.renderStatus(w, "login", pageData{
		Title: "Sign in", Error: msg, CSRFToken: h.issuePublicCSRF(w), TailnetSignIn: si, Theme: themeFromRequest(r),
	}, status)
}
