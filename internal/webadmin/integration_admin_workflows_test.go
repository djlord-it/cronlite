//go:build integration

package webadmin

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestIntegrationUIKeyLifecycleAndNamespaceIsolation(t *testing.T) {
	rt := newIntegrationRuntime(t)
	owner := mustBootstrap(t, rt, "workspace-a")
	other := mustCreateKey(t, rt, "workspace-b")
	server := newIntegrationServer(t, rt)
	client := newIntegrationClient(t)
	loginIntegrationClient(t, client, server.URL, owner.PlaintextToken)
	csrf := authenticatedCSRF(t, client, server.URL)
	for _, form := range []url.Values{{"label": {"worker"}}, {"csrf_token": {csrf}, "label": {" "}}} {
		response, _ := integrationPostForm(t, client, server.URL+"/admin/keys", form)
		if response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("invalid creation status %d", response.StatusCode)
		}
	}
	if n := countRows(t, rt.db, "SELECT COUNT(*) FROM api_keys"); n != 2 {
		t.Fatalf("invalid forms wrote keys: %d", n)
	}
	response, body := integrationPostForm(t, client, server.URL+"/admin/keys", url.Values{"csrf_token": {csrf}, "label": {"production-worker"}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("create status %d", response.StatusCode)
	}
	token := regexp.MustCompile(`ec_[a-f0-9]{64}`).FindString(body)
	if token == "" {
		t.Fatal("new secret not shown")
	}
	var keyID string
	integrationScanRow(t, rt.db, "SELECT id FROM api_keys WHERE label=$1", []any{"production-worker"}, &keyID)
	response, body = integrationGet(t, client, server.URL+"/admin/keys")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "production-worker") || strings.Contains(body, token) || strings.Contains(body, other.Key.ID.String()) {
		t.Fatal("key listing leaked secrets or namespace, or missed new key")
	}
	replacementClient := newIntegrationClient(t)
	loginIntegrationClient(t, replacementClient, server.URL, token)
	for _, target := range []struct {
		id     string
		status int
	}{{owner.Key.ID.String(), http.StatusConflict}, {other.Key.ID.String(), http.StatusNotFound}, {keyID, http.StatusSeeOther}} {
		response, _ = integrationPostForm(t, client, server.URL+"/admin/keys/"+target.id+"/delete", url.Values{"csrf_token": {csrf}})
		if response.StatusCode != target.status {
			t.Fatalf("revoke status %d, want %d", response.StatusCode, target.status)
		}
	}
	response, _ = integrationGet(t, replacementClient, server.URL+"/admin/jobs")
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/admin/login" {
		t.Fatal("revoked key still authenticated")
	}
	if n := countRows(t, rt.db, "SELECT COUNT(*) FROM api_keys"); n != 2 {
		t.Fatalf("wrong remaining key count %d", n)
	}
	t.Log("UI key creation, one-time secret, namespace isolation, self-revocation guard, and session revocation verified")
}

func TestIntegrationUIScheduleAndAcknowledgement(t *testing.T) {
	rt := newIntegrationRuntime(t)
	owner := mustBootstrap(t, rt, "workspace-a")
	other := mustCreateKey(t, rt, "workspace-b")
	server := newIntegrationServer(t, rt)
	client := newIntegrationClient(t)
	loginIntegrationClient(t, client, server.URL, owner.PlaintextToken)
	otherClient := newIntegrationClient(t)
	loginIntegrationClient(t, otherClient, server.URL, other.PlaintextToken)
	csrf := authenticatedCSRF(t, client, server.URL)
	response, body := integrationPostForm(t, client, server.URL+"/admin/schedule", url.Values{"csrf_token": {csrf}, "description": {"every 15 minutes"}, "timezone": {"America/Toronto"}})
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "*/15 * * * *") || !strings.Contains(body, "Use this schedule") {
		t.Fatal("schedule builder did not resolve schedule")
	}
	jobID := createIntegrationJob(t, client, server.URL, "ui-workflow", integrationWebhookURL(t))
	response = postIntegrationJobAction(t, client, server.URL, jobID, "trigger")
	if response.StatusCode != http.StatusSeeOther {
		t.Fatal("trigger failed")
	}
	executionID := latestExecutionID(t, rt.db, jobID)
	integrationExec(t, rt.db, "UPDATE executions SET status='delivered' WHERE id=$1", executionID)
	response, body = integrationGet(t, client, server.URL+"/admin/executions?status=pending")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, executionID.String()) {
		t.Fatal("pending execution missing")
	}
	response, _ = integrationPostForm(t, otherClient, server.URL+"/admin/executions/"+executionID.String()+"/ack", url.Values{"csrf_token": {authenticatedCSRF(t, otherClient, server.URL)}})
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("cross-namespace acknowledgement allowed")
	}
	response, _ = integrationPostForm(t, client, server.URL+"/admin/executions/"+executionID.String()+"/ack", url.Values{"csrf_token": {csrf}})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatal("acknowledgement failed")
	}
	if n := countRows(t, rt.db, "SELECT COUNT(*) FROM executions WHERE id=$1 AND acknowledged_at IS NOT NULL", executionID); n != 1 {
		t.Fatal("acknowledgement not persisted")
	}
	t.Log("UI schedule preview, pending executions, and namespace-scoped persisted acknowledgement verified")
}
