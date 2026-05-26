package diff

import (
	"strings"
	"testing"

	"github.com/pgplex/pgschema/ir"
)

func TestShouldDeferConstraintWhenReferencedKeyAddedInModify(t *testing.T) {
	table := &ir.Table{Schema: "public", Name: "child"}
	fk := &ir.Constraint{
		Type:              ir.ConstraintTypeForeignKey,
		ReferencedSchema:  "public",
		ReferencedTable:   "parent",
		ReferencedColumns: []*ir.ConstraintColumn{{Name: "external_id", Position: 1}},
	}
	modifyLookup := map[string][]*ir.Constraint{
		"public.parent": {
			{
				Type:    ir.ConstraintTypeUnique,
				Columns: []*ir.ConstraintColumn{{Name: "external_id", Position: 1}},
			},
		},
	}

	deferred := shouldDeferConstraint(
		table,
		fk,
		"public.child",
		map[string]bool{},
		map[string]bool{"public.parent": true},
		modifyLookup,
	)
	if !deferred {
		t.Fatalf("expected FK to be deferred when referenced UNIQUE is added in MODIFY phase")
	}
}

func TestShouldDeferConstraintWhenCrossSchemaKeyAddedInModify(t *testing.T) {
	table := &ir.Table{Schema: "app", Name: "child"}
	fk := &ir.Constraint{
		Type:              ir.ConstraintTypeForeignKey,
		ReferencedSchema:  "core",
		ReferencedTable:   "parent",
		ReferencedColumns: []*ir.ConstraintColumn{{Name: "id", Position: 1}},
	}
	modifyLookup := map[string][]*ir.Constraint{
		"core.parent": {
			{
				Type:    ir.ConstraintTypePrimaryKey,
				Columns: []*ir.ConstraintColumn{{Name: "id", Position: 1}},
			},
		},
	}

	deferred := shouldDeferConstraint(
		table,
		fk,
		"app.child",
		map[string]bool{},
		map[string]bool{"core.parent": true},
		modifyLookup,
	)
	if !deferred {
		t.Fatalf("expected cross-schema FK to be deferred when referenced key is added in MODIFY phase")
	}
}

func TestShouldDeferConstraintWhenCrossSchemaReferenceAlreadyCreated(t *testing.T) {
	table := &ir.Table{Schema: "integracao", Name: "child"}
	fk := &ir.Constraint{
		Type:             ir.ConstraintTypeForeignKey,
		ReferencedSchema: "comum",
		ReferencedTable:  "parent",
	}

	deferred := shouldDeferConstraint(
		table,
		fk,
		"integracao.child",
		map[string]bool{"comum.parent": true},
		map[string]bool{},
		nil,
	)
	if !deferred {
		t.Fatalf("expected cross-schema FK to be deferred even when referenced table was created earlier")
	}
}

func TestGenerateMigrationWithOptions_ForceQualifiedNamesMultiSchema(t *testing.T) {
	oldIR := &ir.IR{Schemas: map[string]*ir.Schema{}}
	newIR := &ir.IR{
		Schemas: map[string]*ir.Schema{
			"comum": {
				Name: "comum",
				Tables: map[string]*ir.Table{
					"parent": {
						Schema: "comum",
						Name:   "parent",
						Columns: []*ir.Column{
							{Name: "id", DataType: "integer"},
						},
						Constraints: map[string]*ir.Constraint{
							"parent_pkey": {
								Name:   "parent_pkey",
								Type:   ir.ConstraintTypePrimaryKey,
								Schema: "comum",
								Columns: []*ir.ConstraintColumn{
									{Name: "id", Position: 1},
								},
							},
						},
					},
				},
			},
			"integracao": {
				Name: "integracao",
				Tables: map[string]*ir.Table{
					"child": {
						Schema: "integracao",
						Name:   "child",
						Columns: []*ir.Column{
							{Name: "id", DataType: "integer"},
							{Name: "parent_id", DataType: "integer"},
						},
						Constraints: map[string]*ir.Constraint{
							"child_pkey": {
								Name:   "child_pkey",
								Type:   ir.ConstraintTypePrimaryKey,
								Schema: "integracao",
								Columns: []*ir.ConstraintColumn{
									{Name: "id", Position: 1},
								},
							},
							"child_parent_fkey": {
								Name:             "child_parent_fkey",
								Type:             ir.ConstraintTypeForeignKey,
								Schema:           "integracao",
								ReferencedSchema: "comum",
								ReferencedTable:  "parent",
								Columns: []*ir.ConstraintColumn{
									{Name: "parent_id", Position: 1},
								},
								ReferencedColumns: []*ir.ConstraintColumn{
									{Name: "id", Position: 1},
								},
							},
						},
					},
				},
			},
		},
	}

	diffs := GenerateMigrationWithOptions(oldIR, newIR, "integracao", GenerateOptions{ForceQualifiedNames: true})
	var sqlParts []string
	for _, d := range diffs {
		for _, stmt := range d.Statements {
			sqlParts = append(sqlParts, stmt.SQL)
		}
	}
	sql := strings.Join(sqlParts, "\n")

	if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS comum.parent") {
		t.Fatalf("expected qualified create table for comum.parent, got:\n%s", sql)
	}
	if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS integracao.child") {
		t.Fatalf("expected qualified create table for integracao.child, got:\n%s", sql)
	}
	if !strings.Contains(sql, "REFERENCES comum.parent") {
		t.Fatalf("expected qualified FK reference to comum.parent, got:\n%s", sql)
	}
}

func TestGenerateMigrationWithOptions_DefaultKeepsSingleSchemaUnqualified(t *testing.T) {
	oldIR := &ir.IR{Schemas: map[string]*ir.Schema{}}
	newIR := &ir.IR{
		Schemas: map[string]*ir.Schema{
			"public": {
				Name: "public",
				Tables: map[string]*ir.Table{
					"users": {
						Schema: "public",
						Name:   "users",
						Columns: []*ir.Column{
							{Name: "id", DataType: "integer"},
						},
						Constraints: map[string]*ir.Constraint{
							"users_pkey": {
								Name:   "users_pkey",
								Type:   ir.ConstraintTypePrimaryKey,
								Schema: "public",
								Columns: []*ir.ConstraintColumn{
									{Name: "id", Position: 1},
								},
							},
						},
					},
				},
			},
		},
	}

	diffs := GenerateMigration(oldIR, newIR, "public")
	var sqlParts []string
	for _, d := range diffs {
		for _, stmt := range d.Statements {
			sqlParts = append(sqlParts, stmt.SQL)
		}
	}
	sql := strings.Join(sqlParts, "\n")

	if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS users") {
		t.Fatalf("expected unqualified create table for single-schema output, got:\n%s", sql)
	}
	if strings.Contains(sql, "CREATE TABLE IF NOT EXISTS public.users") {
		t.Fatalf("did not expect forced schema qualification in single-schema output, got:\n%s", sql)
	}
}
