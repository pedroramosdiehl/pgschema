// Package postgres provides embedded PostgreSQL functionality for production use.
// This package is used by the plan command to create temporary PostgreSQL instances
// for validating desired state schemas.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pgplex/pgschema/cmd/util"
)

// PostgresVersion is an alias for the embedded-postgres version type.
type PostgresVersion = embeddedpostgres.PostgresVersion

// EmbeddedPostgres manages a temporary embedded PostgreSQL instance.
// This is used by the plan command to validate desired state schemas.
type EmbeddedPostgres struct {
	instance    *embeddedpostgres.EmbeddedPostgres
	db          *sql.DB
	version     PostgresVersion
	host        string
	port        int
	database    string
	username    string
	password    string
	runtimePath string
	tempSchema  string // temporary schema name with timestamp for uniqueness
	tempSchemas map[string]string
}

// EmbeddedPostgresConfig holds configuration for starting embedded PostgreSQL
type EmbeddedPostgresConfig struct {
	Version  PostgresVersion
	Database string
	Username string
	Password string
}

// DetectPostgresVersionFromDB connects to a database and detects its version
// This is a convenience function that opens a connection, detects the version, and closes it
func DetectPostgresVersionFromDB(host string, port int, database, user, password, sslmode string) (PostgresVersion, error) {
	// Build connection config
	finalSSLMode := sslmode
	if finalSSLMode == "" {
		finalSSLMode = "prefer"
	}
	config := &util.ConnectionConfig{
		Host:     host,
		Port:     port,
		Database: database,
		User:     user,
		Password: password,
		SSLMode:  finalSSLMode,
	}

	// Connect to database
	db, err := util.Connect(config)
	if err != nil {
		return "", fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Detect version
	return detectPostgresVersion(db)
}

// StartEmbeddedPostgres starts a temporary embedded PostgreSQL instance
func StartEmbeddedPostgres(config *EmbeddedPostgresConfig) (*EmbeddedPostgres, error) {
	// Create unique runtime path and schema name
	tempSchema := GenerateTempSchemaName()
	runtimePath := filepath.Join(os.TempDir(), tempSchema)

	// Find an available port
	port, err := findAvailablePort()
	if err != nil {
		return nil, fmt.Errorf("failed to find available port: %w", err)
	}

	// Configure embedded postgres
	pgConfig := embeddedpostgres.DefaultConfig().
		Version(config.Version).
		Database(config.Database).
		Username(config.Username).
		Password(config.Password).
		Port(uint32(port)).
		RuntimePath(runtimePath).
		DataPath(filepath.Join(runtimePath, "data")).
		Logger(io.Discard). // Suppress embedded-postgres startup logs
		StartParameters(map[string]string{
			"logging_collector":          "off",    // Disable log collector
			"log_destination":            "stderr", // Send logs to stderr (which we discard)
			"log_min_messages":           "PANIC",  // Only log PANIC level messages
			"log_statement":              "none",   // Don't log SQL statements
			"log_min_duration_statement": "-1",     // Don't log slow queries
		})

	// Create and start PostgreSQL instance
	instance := embeddedpostgres.NewDatabase(pgConfig)
	if err := instance.Start(); err != nil {
		return nil, fmt.Errorf("failed to start embedded PostgreSQL: %w", err)
	}

	// Build connection config
	host := "localhost"
	connConfig := &util.ConnectionConfig{
		Host:     host,
		Port:     port,
		Database: config.Database,
		User:     config.Username,
		Password: config.Password,
		SSLMode:  "disable",
	}

	// Connect to database
	db, err := util.Connect(connConfig)
	if err != nil {
		instance.Stop()
		os.RemoveAll(runtimePath)
		return nil, fmt.Errorf("failed to connect to embedded PostgreSQL: %w", err)
	}

	return &EmbeddedPostgres{
		instance:    instance,
		db:          db,
		version:     config.Version,
		host:        host,
		port:        port,
		database:    config.Database,
		username:    config.Username,
		password:    config.Password,
		runtimePath: runtimePath,
		tempSchema:  tempSchema,
		tempSchemas: make(map[string]string),
	}, nil
}

// Stop stops and cleans up the embedded PostgreSQL instance
func (ep *EmbeddedPostgres) Stop() error {
	// Drop temporary schemas (best effort - don't fail if this errors)
	if ep.db != nil {
		ctx := context.Background()
		for _, schema := range ep.GetSchemaMapping() {
			dropSchemaSQL := fmt.Sprintf("DROP SCHEMA IF EXISTS \"%s\" CASCADE", schema)
			_, _ = ep.db.ExecContext(ctx, dropSchemaSQL)
		}
		if ep.tempSchema != "" {
			dropSchemaSQL := fmt.Sprintf("DROP SCHEMA IF EXISTS \"%s\" CASCADE", ep.tempSchema)
			_, _ = ep.db.ExecContext(ctx, dropSchemaSQL)
		}
	}

	// Close database connection
	if ep.db != nil {
		ep.db.Close()
	}

	// Stop PostgreSQL instance
	var stopErr error
	if ep.instance != nil {
		stopErr = ep.instance.Stop()
	}

	// Clean up runtime directory
	if ep.runtimePath != "" {
		if err := os.RemoveAll(ep.runtimePath); err != nil {
			// Don't return error here - just ignore cleanup failures
			// This can happen on Windows when files are still in use
		}
	}

	if stopErr != nil {
		return fmt.Errorf("failed to stop embedded PostgreSQL: %w", stopErr)
	}

	return nil
}

// GetConnectionDetails returns all connection details needed to connect to the embedded PostgreSQL instance
func (ep *EmbeddedPostgres) GetConnectionDetails() (host string, port int, database, username, password string) {
	return ep.host, ep.port, ep.database, ep.username, ep.password
}

// GetSchemaName returns the temporary schema name used for desired state validation.
// This returns the timestamped schema name that was created by ApplySchema.
func (ep *EmbeddedPostgres) GetSchemaName() string {
	return ep.tempSchema
}

// GetSchemaMapping returns a copy of the target -> temporary schema mapping.
func (ep *EmbeddedPostgres) GetSchemaMapping() map[string]string {
	out := make(map[string]string, len(ep.tempSchemas))
	for target, tmp := range ep.tempSchemas {
		out[target] = tmp
	}
	return out
}

// ApplySchema resets a schema (drops and recreates it) and applies SQL to it.
// This ensures a clean state before applying the desired schema definition.
// Note: The schema parameter is ignored - we always use the temporary schema name.
func (ep *EmbeddedPostgres) ApplySchema(ctx context.Context, schema string, sql string) error {
	return ep.ApplySchemas(ctx, []SchemaSQLChunk{{Schema: schema, SQL: sql}})
}

// ApplySchemas materializes desired SQL into isolated temporary schemas.
func (ep *EmbeddedPostgres) ApplySchemas(ctx context.Context, chunks []SchemaSQLChunk) error {
	normalized := buildChunkListWithDefaults(chunks)
	if len(normalized) == 0 {
		return fmt.Errorf("no schema chunks provided")
	}

	// Acquire a single dedicated connection to ensure SET search_path affects
	// all subsequent statements. Using *sql.DB (connection pool) does not
	// guarantee the same connection across ExecContext calls, so session-scoped
	// settings like search_path may be lost.
	conn, err := ep.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	ep.tempSchemas = make(map[string]string, len(normalized))
	for _, chunk := range normalized {
		targetSchema := chunk.Schema
		tempSchema := buildTempSchemaForTarget(ep.tempSchema, targetSchema)
		ep.tempSchemas[targetSchema] = tempSchema
	}

	for _, targetSchema := range sortedSchemaKeys(ep.tempSchemas) {
		if err := ep.resetTempSchema(ctx, conn, ep.tempSchemas[targetSchema]); err != nil {
			return err
		}
	}

	pending := make([]*materializationChunk, 0, len(normalized))
	totalStatements := 0
	for _, chunk := range normalized {
		targetSchema := chunk.Schema
		tempSchema := ep.tempSchemas[targetSchema]
		preparedSQL := prepareSchemaSQLForMaterialization(chunk.SQL, targetSchema, tempSchema, ep.tempSchemas)
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
				err := ep.applySchemaStatement(ctx, conn, chunk.tempSchema, statement)
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

	// Keep compatibility with older call sites expecting a single schema.
	if firstSchema := normalized[0].Schema; firstSchema != "" {
		ep.tempSchema = ep.tempSchemas[firstSchema]
	}

	return nil
}

func (ep *EmbeddedPostgres) resetTempSchema(ctx context.Context, conn *sql.Conn, tempSchema string) error {
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

func (ep *EmbeddedPostgres) applySchemaStatement(ctx context.Context, conn *sql.Conn, tempSchema, statement string) error {
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

// findAvailablePort finds an available TCP port for PostgreSQL to use
func findAvailablePort() (int, error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// mapToEmbeddedPostgresVersion maps a PostgreSQL major version to embedded-postgres version
// Supported versions: 14, 15, 16, 17, 18
func mapToEmbeddedPostgresVersion(majorVersion int) (PostgresVersion, error) {
	switch majorVersion {
	case 14:
		return embeddedpostgres.V14, nil
	case 15:
		return embeddedpostgres.V15, nil
	case 16:
		return embeddedpostgres.V16, nil
	case 17:
		return embeddedpostgres.V17, nil
	case 18:
		return embeddedpostgres.V18, nil
	default:
		return "", fmt.Errorf("unsupported PostgreSQL version %d (supported: 14-18)", majorVersion)
	}
}

// detectPostgresVersion queries the target database to determine its PostgreSQL version
// and returns the corresponding embedded-postgres version string
func detectPostgresVersion(db *sql.DB) (PostgresVersion, error) {
	ctx := context.Background()

	// Query PostgreSQL version number (e.g., 170005 for 17.5)
	var versionNum int
	err := db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&versionNum)
	if err != nil {
		return "", fmt.Errorf("failed to query PostgreSQL version: %w", err)
	}

	// Extract major version: version_num / 10000
	// e.g., 170005 / 10000 = 17
	majorVersion := versionNum / 10000

	// Map to embedded-postgres version
	return mapToEmbeddedPostgresVersion(majorVersion)
}
