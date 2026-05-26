package plan

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pgplex/pgschema/testutil"
	"github.com/spf13/cobra"
)

func TestPlanCommand_DatabaseIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	var err error

	// Start PostgreSQL container
	embeddedPG := testutil.SetupPostgres(t)
	defer embeddedPG.Stop()
	conn, host, port, dbname, user, password := testutil.ConnectToPostgres(t, embeddedPG)
	defer conn.Close()

	// Create container struct to match old API for minimal changes
	container := &struct {
		Conn     *sql.DB
		Host     string
		Port     int
		DBName   string
		User     string
		Password string
	}{
		Conn:     conn,
		Host:     host,
		Port:     port,
		DBName:   dbname,
		User:     user,
		Password: password,
	}

	// Setup database with initial schema

	initialSQL := `
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			email VARCHAR(255) UNIQUE
		);
		
		CREATE TABLE posts (
			id SERIAL PRIMARY KEY,
			user_id INTEGER REFERENCES users(id),
			title VARCHAR(255) NOT NULL
		);
	`
	_, err = conn.ExecContext(ctx, initialSQL)
	if err != nil {
		t.Fatalf("Failed to setup initial schema: %v", err)
	}

	// Create desired state schema file (with additional column and table)
	tmpDir := t.TempDir()
	desiredStateFile := filepath.Join(tmpDir, "desired_state.sql")
	desiredStateSQL := `
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			email VARCHAR(255) UNIQUE,
			created_at TIMESTAMP DEFAULT NOW()
		);
		
		CREATE TABLE posts (
			id SERIAL PRIMARY KEY,
			user_id INTEGER REFERENCES users(id),
			title VARCHAR(255) NOT NULL,
			content TEXT
		);
		
		CREATE TABLE comments (
			id SERIAL PRIMARY KEY,
			post_id INTEGER REFERENCES posts(id),
			content TEXT NOT NULL
		);
	`
	err = os.WriteFile(desiredStateFile, []byte(desiredStateSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to write desired state file: %v", err)
	}

	// Get container connection details
	containerHost := container.Host
	portMapped := container.Port

	// Reset global flag variables for clean test state
	outputHuman = ""
	outputJSON = ""
	outputSQL = ""

	// Create a new command instance to avoid flag conflicts
	cmd := &cobra.Command{}
	*cmd = *PlanCmd

	// Set command arguments
	args := []string{
		"--host", containerHost,
		"--port", fmt.Sprintf("%d", portMapped),
		"--db", container.DBName,
		"--user", container.User,
		"--password", container.Password,
		"--file", desiredStateFile,
		"--output-human", "stdout",
	}
	cmd.SetArgs(args)

	// Run plan command
	err = cmd.Execute()
	if err != nil {
		t.Fatalf("Plan command failed: %v", err)
	}

	// The plan should succeed and show the differences
	t.Log("Plan command executed successfully")
}

