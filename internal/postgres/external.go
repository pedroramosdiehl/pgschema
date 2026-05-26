// Package postgres provides external PostgreSQL database functionality for desired state management.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pgplex/pgschema/cmd/util"
)

// ExternalDatabase manages an external PostgreSQL database for desired state validation.
// It creates temporary schemas with timestamp suffixes to avoid conflicts.
type ExternalDatabase struct {
	db                 *sql.DB
	host               string
	port               int
	database           string
	username           string
	password           string
	tempSchema         string // Temporary schema name with timestamp suffix
	tempSchemas        map[string]string
	targetMajorVersion int // Expected major version (from target database)
}

// ExternalDatabaseConfig holds configuration for connecting to an external database
type ExternalDatabaseConfig struct {
	Host               string
	Port               int
	Database           string
	Username           string
	Password           string
	SSLMode            string
	TargetMajorVersion int // Expected major version to match
}

// sslModeOrDefault returns the configured SSL mode, defaulting to "prefer" if empty
func (c *ExternalDatabaseConfig) sslModeOrDefault() string {
	if c.SSLMode == "" {
		return "prefer"
	}
	return c.SSLMode
}

// NewExternalDatabase creates a new external database connection for desired state validation.
// It validates the connection, checks version compatibility, and generates a temporary schema name.
func NewExternalDatabase(config *ExternalDatabaseConfig) (*ExternalDatabase, error) {
	// Build connection config
	connConfig := &util.ConnectionConfig{
		Host:     config.Host,
		Port:     config.Port,
		Database: config.Database,
		User:     config.Username,
		Password: config.Password,
		SSLMode:  config.sslModeOrDefault(),
	}

	// Connect to database
	db, err := util.Connect(connConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to external database: %w", err)
	}

	// Detect version and validate compatibility
	majorVersion, err := detectMajorVersion(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to detect PostgreSQL version: %w", err)
	}

	// Validate version compatibility (require exact major version match)
	if majorVersion != config.TargetMajorVersion {
		db.Close()
		return nil, fmt.Errorf(
			"version mismatch: plan database is PostgreSQL %d, but target database is PostgreSQL %d (exact major version match required)",
			majorVersion, config.TargetMajorVersion,
		)
	}

	// Generate temporary schema name with unique timestamp
	tempSchema := GenerateTempSchemaName()

	return &ExternalDatabase{
		db:                 db,
		host:               config.Host,
		port:               config.Port,
		database:           config.Database,
		username:           config.Username,
		password:           config.Password,
		tempSchema:         tempSchema,
		tempSchemas:        make(map[string]string),
		targetMajorVersion: config.TargetMajorVersion,
	}, nil
}

// GetConnectionDetails returns all connection details needed to connect to the external database
func (ed *ExternalDatabase) GetConnectionDetails() (host string, port int, database, username, password string) {
	return ed.host, ed.port, ed.database, ed.username, ed.password
}

// GetSchemaName returns the temporary schema name used for desired state validation
func (ed *ExternalDatabase) GetSchemaName() string {
	return ed.tempSchema
}

// GetSchemaMapping returns a copy of target -> temporary schema mapping.
func (ed *ExternalDatabase) GetSchemaMapping() map[string]string {
	out := make(map[string]string, len(ed.tempSchemas))
	for target, tmp := range ed.tempSchemas {
		out[target] = tmp
	}
	return out
}

// ApplySchema creates a temporary schema and applies SQL to it.
// The temporary schema name includes a timestamp to avoid conflicts.
func (ed *ExternalDatabase) ApplySchema(ctx context.Context, schema string, sql string) error {
	return ed.ApplySchemas(ctx, []SchemaSQLChunk{{Schema: schema, SQL: sql}})
}

