package dumpdata

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/pgplex/pgschema/cmd/util"
	"github.com/spf13/cobra"
)

var (
	host       string
	port       int
	db         string
	user       string
	password   string
	schema     string
	multiFile  bool
	file       string
	noComments bool
	sslmode    string
)

type DumpConfig struct {
	Host       string
	Port       int
	DB         string
	User       string
	Password   string
	Schema     string
	MultiFile  bool
	File       string
	NoComments bool
	SSLMode    string
}

var DumpDataCmd = &cobra.Command{
	Use:          "dump-data",
	Short:        "Dump database table data as INSERT statements",
	Long:         "Dump and output database table data as INSERT statements for a specific schema.",
	RunE:         runDumpData,
	SilenceUsage: true,
	PreRunE:      util.PreRunEWithEnvVarsAndConnection(&db, &user, &host, &port),
}

func init() {
	DumpDataCmd.Flags().StringVar(&host, "host", "localhost", "Database server host (env: PGHOST)")
	DumpDataCmd.Flags().IntVar(&port, "port", 5432, "Database server port (env: PGPORT)")
	DumpDataCmd.Flags().StringVar(&db, "db", "", "Database name (required) (env: PGDATABASE)")
	DumpDataCmd.Flags().StringVar(&user, "user", "", "Database user name (required) (env: PGUSER)")
	DumpDataCmd.Flags().StringVar(&password, "password", "", "Database password (optional)")
	DumpDataCmd.Flags().StringVar(&schema, "schema", "public", "Schema name to dump data from (default: public)")
	DumpDataCmd.Flags().StringVar(&file, "file", "", "Output file path (optional, prints to stdout if empty)")
	DumpDataCmd.Flags().StringVar(&sslmode, "sslmode", "prefer", "SSL mode (env: PGSSLMODE)")
}

func ExecuteDumpData(config *DumpConfig) (string, error) {
	os.Setenv("PGPASSWORD", config.Password)
	os.Setenv("PGSSLMODE", config.SSLMode)

	// Load ignore configuration
	ignoreConfig, errFile := util.LoadIgnoreFileWithStructure()
	if errFile != nil {
		return "", fmt.Errorf("failed to load .pgschemaignore: %w", errFile)
	}

	args := []string{
		"-h", config.Host,
		"-p", strconv.Itoa(config.Port),
		"-U", config.User,
		"-d", config.DB,
		"-n", config.Schema,
		"-a",
		"--inserts",
	}

	formatTableTarget := func(schema, table string) string {
		if strings.Contains(table, ".") {
			return table
		}
		if schema != "" {
			return fmt.Sprintf("%s.%s", schema, table)
		}
		return table
	}

	for _, table := range ignoreConfig.DMLTables {
		args = append(args, "-T", formatTableTarget(config.Schema, table))
	}

	cmd := exec.Command("pg_dump", args...)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("erro ao executar pg_dump (data): %v (detalhes: %s)", err, stderr.String())
	}

	if config.File != "" {
		err := os.WriteFile(config.File, out.Bytes(), 0644)
		if err != nil {
			return "", fmt.Errorf("falha ao salvar arquivo de dados: %w", err)
		}
		return "", nil
	}

	return out.String(), nil
}

func runDumpData(cmd *cobra.Command, args []string) error {
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

	if err := util.ValidateSSLMode(finalSSLMode); err != nil {
		return err
	}

	config := &DumpConfig{
		Host:     host,
		Port:     port,
		DB:       db,
		User:     user,
		Password: finalPassword,
		Schema:   schema,
		File:     file,
		SSLMode:  finalSSLMode,
	}

	// Chama o executor exclusivo de dados
	output, err := ExecuteDumpData(config)
	if err != nil {
		return err
	}

	if output != "" {
		fmt.Print(output)
	}

	return nil
}
