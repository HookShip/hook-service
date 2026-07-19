// Package postgres provides HookShip's single-cell PostgreSQL durability repository.
package postgres

import (
	"bufio"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.up.sql migrations/checksums.txt
var migrationFiles embed.FS

type migration struct {
	version  int64
	name     string
	checksum string
	sql      string
}

// ValidateMigrationManifest verifies that every forward migration has one
// immutable SHA-256 entry and that the manifest contains no stale entries.
func ValidateMigrationManifest() error {
	_, err := loadMigrations()
	return err
}

// Migrate applies all forward migrations transactionally and rejects any
// checksum change or missing migration already recorded by the cell database.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("postgres pool is required")
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	var serverVersion int
	if err := pool.QueryRow(
		ctx,
		`SELECT current_setting('server_version_num')::integer`,
	).Scan(&serverVersion); err != nil {
		return fmt.Errorf("read PostgreSQL server version: %w", err)
	}
	if serverVersion < 170000 {
		return fmt.Errorf("PostgreSQL 17 or newer is required, got server_version_num %d", serverVersion)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended(current_schema() || ':hookship-migrations', 0)
		)
	`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version bigint PRIMARY KEY,
			name text NOT NULL UNIQUE,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT clock_timestamp(),
			CHECK (checksum ~ '^[0-9a-f]{64}$')
		)
	`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	applied := make(map[int64]struct {
		name     string
		checksum string
	})
	rows, err := tx.Query(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("read migration ledger: %w", err)
	}
	for rows.Next() {
		var version int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			rows.Close()
			return fmt.Errorf("scan migration ledger: %w", err)
		}
		applied[version] = struct {
			name     string
			checksum string
		}{name: name, checksum: checksum}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate migration ledger: %w", err)
	}
	rows.Close()

	known := make(map[int64]migration, len(migrations))
	for _, item := range migrations {
		known[item.version] = item
		if existing, ok := applied[item.version]; ok {
			if existing.name != item.name || existing.checksum != item.checksum {
				return fmt.Errorf(
					"migration checksum drift at version %06d: database has %s %s, source has %s %s",
					item.version,
					existing.name,
					existing.checksum,
					item.name,
					item.checksum,
				)
			}
			continue
		}
		if _, err := tx.Exec(ctx, item.sql); err != nil {
			return fmt.Errorf("apply migration %s: %w", item.name, err)
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
			item.version,
			item.name,
			item.checksum,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", item.name, err)
		}
	}
	for version, existing := range applied {
		if _, ok := known[version]; !ok {
			return fmt.Errorf(
				"database contains forward migration %06d (%s) missing from source",
				version,
				existing.name,
			)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	manifestData, err := migrationFiles.ReadFile("migrations/checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("read migration checksums: %w", err)
	}
	manifest := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(manifestData)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid migration checksum line %q", line)
		}
		checksum, name := fields[0], fields[1]
		if len(checksum) != 64 {
			return nil, fmt.Errorf("invalid checksum for migration %s", name)
		}
		if _, err := hex.DecodeString(checksum); err != nil {
			return nil, fmt.Errorf("invalid checksum for migration %s: %w", name, err)
		}
		if _, exists := manifest[name]; exists {
			return nil, fmt.Errorf("duplicate migration checksum for %s", name)
		}
		manifest[name] = checksum
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read migration checksum manifest: %w", err)
	}

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	var result []migration
	seenVersions := make(map[int64]string)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		version, err := parseMigrationVersion(name)
		if err != nil {
			return nil, err
		}
		if previous, exists := seenVersions[version]; exists {
			return nil, fmt.Errorf("migration version %06d is shared by %s and %s", version, previous, name)
		}
		seenVersions[version] = name
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		sum := sha256.Sum256(body)
		actual := hex.EncodeToString(sum[:])
		expected, ok := manifest[name]
		if !ok {
			return nil, fmt.Errorf("migration %s is missing from checksums.txt", name)
		}
		if expected != actual {
			return nil, fmt.Errorf("migration checksum drift for %s: manifest %s, actual %s", name, expected, actual)
		}
		delete(manifest, name)
		result = append(result, migration{
			version:  version,
			name:     name,
			checksum: actual,
			sql:      string(body),
		})
	}
	if len(result) == 0 {
		return nil, errors.New("no forward PostgreSQL migrations found")
	}
	if len(manifest) != 0 {
		names := make([]string, 0, len(manifest))
		for name := range manifest {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("checksum manifest contains missing migrations: %s", strings.Join(names, ", "))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	return result, nil
}

func parseMigrationVersion(name string) (int64, error) {
	const width = 6
	if len(name) <= width || name[width] != '_' {
		return 0, fmt.Errorf("migration %s must start with a six-digit version", name)
	}
	version, err := strconv.ParseInt(name[:width], 10, 64)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("migration %s has an invalid version", name)
	}
	return version, nil
}