func TestPlanCommand_OutputFormats(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	var err error

	// Start PostgreSQL container
	embeddedPG := testutil.SetupPostgres(t)
	defer embeddedPG.Stop()
	conn, host, port, dbname, user, password := testutil.ConnectToPostgres(t, embeddedPG)
	defer conn.Close()

	// Create container struct to match old API for minimal changes
	container := &struct {
		Conn     *sql.DB
		Host     string
		Port     int
		DBName   string
		User     string
		Password string
	}{
		Conn:     conn,
		Host:     host,
		Port:     port,
		DBName:   dbname,
		User:     user,
		Password: password,
	}

	// Setup simple database schema

	simpleSQL := `
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL
		);
	`
	_, err = conn.ExecContext(ctx, simpleSQL)
	if err != nil {
		t.Fatalf("Failed to setup database schema: %v", err)
	}

	// Create desired state schema file
	tmpDir := t.TempDir()
	desiredStateFile := filepath.Join(tmpDir, "desired.sql")
	desiredSQL := `
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			email VARCHAR(255) UNIQUE
		);
		
		CREATE TABLE posts (
			id SERIAL PRIMARY KEY,
			user_id INTEGER REFERENCES users(id),
			title VARCHAR(255) NOT NULL
		);
	`
	err = os.WriteFile(desiredStateFile, []byte(desiredSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to write desired state file: %v", err)
	}

	// Get container connection details
	containerHost := container.Host
	portMapped := container.Port

	// Test different output formats
	testCases := []struct {
		name       string
		outputFlag string
	}{
		{"human format", "--output-human"},
		{"json format", "--output-json"},
		{"sql format", "--output-sql"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset global flag variables for clean test state
			outputHuman = ""
			outputJSON = ""
			outputSQL = ""

			// Create a new command instance for each test
			cmd := &cobra.Command{}
			*cmd = *PlanCmd

			// Set command arguments
			args := []string{
				"--host", containerHost,
				"--port", fmt.Sprintf("%d", portMapped),
				"--db", container.DBName,
				"--user", container.User,
				"--password", container.Password,
				"--file", desiredStateFile,
				tc.outputFlag, "stdout",
			}
			cmd.SetArgs(args)

			// Run plan command
			err := cmd.Execute()
			if err != nil {
				t.Fatalf("Plan command failed with %s: %v", tc.name, err)
			}

			t.Logf("Plan command executed successfully with %s", tc.name)
		})
	}
}

