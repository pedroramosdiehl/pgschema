package include

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Processor handles processing SQL files with \i include directives
type Processor struct {
	baseDir string
	visited map[string]bool
}

// ProcessedChunk represents SQL materialized from a specific source file.
type ProcessedChunk struct {
	SourcePath string
	Content    string
}

// NewProcessor creates a new include processor for the given base directory
func NewProcessor(baseDir string) *Processor {
	return &Processor{
		baseDir: baseDir,
		visited: make(map[string]bool),
	}
}

// ProcessFile processes a SQL file and resolves all \i include directives
func (p *Processor) ProcessFile(filename string) (string, error) {
	chunks, err := p.ProcessFileWithChunks(filename)
	if err != nil {
		return "", err
	}
	return joinChunks(chunks), nil
}

// ProcessFileWithChunks processes a SQL file and resolves all include directives
// while preserving source-file provenance for each output chunk.
func (p *Processor) ProcessFileWithChunks(filename string) ([]ProcessedChunk, error) {
	// Reset visited map for each top-level file processing
	p.visited = make(map[string]bool)

	// Get absolute path to ensure consistent path handling
	absPath, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for %s: %w", filename, err)
	}

	// Update base directory based on the input file's directory
	p.baseDir = filepath.Dir(absPath)

	return p.processFileRecursiveWithChunks(absPath)
}

// processFileRecursive recursively processes a file and its includes
func (p *Processor) processFileRecursive(filename string) (string, error) {
	chunks, err := p.processFileRecursiveWithChunks(filename)
	if err != nil {
		return "", err
	}
	return joinChunks(chunks), nil
}

func (p *Processor) processFileRecursiveWithChunks(filename string) ([]ProcessedChunk, error) {
	// Check for circular dependencies
	if p.visited[filename] {
		return nil, fmt.Errorf("circular dependency detected: %s", filename)
	}
	
	// Mark file as visited
	p.visited[filename] = true
	defer func() {
		// Unmark after processing to allow the same file to be included in different branches
		delete(p.visited, filename)
	}()
	
	// Read the file content
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", filename, err)
	}
	
	// Process includes in the current file
	currentDir := filepath.Dir(filename)
	processedChunks, err := p.processIncludesWithChunks(string(content), currentDir, filename)
	if err != nil {
		return nil, fmt.Errorf("failed to process includes in %s: %w", filename, err)
	}
	
	return processedChunks, nil
}

// processIncludes processes \i directives in the given content
func (p *Processor) processIncludes(content string, currentDir string) (string, error) {
	chunks, err := p.processIncludesWithChunks(content, currentDir, "")
	if err != nil {
		return "", err
	}
	return joinChunks(chunks), nil
}

func (p *Processor) processIncludesWithChunks(content string, currentDir string, sourcePath string) ([]ProcessedChunk, error) {
	// Regex to match \i directives
	// Matches: \i filename or \i filename; (with optional semicolon)
	includeRegex := regexp.MustCompile(`^\s*\\i\s+([^\s;]+)\s*;?\s*$`)
	
	lines := strings.Split(content, "\n")
	var resultChunks []ProcessedChunk
	var currentLines []string

	flushCurrent := func() {
		if len(currentLines) == 0 {
			return
		}
		text := strings.Join(currentLines, "\n")
		if strings.TrimSpace(text) == "" {
			currentLines = currentLines[:0]
			return
		}
		resultChunks = append(resultChunks, ProcessedChunk{
			SourcePath: sourcePath,
			Content:    text,
		})
		currentLines = currentLines[:0]
	}
	
	for _, line := range lines {
		matches := includeRegex.FindStringSubmatch(line)
		if matches != nil {
			flushCurrent()

			// Found an include directive
			includePath := matches[1]

			// Resolve the include path
			resolvedPath, isFolder, err := p.resolveIncludePath(includePath, currentDir)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve include path %s: %w", includePath, err)
			}

			var includedChunks []ProcessedChunk
			if isFolder {
				// Process the folder recursively
				includedChunks, err = p.processFolderRecursiveWithChunks(resolvedPath)
				if err != nil {
					return nil, fmt.Errorf("failed to process included folder %s: %w", resolvedPath, err)
				}
			} else {
				// Process the included file recursively
				includedChunks, err = p.processFileRecursiveWithChunks(resolvedPath)
				if err != nil {
					return nil, fmt.Errorf("failed to process included file %s: %w", resolvedPath, err)
				}
			}

			resultChunks = append(resultChunks, includedChunks...)
		} else {
			// Regular line, add as-is
			currentLines = append(currentLines, line)
		}
	}

	flushCurrent()
	return resultChunks, nil
}

