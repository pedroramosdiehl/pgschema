package applydata

import (
	"context"
	"fmt"
	"os"

	"github.com/pgplex/pgschema/cmd/util"
	"github.com/spf13/cobra"
)

var (
	host      string
	port      int
	db        string
	user      string
	password  string
	sslmode   string
	applyFile string
)

var ApplyCmd = &cobra.Command{
	Use:          "apply",
	Short:        "Apply a SQL file (DML) to the database",
	Long:         "Read a SQL file from disk and execute it against the target database within a transaction.",
	RunE:         runApply,
	SilenceUsage: true,
	PreRunE:      util.PreRunEWithEnvVarsAndConnection(&db, &user, &host, &port),
}

func init() {
	ApplyCmd.Flags().StringVar(&host, "host", "localhost", "Database server host (env: PGHOST)")
	ApplyCmd.Flags().IntVar(&port, "port", 5432, "Database server port (env: PGPORT)")
	ApplyCmd.Flags().StringVar(&db, "db", "", "Database name (required) (env: PGDATABASE)")
	ApplyCmd.Flags().StringVar(&user, "user", "", "Database user name (required) (env: PGUSER)")
	ApplyCmd.Flags().StringVar(&password, "password", "", "Database password (optional)")
	ApplyCmd.Flags().StringVar(&sslmode, "sslmode", "prefer", "SSL mode (env: PGSSLMODE)")

	// Flag obrigatória para o arquivo de entrada
	ApplyCmd.Flags().StringVar(&applyFile, "file", "", "Path to the SQL file to apply (required)")
	ApplyCmd.MarkFlagRequired("file")
}

func runApply(cmd *cobra.Command, args []string) error {
	finalPassword := password
	if finalPassword == "" {
		if envPassword := os.Getenv("PGPASSWORD"); envPassword != "" {
			finalPassword = envPassword
		}
	}

	finalSSLMode := sslmode
	if cmd == nil || !cmd.Flags().Changed("sslmode") {
		if envSSLMode := os.Getenv("PGSSLMODE"); envSSLMode != "" {
			finalSSLMode = envSSLMode
		}
	}

	sqlContent, err := os.ReadFile(applyFile)
	if err != nil {
		return fmt.Errorf("failed to read SQL file %s: %w", applyFile, err)
	}

	connConfig := &util.ConnectionConfig{
		Host:            host,
		Port:            port,
		Database:        db,
		User:            user,
		Password:        finalPassword,
		SSLMode:         finalSSLMode,
		ApplicationName: "pgschema-apply",
	}

	conn, err := util.Connect(connConfig)
	if err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	defer conn.Close()

	ctx := context.Background()

	fmt.Printf("Applying %s to database %s...\n", applyFile, db)

	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}

	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, string(sqlContent))
	if err != nil {
		return fmt.Errorf("error executing SQL script: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	fmt.Println("SQL script applied successfully!")
	return nil
}
