package pkgsqlite

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
)

// CreateTable creates a table in the database based on the given struct.
func CreateTable(db *sql.DB, table interface{}, tableName string) error {
	t := reflect.TypeOf(table)

	var fields []string
	var foreignKeys []string

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		sqlTag := field.Tag.Get("sql")
		if sqlTag == "" {
			continue
		}

		parts := strings.Split(sqlTag, ",")
		sqlName := parts[0]
		constraint := ""

		for _, part := range parts[1:] {
			fmt.Println("Inspecting part:", part)
			if part == "primary_key" {
				constraint = "INTEGER PRIMARY KEY AUTOINCREMENT"
			} else if strings.HasPrefix(strings.TrimSpace(part), "foreign_key=") {
				fkParts := strings.Split(part, "=")
				refTableAndField := strings.Split(fkParts[1], ".")
				if len(refTableAndField) == 1 {
					foreignKeys = append(foreignKeys, fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s", sqlName, fkParts[1]))
				} else if len(refTableAndField) == 2 {
					foreignKeys = append(foreignKeys, fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)", sqlName, refTableAndField[0], refTableAndField[1]))
				}
				fmt.Println("Entered foreign_key block")
			}
		}

		if constraint == "" {
			constraint = convertGoTypeToSQLType(field.Type.Name())
		}

		fieldString := fmt.Sprintf("%s %s", sqlName, constraint)
		fields = append(fields, fieldString)
	}

	createTableSQL := fmt.Sprintf("CREATE TABLE %s (%s", tableName, strings.Join(fields, ", "))
	if len(foreignKeys) > 0 {
		createTableSQL += ", " + strings.Join(foreignKeys, ", ")
		fmt.Printf("Table %s linked with: %s\n", tableName, strings.Join(foreignKeys, ", "))
	}
	createTableSQL += ");"

	_, err := db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create table %s: %v", tableName, err)
	}

	return nil
}

func convertGoTypeToSQLType(goType string) string {
	switch goType {
	case "int64":
		return "INTEGER"
	case "string":
		return "TEXT NOT NULL"
	case "bool":
		return "BOOLEAN"
	default:
		return "TEXT"
	}
}
