package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/djlord-it/cronlite/schema"
)

func TestLoadEmbeddedSchema(t *testing.T) {
	migs, err := Load(schema.FS)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(migs) != 11 {
		t.Fatalf("got %d migrations, want 11", len(migs))
	}
	for i, m := range migs {
		if m.Version != i+1 {
			t.Errorf("migs[%d].Version = %d, want %d", i, m.Version, i+1)
		}
		if want := m.Version == 11; m.ManualOnExisting != want {
			t.Errorf("version %d ManualOnExisting = %v, want %v", m.Version, m.ManualOnExisting, want)
		}
		raw, err := fs.ReadFile(schema.FS, m.Name)
		if err != nil {
			t.Fatalf("read %s: %v", m.Name, err)
		}
		sum := sha256.Sum256(raw)
		if m.Checksum != hex.EncodeToString(sum[:]) {
			t.Errorf("version %d checksum mismatch", m.Version)
		}
		if m.SQL != string(raw) {
			t.Errorf("version %d SQL is not the raw file contents", m.Version)
		}
	}
	if migs[10].Name != "011_execution_ownership.sql" {
		t.Errorf("version 11 name = %q", migs[10].Name)
	}
}

func TestLoadRejectsDuplicateVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"001_a.sql": {Data: []byte("SELECT 1;")},
		"001_b.sql": {Data: []byte("SELECT 2;")},
	}
	_, err := Load(fsys)
	if err == nil || !strings.Contains(err.Error(), "duplicate migration version 1") {
		t.Fatalf("err = %v, want duplicate migration version 1", err)
	}
}

func TestLoadIgnoresNonMigrationFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"001_a.sql": {Data: []byte("SELECT 1;")},
		"README.md": {Data: []byte("docs")},
		"embed.go":  {Data: []byte("package schema")},
	}
	migs, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(migs) != 1 || migs[0].Name != "001_a.sql" {
		t.Fatalf("got %+v", migs)
	}
}

func TestStripTxControl(t *testing.T) {
	got := stripTxControl("BEGIN;\nCREATE TABLE a();\n  commit;  \n")
	if want := "\nCREATE TABLE a();\n\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	in := "BEGIN; CREATE TABLE b();"
	if got := stripTxControl(in); got != in {
		t.Errorf("inline begin modified: %q", got)
	}
}
