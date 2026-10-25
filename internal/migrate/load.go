// Package migrate loads and applies the embedded SQL migrations.
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Migration is one NNN_name.sql file.
type Migration struct {
	Version          int
	Name             string
	SQL              string // raw file contents; tx control is stripped at apply time
	Checksum         string // lowercase hex sha256 of the raw file bytes
	ManualOnExisting bool
}

// manualOnExisting lists versions that must not be auto-applied to a
// database that already has data.
var manualOnExisting = map[int]bool{11: true}

var migrationName = regexp.MustCompile(`^(\d{3})_.+\.sql$`)

// Load reads migrations from the root of fsys, sorted by version.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var migs []Migration
	seen := make(map[int]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		version, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("parse version of %s: %w", e.Name(), err)
		}
		if prev, ok := seen[version]; ok {
			return nil, fmt.Errorf("duplicate migration version %d: %s and %s", version, prev, e.Name())
		}
		seen[version] = e.Name()
		raw, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(raw)
		migs = append(migs, Migration{
			Version:          version,
			Name:             e.Name(),
			SQL:              string(raw),
			Checksum:         hex.EncodeToString(sum[:]),
			ManualOnExisting: manualOnExisting[version],
		})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].Version < migs[j].Version })
	return migs, nil
}

// stripTxControl blanks lines that consist solely of BEGIN; or COMMIT;
// (case-insensitive) so the runner can wrap each migration in its own
// transaction. Line count is preserved.
func stripTxControl(sql string) string {
	lines := strings.Split(sql, "\n")
	for i, l := range lines {
		switch strings.ToLower(strings.TrimSpace(l)) {
		case "begin;", "commit;":
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}
