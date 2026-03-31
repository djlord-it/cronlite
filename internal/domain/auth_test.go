package domain

import (
	"context"
	"testing"
)

func TestNamespace_IsZero(t *testing.T) {
	tests := []struct {
		name string
		ns   Namespace
		want bool
	}{
		{
			name: "empty string is zero",
			ns:   Namespace(""),
			want: true,
		},
		{
			name: "non-empty string is not zero",
			ns:   Namespace("tenant-1"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ns.IsZero()
			if got != tt.want {
				t.Errorf("Namespace(%q).IsZero() = %v, want %v", tt.ns, got, tt.want)
			}
		})
	}
}

func TestNamespace_String(t *testing.T) {
	ns := Namespace("tenant-1")
	got := ns.String()
	want := "tenant-1"
	if got != want {
		t.Errorf("Namespace.String() = %q, want %q", got, want)
	}
}

func TestNamespaceContext_RoundTrip(t *testing.T) {
	ns := Namespace("tenant-42")
	ctx := NamespaceToContext(context.Background(), ns)
	got := NamespaceFromContext(ctx)

	if got != ns {
		t.Errorf("NamespaceFromContext() = %q, want %q", got, ns)
	}
}

func TestNamespaceFromContext_BareContext(t *testing.T) {
	got := NamespaceFromContext(context.Background())

	if !got.IsZero() {
		t.Errorf("NamespaceFromContext(bare) = %q, want zero value", got)
	}
	if got != Namespace("") {
		t.Errorf("NamespaceFromContext(bare) = %q, want empty Namespace", got)
	}
}