// ApplySchemas creates isolated temporary schemas and applies schema-specific SQL.
func (ed *ExternalDatabase) ApplySchemas(ctx context.Context, chunks []SchemaSQLChunk) error {
	normalized := buildChunkListWithDefaults(chunks)
	if len(normalized) == 0 {
		return fmt.Errorf("no schema chunks provided")
	}

	// Note: We use the temporary schema name (ed.tempSchema) instead of the user-provided schema name
	// This ensures we don't interfere with existing schemas in the external database

	// Acquire a single dedicated connection to ensure SET search_path affects
	// all subsequent statements. Using *sql.DB (connection pool) does not
	// guarantee the same connection across ExecContext calls, so session-scoped
	// settings like search_path may be lost.
	conn, err := ed.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	ed.tempSchemas = make(map[string]string, len(normalized))
	for _, chunk := range normalized {
		targetSchema := chunk.Schema
		tempSchema := buildTempSchemaForTarget(ed.tempSchema, targetSchema)
		ed.tempSchemas[targetSchema] = tempSchema
	}

	for _, targetSchema := range sortedSchemaKeys(ed.tempSchemas) {
		if err := ed.resetTempSchema(ctx, conn, ed.tempSchemas[targetSchema]); err != nil {
			return err
		}
	}

	pending := make([]*materializationChunk, 0, len(normalized))
	totalStatements := 0
	for _, chunk := range normalized {
		targetSchema := chunk.Schema
		tempSchema := ed.tempSchemas[targetSchema]
		preparedSQL := prepareSchemaSQLForMaterialization(chunk.SQL, targetSchema, tempSchema, ed.tempSchemas)
		matChunk := buildMaterializationChunk(targetSchema, tempSchema, preparedSQL)
		if !matChunk.done() {
			pending = append(pending, matChunk)
			totalStatements += len(matChunk.statements)
		}
	}

	maxAttempts := totalStatements + 1
	for attempt := 1; len(pending) > 0 && attempt <= maxAttempts; attempt++ {
		nextPending := make([]*materializationChunk, 0, len(pending))
		progress := false
		var firstRetryableErr error

		for _, chunk := range pending {
			for !chunk.done() {
				statement := chunk.statements[chunk.nextIndex]
				err := ed.applySchemaStatement(ctx, conn, chunk.tempSchema, statement)
				if err != nil {
					if isRetryableMissingRelationError(err) {
						if firstRetryableErr == nil {
							firstRetryableErr = err
						}
						// Adia só o statement dependente e continua no restante do chunk.
						nextPending = append(nextPending, buildMaterializationChunk(chunk.targetSchema, chunk.tempSchema, statement))
						chunk.nextIndex++
						continue
					}
					return fmt.Errorf("failed to apply schema SQL to temporary schema %s: %w", chunk.tempSchema, enhanceApplyError(err, statement))
				}

				chunk.nextIndex++
				progress = true
			}
		}

		if len(nextPending) == 0 {
			break
		}
		if !progress {
			if firstRetryableErr != nil {
				return fmt.Errorf("failed to apply desired state SQL after %d attempt(s): %w", attempt, firstRetryableErr)
			}
			return fmt.Errorf("failed to apply desired state SQL after %d attempt(s): unresolved cross-schema dependencies", attempt)
		}
		pending = nextPending
	}

	if firstSchema := normalized[0].Schema; firstSchema != "" {
		ed.tempSchema = ed.tempSchemas[firstSchema]
	}

	return nil
}

func (ed *ExternalDatabase) resetTempSchema(ctx context.Context, conn *sql.Conn, tempSchema string) error {
	dropSchemaSQL := fmt.Sprintf("DROP SCHEMA IF EXISTS \"%s\" CASCADE", tempSchema)
	if _, err := util.ExecContextWithLogging(ctx, conn, dropSchemaSQL, "drop temporary schema"); err != nil {
		return fmt.Errorf("failed to drop temporary schema %s: %w", tempSchema, err)
	}

	createSchemaSQL := fmt.Sprintf("CREATE SCHEMA \"%s\"", tempSchema)
	if _, err := util.ExecContextWithLogging(ctx, conn, createSchemaSQL, "create temporary schema"); err != nil {
		return fmt.Errorf("failed to create temporary schema %s: %w", tempSchema, err)
	}

	return nil
}

func (ed *ExternalDatabase) applySchemaStatement(ctx context.Context, conn *sql.Conn, tempSchema, statement string) error {
	setSearchPathSQL := fmt.Sprintf("SET search_path TO \"%s\", public", tempSchema)
	if _, err := util.ExecContextWithLogging(ctx, conn, setSearchPathSQL, "set search_path for desired state"); err != nil {
		return fmt.Errorf("failed to set search_path: %w", err)
	}

	if _, err := util.ExecContextWithLogging(ctx, conn, "SET check_function_bodies = off", "disable function body validation for desired state"); err != nil {
		return fmt.Errorf("failed to disable check_function_bodies: %w", err)
	}

	if strings.TrimSpace(statement) == "" {
		return nil
	}

	if _, err := util.ExecContextWithLogging(ctx, conn, statement, "apply desired state SQL statement to temporary schema"); err != nil {
		return err
	}

	return nil
}

// Stop closes the connection and drops the temporary schema (best effort).
// Errors during cleanup are logged but don't cause failures.
func (ed *ExternalDatabase) Stop() error {
	// Drop temporary schemas (best effort - don't fail if this errors)
	if ed.db != nil {
		ctx := context.Background()
		for _, schema := range ed.GetSchemaMapping() {
			dropSchemaSQL := fmt.Sprintf("DROP SCHEMA IF EXISTS \"%s\" CASCADE", schema)
			_, _ = ed.db.ExecContext(ctx, dropSchemaSQL)
		}
		if ed.tempSchema != "" {
			dropSchemaSQL := fmt.Sprintf("DROP SCHEMA IF EXISTS \"%s\" CASCADE", ed.tempSchema)
			_, _ = ed.db.ExecContext(ctx, dropSchemaSQL)
		}
	}

	// Close database connection
	if ed.db != nil {
		return ed.db.Close()
	}

	return nil
}

// detectMajorVersion queries the database to determine its PostgreSQL major version
func detectMajorVersion(db *sql.DB) (int, error) {
	ctx := context.Background()

	// Query PostgreSQL version number (e.g., 170005 for 17.5)
	var versionNum int
	err := db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&versionNum)
	if err != nil {
		return 0, fmt.Errorf("failed to query PostgreSQL version: %w", err)
	}

	// Extract major version: version_num / 10000
	// e.g., 170005 / 10000 = 17
	majorVersion := versionNum / 10000

	return majorVersion, nil
}
