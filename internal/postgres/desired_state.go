// Package postgres provides PostgreSQL functionality for desired state management.
// This file defines the interface for desired state providers (embedded or external databases).
package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// DesiredStateProvider is an interface that abstracts the desired state database provider.
// It can be implemented by either embedded PostgreSQL or an external database connection.
type DesiredStateProvider interface {
	// GetConnectionDetails returns connection details for IR inspection
	// Returns: host, port, database, username, password
	GetConnectionDetails() (string, int, string, string, string)

	// GetSchemaName returns the actual schema name to inspect.
	// For embedded postgres: returns the temporary schema name (pgschema_tmp_*)
	// For external database: returns the temporary schema name (pgschema_tmp_*)
	GetSchemaName() string

	// GetSchemaMapping returns a mapping from target schema names to
	// temporary schema names created while materializing desired state.
	GetSchemaMapping() map[string]string

	// ApplySchema applies the desired state SQL to a schema.
	// For embedded postgres: resets the schema (drop/recreate)
	// For external database: creates temporary schema with timestamp suffix
	ApplySchema(ctx context.Context, schema string, sql string) error

	// ApplySchemas applies schema-aware SQL chunks to isolated temporary schemas.
	// Each target schema is materialized in its own temporary namespace.
	ApplySchemas(ctx context.Context, chunks []SchemaSQLChunk) error

	// Stop performs cleanup.
	// For embedded postgres: stops instance and removes temp directory
	// For external database: drops temporary schema (best effort) and closes connection
	Stop() error
}

// SchemaSQLChunk represents desired-state SQL associated with a target schema.
type SchemaSQLChunk struct {
	Schema string
	SQL    string
}

// GenerateTempSchemaName creates a unique temporary schema name for plan operations.
// The format is: pgschema_tmp_YYYYMMDD_HHMMSS_RRRRRRRR
// where RRRRRRRR is a random 8-character hex string for uniqueness.
// The "_tmp_" marker makes it distinctive and prevents accidental matching with user schemas.
//
// Example: pgschema_tmp_20251030_154501_a3f9d2e1
//
// Panics if random number generation fails (indicates serious system issue).
func GenerateTempSchemaName() string {
	timestamp := time.Now().Format("20060102_150405")

	// Add random suffix for uniqueness (4 bytes = 8 hex characters)
	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		// If crypto/rand fails, something is seriously wrong with the system
		panic(fmt.Sprintf("failed to generate random schema name: %v", err))
	}
	randomSuffix := hex.EncodeToString(randomBytes)

	return fmt.Sprintf("pgschema_tmp_%s_%s", timestamp, randomSuffix)
}

func buildChunkListWithDefaults(chunks []SchemaSQLChunk) []SchemaSQLChunk {
	if len(chunks) == 0 {
		return nil
	}

	grouped := make(map[string][]string, len(chunks))
	order := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		schema := strings.TrimSpace(chunk.Schema)
		if schema == "" {
			schema = "public"
		}
		if _, ok := grouped[schema]; !ok {
			order = append(order, schema)
		}
		grouped[schema] = append(grouped[schema], chunk.SQL)
	}

	out := make([]SchemaSQLChunk, 0, len(order))
	for _, schema := range order {
		sqlParts := grouped[schema]
		out = append(out, SchemaSQLChunk{
			Schema: schema,
			SQL:    strings.Join(sqlParts, "\n"),
		})
	}
	return out
}