// resolveIncludePath resolves an include path relative to the current directory
// Only allows files within the base directory and its subdirectories
// Returns the resolved path and a flag indicating if it's a folder
func (p *Processor) resolveIncludePath(includePath string, currentDir string) (string, bool, error) {
	// Check if this is a folder path (ends with /)
	isFolder := strings.HasSuffix(includePath, "/")

	// Clean the path to remove any . or .. components
	cleanPath := filepath.Clean(includePath)

	// Check for directory traversal attempts
	if strings.Contains(cleanPath, "..") {
		return "", false, fmt.Errorf("directory traversal not allowed: %s", includePath)
	}

	// Resolve relative to current directory
	resolvedPath := filepath.Join(currentDir, cleanPath)

	// Get absolute path
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return "", false, fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Ensure the resolved path is within the base directory
	baseAbs, err := filepath.Abs(p.baseDir)
	if err != nil {
		return "", false, fmt.Errorf("failed to get absolute base path: %w", err)
	}

	// Check if the resolved path is within the base directory
	relPath, err := filepath.Rel(baseAbs, absPath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return "", false, fmt.Errorf("include path %s is outside the base directory %s", includePath, p.baseDir)
	}

	// Check if path exists
	stat, err := os.Stat(absPath)
	if os.IsNotExist(err) {
		if isFolder {
			return "", false, fmt.Errorf("included folder does not exist: %s", absPath)
		} else {
			return "", false, fmt.Errorf("included file does not exist: %s", absPath)
		}
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to stat path %s: %w", absPath, err)
	}

	// Validate that the path type matches the expectation
	if isFolder && !stat.IsDir() {
		return "", false, fmt.Errorf("expected folder but found file: %s", absPath)
	}
	if !isFolder && stat.IsDir() {
		return "", false, fmt.Errorf("expected file but found folder: %s (use %s/ for folder includes)", absPath, includePath)
	}

	return absPath, isFolder, nil
}

// processFolderRecursive processes all .sql files in a folder using DFS
func (p *Processor) processFolderRecursive(folderPath string) (string, error) {
	chunks, err := p.processFolderRecursiveWithChunks(folderPath)
	if err != nil {
		return "", err
	}
	return joinChunks(chunks), nil
}

func (p *Processor) processFolderRecursiveWithChunks(folderPath string) ([]ProcessedChunk, error) {
	// Read directory contents
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory %s: %w", folderPath, err)
	}

	// Sort entries alphabetically (natural filename order)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var resultChunks []ProcessedChunk

	// Process each entry in alphabetical order
	for _, entry := range entries {
		entryPath := filepath.Join(folderPath, entry.Name())

		if entry.IsDir() {
			// Recursively process subdirectory (DFS)
			subFolderChunks, err := p.processFolderRecursiveWithChunks(entryPath)
			if err != nil {
				return nil, fmt.Errorf("failed to process subdirectory %s: %w", entryPath, err)
			}
			resultChunks = append(resultChunks, subFolderChunks...)
		} else if strings.HasSuffix(entry.Name(), ".sql") {
			// Process .sql file
			fileChunks, err := p.processFileRecursiveWithChunks(entryPath)
			if err != nil {
				return nil, fmt.Errorf("failed to process file %s: %w", entryPath, err)
			}
			resultChunks = append(resultChunks, fileChunks...)
		}
		// Ignore non-.sql files
	}

	return resultChunks, nil
}

func joinChunks(chunks []ProcessedChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	var b strings.Builder
	prevEndsWithNewline := false
	for _, chunk := range chunks {
		content := chunk.Content
		if content == "" {
			continue
		}
		if b.Len() > 0 && !prevEndsWithNewline && !strings.HasPrefix(content, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString(content)
		prevEndsWithNewline = strings.HasSuffix(content, "\n")
	}
	return b.String()
}