func TestPlanCommand_SchemaFiltering(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	var err error

	// Start PostgreSQL container
	embeddedPG := testutil.SetupPostgres(t)
	defer embeddedPG.Stop()
	conn, host, port, dbname, user, password := testutil.ConnectToPostgres(t, embeddedPG)
	defer conn.Close()

	// Create container struct to match old API for minimal changes
	container := &struct {
		Conn     *sql.DB
		Host     string
		Port     int
		DBName   string
		User     string
		Password string
	}{
		Conn:     conn,
		Host:     host,
		Port:     port,
		DBName:   dbname,
		User:     user,
		Password: password,
	}

	// Setup database with multiple schemas

	multiSchemaSQL := `
		CREATE SCHEMA app;
		CREATE SCHEMA analytics;
		
		CREATE TABLE public.users (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL
		);
		
		CREATE TABLE app.products (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL
		);
		
		CREATE TABLE analytics.reports (
			id SERIAL PRIMARY KEY,
			data TEXT
		);
	`
	_, err = conn.ExecContext(ctx, multiSchemaSQL)
	if err != nil {
		t.Fatalf("Failed to setup multi-schema database: %v", err)
	}

	// Create desired state file for public schema only
	tmpDir := t.TempDir()
	publicSchemaFile := filepath.Join(tmpDir, "public_schema.sql")
	publicSchemaSQL := `
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			email VARCHAR(255) UNIQUE
		);
		
		CREATE TABLE posts (
			id SERIAL PRIMARY KEY,
			title VARCHAR(255) NOT NULL
		);
	`
	err = os.WriteFile(publicSchemaFile, []byte(publicSchemaSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to write public schema file: %v", err)
	}

	// Get container connection details
	containerHost := container.Host
	portMapped := container.Port

	// Reset global flag variables for clean test state
	outputHuman = ""
	outputJSON = ""
	outputSQL = ""

	// Create a new command instance
	cmd := &cobra.Command{}
	*cmd = *PlanCmd

	// Set command arguments with schema filtering
	args := []string{
		"--host", containerHost,
		"--port", fmt.Sprintf("%d", portMapped),
		"--db", container.DBName,
		"--user", container.User,
		"--password", container.Password,
		"--schema", "public", // Filter to only public schema
		"--file", publicSchemaFile,
		"--output-human", "stdout",
	}
	cmd.SetArgs(args)

	// Run plan command
	err = cmd.Execute()
	if err != nil {
		t.Fatalf("Plan command failed with schema filtering: %v", err)
	}

	t.Log("Plan command executed successfully with schema filtering")
}

func TestPlanCommand_MultiSchemaHomonymousTables(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	embeddedPG := testutil.SetupPostgres(t)
	defer embeddedPG.Stop()
	conn, host, port, dbname, user, password := testutil.ConnectToPostgres(t, embeddedPG)
	defer conn.Close()

	container := &struct {
		Conn     *sql.DB
		Host     string
		Port     int
		DBName   string
		User     string
		Password string
	}{
		Conn:     conn,
		Host:     host,
		Port:     port,
		DBName:   dbname,
		User:     user,
		Password: password,
	}

	setupSQL := `
		CREATE SCHEMA integracao;
		CREATE SCHEMA comum;
	`
	if _, err := conn.ExecContext(ctx, setupSQL); err != nil {
		t.Fatalf("failed to setup schemas: %v", err)
	}

	tmpDir := t.TempDir()
	integracaoDir := filepath.Join(tmpDir, "integracao")
	comumDir := filepath.Join(tmpDir, "comum")
	if err := os.MkdirAll(integracaoDir, 0755); err != nil {
		t.Fatalf("failed to create integracao dir: %v", err)
	}
	if err := os.MkdirAll(comumDir, 0755); err != nil {
		t.Fatalf("failed to create comum dir: %v", err)
	}

	integracaoSQL := `CREATE TABLE objetivo_desenvolvimento_sustentavel (
  id bigint PRIMARY KEY
);`
	comumSQL := `CREATE TABLE objetivo_desenvolvimento_sustentavel (
  id bigint PRIMARY KEY,
  id_objetivo_desenvolvimento_sustentavel bigint
);
ALTER TABLE objetivo_desenvolvimento_sustentavel
ADD CONSTRAINT objetivo_desenvolvimento_sust_fk
FOREIGN KEY (id_objetivo_desenvolvimento_sustentavel)
REFERENCES integracao.objetivo_desenvolvimento_sustentavel(id);`

	if err := os.WriteFile(filepath.Join(integracaoDir, "schema.sql"), []byte(integracaoSQL), 0644); err != nil {
		t.Fatalf("failed to write integracao schema file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(comumDir, "schema.sql"), []byte(comumSQL), 0644); err != nil {
		t.Fatalf("failed to write comum schema file: %v", err)
	}

	bundlePath := filepath.Join(tmpDir, "schema.bundle.sql")
	bundleSQL := "\\i integracao/schema.sql\n\\i comum/schema.sql\n"
	if err := os.WriteFile(bundlePath, []byte(bundleSQL), 0644); err != nil {
		t.Fatalf("failed to write bundle file: %v", err)
	}

	outputHuman = ""
	outputJSON = ""
	outputSQL = ""

	cmd := &cobra.Command{}
	*cmd = *PlanCmd
	cmd.SetArgs([]string{
		"--host", container.Host,
		"--port", fmt.Sprintf("%d", container.Port),
		"--db", container.DBName,
		"--user", container.User,
		"--password", container.Password,
		"--schema", "integracao,comum",
		"--file", bundlePath,
		"--output-human", "stdout",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("plan command failed for homonymous multi-schema bundle: %v", err)
	}
}

func TestPlanCommand_MultiSchemaGreenfieldCrossSchemaReferences(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	embeddedPG := testutil.SetupPostgres(t)
	defer embeddedPG.Stop()
	conn, host, port, dbname, user, password := testutil.ConnectToPostgres(t, embeddedPG)
	defer conn.Close()

	container := &struct {
		Conn     *sql.DB
		Host     string
		Port     int
		DBName   string
		User     string
		Password string
	}{
		Conn:     conn,
		Host:     host,
		Port:     port,
		DBName:   dbname,
		User:     user,
		Password: password,
	}

	if _, err := conn.ExecContext(ctx, `CREATE SCHEMA integracao; CREATE SCHEMA comum;`); err != nil {
		t.Fatalf("failed to setup schemas: %v", err)
	}

	tmpDir := t.TempDir()
	integracaoDir := filepath.Join(tmpDir, "integracao")
	comumDir := filepath.Join(tmpDir, "comum")
	if err := os.MkdirAll(integracaoDir, 0755); err != nil {
		t.Fatalf("failed to create integracao dir: %v", err)
	}
	if err := os.MkdirAll(comumDir, 0755); err != nil {
		t.Fatalf("failed to create comum dir: %v", err)
	}

	// integracao references comum before comum chunk is materialized.
	// This reproduces the greenfield failure mode from real-world bundles.
	integracaoSQL := `CREATE VIEW vw_categoria AS
SELECT c.id
FROM comum.categoria_plano_trabalho c;`
	comumSQL := `CREATE TABLE categoria_plano_trabalho (
	id bigint PRIMARY KEY
);`

	if err := os.WriteFile(filepath.Join(integracaoDir, "schema.sql"), []byte(integracaoSQL), 0644); err != nil {
		t.Fatalf("failed to write integracao schema file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(comumDir, "schema.sql"), []byte(comumSQL), 0644); err != nil {
		t.Fatalf("failed to write comum schema file: %v", err)
	}

	bundlePath := filepath.Join(tmpDir, "schema.bundle.sql")
	bundleSQL := "\\i integracao/schema.sql\n\\i comum/schema.sql\n"
	if err := os.WriteFile(bundlePath, []byte(bundleSQL), 0644); err != nil {
		t.Fatalf("failed to write bundle file: %v", err)
	}

	outputHuman = ""
	outputJSON = ""
	outputSQL = ""

	cmd := &cobra.Command{}
	*cmd = *PlanCmd
	cmd.SetArgs([]string{
		"--host", container.Host,
		"--port", fmt.Sprintf("%d", container.Port),
		"--db", container.DBName,
		"--user", container.User,
		"--password", container.Password,
		"--schema", "integracao,comum",
		"--file", bundlePath,
		"--output-human", "stdout",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("plan command failed for greenfield cross-schema materialization: %v", err)
	}
}

func TestPlanCommand_EmptyDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	var err error

	// Start PostgreSQL container with empty database
	embeddedPG := testutil.SetupPostgres(t)
	defer embeddedPG.Stop()
	conn, host, port, dbname, user, password := testutil.ConnectToPostgres(t, embeddedPG)
	defer conn.Close()

	// Create container struct to match old API for minimal changes
	container := &struct {
		Conn     *sql.DB
		Host     string
		Port     int
		DBName   string
		User     string
		Password string
	}{
		Conn:     conn,
		Host:     host,
		Port:     port,
		DBName:   dbname,
		User:     user,
		Password: password,
	}

	// Create desired state schema file
	tmpDir := t.TempDir()
	desiredStateFile := filepath.Join(tmpDir, "initial_schema.sql")
	desiredStateSQL := `
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			email VARCHAR(255) UNIQUE
		);
		
		CREATE TABLE posts (
			id SERIAL PRIMARY KEY,
			user_id INTEGER REFERENCES users(id),
			title VARCHAR(255) NOT NULL,
			content TEXT
		);
	`
	err = os.WriteFile(desiredStateFile, []byte(desiredStateSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to write desired state file: %v", err)
	}

	// Get container connection details
	containerHost := container.Host
	portMapped := container.Port

	// Reset global flag variables for clean test state
	outputHuman = ""
	outputJSON = ""
	outputSQL = ""

	// Create a new command instance
	cmd := &cobra.Command{}
	*cmd = *PlanCmd

	// Set command arguments
	args := []string{
		"--host", containerHost,
		"--port", fmt.Sprintf("%d", portMapped),
		"--db", container.DBName,
		"--user", container.User,
		"--password", container.Password,
		"--file", desiredStateFile,
		"--output-human", "stdout",
	}
	cmd.SetArgs(args)

	// Run plan command
	err = cmd.Execute()
	if err != nil {
		t.Fatalf("Plan command failed on empty database: %v", err)
	}

	t.Log("Plan command executed successfully on empty database")
}