func schemaNameSuffix(schema string) string {
	normalized := strings.ToLower(strings.TrimSpace(schema))
	if normalized == "" {
		return "public"
	}

	var b strings.Builder
	for _, r := range normalized {
		isLetter := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if isLetter || isDigit || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	suffix := b.String()
	if suffix == "" {
		return "public"
	}
	return suffix
}

func buildTempSchemaForTarget(baseTempSchema, targetSchema string) string {
	const maxSchemaNameLen = 63
	suffix := schemaNameSuffix(targetSchema)

	if baseTempSchema == "" {
		baseTempSchema = GenerateTempSchemaName()
	}

	separator := "__"
	available := maxSchemaNameLen - len(baseTempSchema) - len(separator)
	if available <= 0 {
		if len(baseTempSchema) > maxSchemaNameLen {
			return baseTempSchema[:maxSchemaNameLen]
		}
		return baseTempSchema
	}
	if len(suffix) > available {
		suffix = suffix[:available]
	}

	return baseTempSchema + separator + suffix
}

func sortedSchemaKeys(mapping map[string]string) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// prepareSchemaSQLForMaterialization applies all SQL rewrites required before
// executing desired-state chunks in temporary schemas.
func prepareSchemaSQLForMaterialization(sql, targetSchema, tempSchema string, schemaMapping map[string]string) string {
	schemaAgnosticSQL := stripSchemaQualifications(sql, targetSchema)
	schemaAgnosticSQL = replaceSchemaInDefaultPrivileges(schemaAgnosticSQL, targetSchema, tempSchema)
	schemaAgnosticSQL = replaceSchemaInSearchPath(schemaAgnosticSQL, targetSchema, tempSchema)
	schemaAgnosticSQL = replaceManagedSchemaQualifications(schemaAgnosticSQL, schemaMapping)
	return schemaAgnosticSQL
}

// stripSchemaQualifications removes schema qualifications from SQL statements for the specified target schema.
//
// Purpose:
// When applying user-provided SQL to temporary schemas during the plan command, we need to ensure
// that objects are created in the temporary schema (e.g., pgschema_tmp_20251030_154501_123456789)
// rather than in explicitly qualified schemas. PostgreSQL's search_path only affects unqualified
// object names - explicit schema qualifications always override search_path.
//
// Input SQL Sources:
// - pgschema dump command produces schema-agnostic output (no schema qualifications for target schema)
// - Users may manually edit SQL files and add schema qualifications (e.g., public.table)
// - Users may provide SQL from other sources that contains schema qualifications
//
// Behavior:
// This function strips schema qualifications ONLY for the target schema (specified by schemaName),
// while preserving qualifications for other schemas. This allows:
// 1. Target schema objects to be created in temporary schemas via search_path
// 2. Cross-schema references to be preserved correctly
//
// Examples:
// When target schema is "public":
// - public.employees -> employees (stripped - will use search_path)
// - "public".employees -> employees (stripped - handles quoted identifiers)
// - public."employees" -> "employees" (stripped - preserves quoted object names)
// - other_schema.users -> other_schema.users (preserved - cross-schema reference)
//
// It handles both quoted and unquoted schema names:
// - public.table -> table
// - "public".table -> table
// - public."table" -> "table"
// - "public"."table" -> "table"
//
// Only qualifications matching the specified schemaName are stripped.
// All other schema qualifications are preserved as intentional cross-schema references.
func stripSchemaQualifications(sql string, schemaName string) string {
	if schemaName == "" || !strings.Contains(sql, schemaName) {
		return sql
	}

	// Split SQL into dollar-quoted and non-dollar-quoted segments.
	// Schema qualifiers inside function/procedure bodies (dollar-quoted blocks)
	// must be preserved — the user may need them when search_path doesn't include
	// the function's schema (e.g., SET search_path = ''). (Issue #354)
	//
	// To avoid type-identity mismatches between stripped parameter types and
	// unstripped body references (Issue #399), callers should disable function
	// body validation with SET check_function_bodies = off before executing
	// the resulting SQL.
	segments := splitDollarQuotedSegments(sql)
	var result strings.Builder
	result.Grow(len(sql))
	for _, seg := range segments {
		if seg.quoted {
			// Preserve dollar-quoted content as-is
			result.WriteString(seg.text)
		} else {
			// Further split on single-quoted string literals to avoid stripping
			// schema prefixes from inside string constants (Issue #371).
			// e.g., has_scope('s.manage') must NOT become has_scope('manage')
			result.WriteString(stripSchemaQualificationsPreservingStrings(seg.text, schemaName))
		}
	}
	return result.String()
}

// replaceManagedSchemaQualifications rewrites schema-qualified references for all managed
// target schemas to their temporary materialized schema names.
//
// Example:
// - comum.table -> pgschema_tmp_xxx__comum.table
// - "comum"."table" -> "pgschema_tmp_xxx__comum"."table"
//
// Similar to stripSchemaQualifications, this function preserves single-quoted string literals,
// SQL comments, and dollar-quoted blocks.
func replaceManagedSchemaQualifications(sql string, schemaMapping map[string]string) string {
	if len(schemaMapping) == 0 {
		return sql
	}

	segments := splitDollarQuotedSegments(sql)
	var result strings.Builder
	result.Grow(len(sql))
	for _, seg := range segments {
		if seg.quoted {
			result.WriteString(seg.text)
		} else {
			result.WriteString(replaceManagedSchemaQualificationsPreservingStrings(seg.text, schemaMapping))
		}
	}
	return result.String()
}

func replaceManagedSchemaQualificationsPreservingStrings(text string, schemaMapping map[string]string) string {
	var result strings.Builder
	result.Grow(len(text))

	i := 0
	segStart := 0

	flushCode := func(end int) {
		if end > segStart {
			result.WriteString(replaceManagedSchemaQualificationsFromText(text[segStart:end], schemaMapping))
		}
		segStart = end
	}
	flushLiteral := func(end int) {
		result.WriteString(text[segStart:end])
		segStart = end
	}

	for i < len(text) {
		ch := text[i]

		if ch == '\'' {
			flushCode(i)
			i++
			for i < len(text) {
				if text[i] == '\'' {
					if i+1 < len(text) && text[i+1] == '\'' {
						i += 2
					} else {
						i++
						break
					}
				} else {
					i++
				}
			}
			flushLiteral(i)
			continue
		}

		if ch == '-' && i+1 < len(text) && text[i+1] == '-' {
			flushCode(i)
			i += 2
			for i < len(text) && text[i] != '\n' {
				i++
			}
			if i < len(text) {
				i++
			}
			flushLiteral(i)
			continue
		}

		if ch == '/' && i+1 < len(text) && text[i+1] == '*' {
			flushCode(i)
			i += 2
			for i < len(text) {
				if text[i] == '*' && i+1 < len(text) && text[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
			flushLiteral(i)
			continue
		}

		i++
	}

	flushCode(i)
	return result.String()
}

func replaceManagedSchemaQualificationsFromText(text string, schemaMapping map[string]string) string {
	if len(schemaMapping) == 0 || text == "" {
		return text
	}

	keys := sortedSchemaKeys(schemaMapping)
	result := text
	for _, targetSchema := range keys {
		tempSchema := schemaMapping[targetSchema]
		if targetSchema == "" || tempSchema == "" || targetSchema == tempSchema {
			continue
		}

		result = strings.ReplaceAll(
			result,
			fmt.Sprintf(`"%s".`, targetSchema),
			fmt.Sprintf(`"%s".`, tempSchema),
		)

		pattern := regexp.MustCompile(
			fmt.Sprintf(`(?i)(^|[^a-zA-Z0-9_$"])%s\.`, regexp.QuoteMeta(targetSchema)),
		)
		result = pattern.ReplaceAllString(result, fmt.Sprintf(`${1}"%s".`, tempSchema))
	}

	return result
}

func isRetryableMissingRelationError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "42P01" {
			return true
		}
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "relation") && strings.Contains(msg, "does not exist")
}

type materializationChunk struct {
	targetSchema string
	tempSchema   string
	statements   []string
	nextIndex    int
}

func buildMaterializationChunk(targetSchema, tempSchema, sql string) *materializationChunk {
	statements := splitSQLStatements(sql)
	return &materializationChunk{
		targetSchema: targetSchema,
		tempSchema:   tempSchema,
		statements:   statements,
		nextIndex:    0,
	}
}

func (c *materializationChunk) done() bool {
	return c == nil || c.nextIndex >= len(c.statements)
}

func splitSQLStatements(sql string) []string {
	text := strings.TrimSpace(sql)
	if text == "" {
		return nil
	}

	var statements []string
	start := 0

	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	dollarTag := ""

	for i := 0; i < len(sql); i++ {
		ch := sql[i]

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if dollarTag != "" {
			if strings.HasPrefix(sql[i:], dollarTag) {
				i += len(dollarTag) - 1
				dollarTag = ""
			}
			continue
		}
		if inSingle {
			if ch == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
			continue
		}
		if inDouble {
			if ch == '"' {
				if i+1 < len(sql) && sql[i+1] == '"' {
					i++
				} else {
					inDouble = false
				}
			}
			continue
		}

		if ch == '\'' {
			inSingle = true
			continue
		}
		if ch == '"' {
			inDouble = true
			continue
		}
		if ch == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			inLineComment = true
			i++
			continue
		}
		if ch == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			inBlockComment = true
			i++
			continue
		}
		if ch == '$' {
			if tag, ok := scanDollarTag(sql, i); ok {
				dollarTag = tag
				i += len(tag) - 1
				continue
			}
		}
		if ch == ';' {
			statement := strings.TrimSpace(sql[start : i+1])
			if statement != "" {
				statements = append(statements, statement)
			}
			start = i + 1
		}
	}

	if start < len(sql) {
		tail := strings.TrimSpace(sql[start:])
		if tail != "" {
			statements = append(statements, tail)
		}
	}

	return statements
}

func scanDollarTag(sql string, start int) (string, bool) {
	if start >= len(sql) || sql[start] != '$' {
		return "", false
	}

	end := strings.IndexByte(sql[start+1:], '$')
	if end == -1 {
		return "", false
	}
	end += start + 1
	tag := sql[start : end+1]

	inner := tag[1 : len(tag)-1]
	if inner == "" {
		return "$$", true
	}
	if (inner[0] >= 'a' && inner[0] <= 'z') || (inner[0] >= 'A' && inner[0] <= 'Z') || inner[0] == '_' {
		for i := 1; i < len(inner); i++ {
			c := inner[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
				return "", false
			}
		}
		return tag, true
	}
	return "", false
}

// stripSchemaQualificationsPreservingStrings splits text on single-quoted string
// literals and SQL comments, applies schema stripping only to non-string,
// non-comment parts, and reassembles.
//
// Limitation: E'...' escape-string syntax uses backslash-escaped quotes (E'it\'s')
// rather than doubled quotes ('it”s'). This parser only recognises the ” form.
// With E'content\”, a backslash-escaped quote may cause the parser to mistrack
// string boundaries, which can result in either:
//   - false-negative: schema qualifiers after the string are not stripped, or
//   - false-positive: schema prefixes inside the E-string are incorrectly stripped.
//
// Both cases change semantics only for E'...' strings, which are extremely rare
// in DDL schema definitions. The false-negative case preserves valid SQL; the
// false-positive case could alter string content but is unlikely in practice.
func stripSchemaQualificationsPreservingStrings(text string, schemaName string) string {
	var result strings.Builder
	result.Grow(len(text))

	// flushCode writes text[segStart:end] through schema stripping and advances segStart.
	i := 0
	segStart := 0

	flushCode := func(end int) {
		if end > segStart {
			result.WriteString(stripSchemaQualificationsFromText(text[segStart:end], schemaName))
		}
		segStart = end
	}
	flushLiteral := func(end int) {
		result.WriteString(text[segStart:end])
		segStart = end
	}

	for i < len(text) {
		ch := text[i]

		// Start of single-quoted string literal
		if ch == '\'' {
			flushCode(i)
			i++ // skip opening quote
			for i < len(text) {
				if text[i] == '\'' {
					if i+1 < len(text) && text[i+1] == '\'' {
						i += 2 // skip escaped ''
					} else {
						i++ // skip closing quote
						break
					}
				} else {
					i++
				}
			}
			flushLiteral(i)
			continue
		}

		// Start of line comment (--)
		if ch == '-' && i+1 < len(text) && text[i+1] == '-' {
			flushCode(i)
			i += 2
			for i < len(text) && text[i] != '\n' {
				i++
			}
			if i < len(text) {
				i++ // skip the newline
			}
			flushLiteral(i)
			continue
		}

		// Start of block comment (/* ... */)
		if ch == '/' && i+1 < len(text) && text[i+1] == '*' {
			flushCode(i)
			i += 2
			for i < len(text) {
				if text[i] == '*' && i+1 < len(text) && text[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
			flushLiteral(i)
			continue
		}

		i++
	}
	// Remaining text is code
	flushCode(i)
	return result.String()
}

// dollarQuotedSegment represents a segment of SQL text, either inside or outside a dollar-quoted block.
type dollarQuotedSegment struct {
	text   string
	quoted bool // true if this segment is inside dollar quotes (including the delimiters)
}

// splitDollarQuotedSegments splits SQL text into segments that are either inside or outside
// dollar-quoted blocks ($$...$$, $tag$...$tag$, etc.). This allows callers to process
// only the non-quoted parts while preserving function/procedure bodies verbatim.
// dollarQuoteRe matches PostgreSQL dollar-quote tags: $$ or $identifier$ where the
// identifier must start with a letter or underscore (not a digit). This avoids
// false positives on $1, $2 etc. parameter references.
var dollarQuoteRe = regexp.MustCompile(`\$(?:[a-zA-Z_][a-zA-Z0-9_]*)?\$`)

func splitDollarQuotedSegments(sql string) []dollarQuotedSegment {
	var segments []dollarQuotedSegment

	pos := 0
	for pos < len(sql) {
		// Find the next dollar-quote opening tag
		loc := dollarQuoteRe.FindStringIndex(sql[pos:])
		if loc == nil {
			// No more dollar quotes — rest is unquoted
			segments = append(segments, dollarQuotedSegment{text: sql[pos:], quoted: false})
			break
		}

		openStart := pos + loc[0]
		openEnd := pos + loc[1]
		tag := sql[openStart:openEnd]

		// Add the unquoted segment before this tag
		if openStart > pos {
			segments = append(segments, dollarQuotedSegment{text: sql[pos:openStart], quoted: false})
		}

		// Find the matching closing tag
		closeIdx := strings.Index(sql[openEnd:], tag)
		if closeIdx == -1 {
			// No closing tag — treat rest as quoted (unterminated)
			segments = append(segments, dollarQuotedSegment{text: sql[openStart:], quoted: true})
			pos = len(sql)
		} else {
			closeEnd := openEnd + closeIdx + len(tag)
			segments = append(segments, dollarQuotedSegment{text: sql[openStart:closeEnd], quoted: true})
			pos = closeEnd
		}
	}
	return segments
}

// schemaRegexes holds compiled regexes for a specific schema name, avoiding
// recompilation on every call to stripSchemaQualificationsFromText.
type schemaRegexes struct {
	re1 *regexp.Regexp // "schema"."object"
	re2 *regexp.Regexp // "schema".object
	re3 *regexp.Regexp // schema."object"
	re4 *regexp.Regexp // schema.object
}

var (
	schemaRegexCache   = make(map[string]*schemaRegexes)
	schemaRegexCacheMu sync.Mutex
)

func getSchemaRegexes(schemaName string) *schemaRegexes {
	schemaRegexCacheMu.Lock()
	defer schemaRegexCacheMu.Unlock()
	if cached, ok := schemaRegexCache[schemaName]; ok {
		return cached
	}
	escapedSchema := regexp.QuoteMeta(schemaName)
	// Patterns 1-2: quoted schema ("schema".object / "schema"."object")
	// The leading " already prevents suffix matching.
	// Patterns 3-4: unquoted schema (schema.object / schema."object")
	// Use a capture group for the optional non-identifier prefix so we can
	// preserve it in replacement without the match[0] ambiguity at ^.
	// The character class [^a-zA-Z0-9_$"] ensures the schema name isn't a
	// suffix of a longer identifier (e.g., schema "s" won't match "sales").
	sr := &schemaRegexes{
		re1: regexp.MustCompile(fmt.Sprintf(`"%s"\.(\"[^"]+\")`, escapedSchema)),
		re2: regexp.MustCompile(fmt.Sprintf(`"%s"\.([a-zA-Z_][a-zA-Z0-9_$]*)`, escapedSchema)),
		re3: regexp.MustCompile(fmt.Sprintf(`(^|[^a-zA-Z0-9_$"])%s\.(\"[^"]+\")`, escapedSchema)),
		re4: regexp.MustCompile(fmt.Sprintf(`(^|[^a-zA-Z0-9_$"])%s\.([a-zA-Z_][a-zA-Z0-9_$]*)`, escapedSchema)),
	}
	schemaRegexCache[schemaName] = sr
	return sr
}

// stripSchemaQualificationsFromText performs the actual schema qualification stripping on a text segment.
// It handles 4 cases:
// 1. unquoted_schema.unquoted_object  -> unquoted_object
// 2. unquoted_schema."quoted_object"  -> "quoted_object"
// 3. "quoted_schema".unquoted_object  -> unquoted_object
// 4. "quoted_schema"."quoted_object"  -> "quoted_object"
func stripSchemaQualificationsFromText(text string, schemaName string) string {
	sr := getSchemaRegexes(schemaName)

	result := text
	// Apply in order: quoted schema first to avoid double-matching
	result = sr.re1.ReplaceAllString(result, "$1")
	result = sr.re2.ReplaceAllString(result, "$1")
	// For patterns 3 and 4, $1 is the prefix (boundary char or empty at ^),
	// $2 is the object name — preserve the prefix and keep only the object.
	result = sr.re3.ReplaceAllString(result, "${1}${2}")
	result = sr.re4.ReplaceAllString(result, "${1}${2}")

	return result
}

// replaceSchemaInSearchPath replaces the target schema name in SET search_path clauses
// within function/procedure definitions.
//
// Purpose:
// When functions or procedures have SET search_path = public, pg_temp (or similar),
// PostgreSQL uses that search_path during function body validation (for SQL-language functions)
// and execution. When applying to a temporary schema, we need to replace the target schema
// in these clauses so that table references in function bodies can be resolved.
//
// Example (when target schema is "public" and temp schema is "pgschema_tmp_xxx"):
//
//	SET search_path = public, pg_temp  ->  SET search_path = "pgschema_tmp_xxx", pg_temp
//	SET search_path TO public          ->  SET search_path TO "pgschema_tmp_xxx"
//
// This handles both = and TO syntax, quoted and unquoted schema names (case-insensitive),
// and preserves other schemas in the comma-separated list.
//
// Limitations:
//   - Like stripSchemaQualifications and replaceSchemaInDefaultPrivileges, this function
//     operates on the raw SQL string without dollar-quote awareness. A SET search_path
//     inside a $$-quoted function body (e.g., dynamic SQL) would also be rewritten. In
//     practice this is not an issue because such usage is extremely rare, and the round-trip
//     through database inspection and normalizeSchemaNames restores the original schema name.
//   - When targetSchema is "public", replacing it removes "public" from the function's
//     search_path. If the function body references extension objects installed in "public"
//     (e.g., citext), they may not be found. Most extension objects (uuid, jsonb, etc.) live
//     in pg_catalog which is always searched, so this is rarely an issue in practice.
func replaceSchemaInSearchPath(sql string, targetSchema, tempSchema string) string {
	if targetSchema == "" || tempSchema == "" {
		return sql
	}

	replacement := fmt.Sprintf(`"%s"`, tempSchema)

	// Pattern: SET search_path = ... or SET search_path TO ...
	// We match the entire SET search_path clause and replace the target schema within it.
	searchPathPattern := regexp.MustCompile(`(?i)(SET\s+search_path\s*(?:=|TO)\s*)([^\n;]+)`)

	// Pattern to detect trailing function body start in the captured value.
	// When SET search_path and the body are on the same line, the value regex captures both.
	// Handles both AS $$ (dollar-quoted) and BEGIN ATOMIC (SQL-standard, PG14+) syntax.
	bodyStartPattern := regexp.MustCompile(`(?i)\s+(?:AS\s|BEGIN\s+ATOMIC\b)`)

	return searchPathPattern.ReplaceAllStringFunc(sql, func(match string) string {
		loc := searchPathPattern.FindStringSubmatchIndex(match)
		if loc == nil {
			return match
		}
		prefix := match[loc[2]:loc[3]]
		value := match[loc[4]:loc[5]]

		// Separate the search_path value from any trailing function body start
		suffix := ""
		if asLoc := bodyStartPattern.FindStringIndex(value); asLoc != nil {
			suffix = value[asLoc[0]:]
			value = value[:asLoc[0]]
		}

		// Tokenize the comma-separated search_path list and replace matching schemas.
		// This avoids regex pitfalls with quoted identifiers (e.g., "PUBLIC" should not
		// be matched by a case-insensitive unquoted pattern for "public").
		tokens := strings.Split(value, ",")
		for i, token := range tokens {
			trimmed := strings.TrimSpace(token)
			if strings.HasPrefix(trimmed, `"`) && strings.HasSuffix(trimmed, `"`) {
				// Quoted identifier: case-sensitive exact match.
				// "public" matches targetSchema "public", but "PUBLIC" does not
				// (in PostgreSQL, quoted identifiers preserve case).
				inner := trimmed[1 : len(trimmed)-1]
				if inner == targetSchema {
					tokens[i] = strings.Replace(token, trimmed, replacement, 1)
				}
			} else {
				// Unquoted identifier: case-insensitive match.
				// PostgreSQL folds unquoted identifiers to lowercase.
				if strings.EqualFold(trimmed, targetSchema) {
					tokens[i] = strings.Replace(token, trimmed, replacement, 1)
				}
			}
		}

		return prefix + strings.Join(tokens, ",") + suffix
	})
}

// replaceSchemaInDefaultPrivileges replaces schema names in ALTER DEFAULT PRIVILEGES statements.
// This is needed because stripSchemaQualifications only handles "schema.object" patterns,
// not "IN SCHEMA <schema>" clauses used by ALTER DEFAULT PRIVILEGES.
//
// Example:
//
//	ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO app_user;
//
// becomes:
//
//	ALTER DEFAULT PRIVILEGES IN SCHEMA pgschema_tmp_xxx GRANT SELECT ON TABLES TO app_user;
//
// This ensures default privileges are created in the temporary schema where we can inspect them.
func replaceSchemaInDefaultPrivileges(sql string, targetSchema, tempSchema string) string {
	if targetSchema == "" || tempSchema == "" {
		return sql
	}

	escapedTarget := regexp.QuoteMeta(targetSchema)

	// Pattern: IN SCHEMA <schema> (case insensitive for SQL keywords)
	// Handle both quoted and unquoted schema names
	// Pattern 1: IN SCHEMA "schema" (quoted)
	pattern1 := fmt.Sprintf(`(?i)(IN\s+SCHEMA\s+)"%s"`, escapedTarget)
	re1 := regexp.MustCompile(pattern1)
	result := re1.ReplaceAllString(sql, fmt.Sprintf(`${1}"%s"`, tempSchema))

	// Pattern 2: IN SCHEMA schema (unquoted)
	// Use word boundary to avoid partial matches
	pattern2 := fmt.Sprintf(`(?i)(IN\s+SCHEMA\s+)%s\b`, escapedTarget)
	re2 := regexp.MustCompile(pattern2)
	result = re2.ReplaceAllString(result, fmt.Sprintf(`${1}"%s"`, tempSchema))

	return result
}

// enhanceApplyError extracts the surrounding SQL context from a PostgreSQL error's
// position field to help the user locate the problematic statement in their schema files.
func enhanceApplyError(err error, sql string) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Position == 0 {
		return err
	}

	// PostgreSQL Position is 1-based character (not byte) offset
	runes := []rune(sql)
	pos := int(pgErr.Position) - 1
	if pos < 0 || pos >= len(runes) {
		return err
	}

	line := 1
	lineStart := 0
	for i := 0; i < pos; i++ {
		if runes[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}
	col := pos - lineStart + 1

	const contextLines = 3
	lines := strings.Split(sql, "\n")

	startLine := max(line-contextLines, 1)
	endLine := min(line+contextLines, len(lines))

	var snippet strings.Builder
	for i := startLine; i <= endLine; i++ {
		prefix := "  "
		if i == line {
			prefix = "> "
		}
		snippet.WriteString(fmt.Sprintf("%s%5d | %s\n", prefix, i, lines[i-1]))
	}

	return fmt.Errorf("%w\n\nError location (line %d, column %d):\n%s", err, line, col, snippet.String())
}
