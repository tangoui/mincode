package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	schemaRiskSafe      = "safe"
	schemaRiskReview    = "review"
	schemaRiskDangerous = "dangerous"
)

// SchemaSnapshot 是用于可靠对比的数据库结构快照。
type SchemaSnapshot struct {
	Dialect     string                `json:"dialect"`
	Database    string                `json:"database"`
	Tables      []SchemaTableSnapshot `json:"tables"`
	Fingerprint string                `json:"fingerprint"`
}

// SchemaTableSnapshot 仅包含首期同步支持的表结构。
type SchemaTableSnapshot struct {
	Name           string                 `json:"name"`
	Columns        []SchemaColumnSnapshot `json:"columns"`
	PrimaryKey     []string               `json:"primaryKey"`
	PrimaryKeyName string                 `json:"primaryKeyName,omitempty"`
	Indexes        []SchemaIndexSnapshot  `json:"indexes"`
}

// SchemaColumnSnapshot 保留生成目标方言 DDL 所需的完整列定义。
type SchemaColumnSnapshot struct {
	Name          string  `json:"name"`
	DataType      string  `json:"dataType"`
	Nullable      bool    `json:"nullable"`
	DefaultValue  *string `json:"defaultValue,omitempty"`
	AutoIncrement bool    `json:"autoIncrement"`
}

// SchemaIndexSnapshot 表示普通或唯一索引；主键索引不进入此集合。
type SchemaIndexSnapshot struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}

// SchemaChange 是源结构到目标结构的一项可选择差异。
type SchemaChange struct {
	ID              string                `json:"id"`
	Kind            string                `json:"kind"`
	Table           string                `json:"table"`
	ObjectName      string                `json:"objectName"`
	Risk            string                `json:"risk"`
	DefaultSelected bool                  `json:"defaultSelected"`
	SourceTable     *SchemaTableSnapshot  `json:"sourceTable,omitempty"`
	TargetTable     *SchemaTableSnapshot  `json:"targetTable,omitempty"`
	SourceColumn    *SchemaColumnSnapshot `json:"sourceColumn,omitempty"`
	TargetColumn    *SchemaColumnSnapshot `json:"targetColumn,omitempty"`
	SourceIndex     *SchemaIndexSnapshot  `json:"sourceIndex,omitempty"`
	TargetIndex     *SchemaIndexSnapshot  `json:"targetIndex,omitempty"`
	SourceColumns   []string              `json:"sourceColumns,omitempty"`
	TargetColumns   []string              `json:"targetColumns,omitempty"`
}

// SchemaComparison 是源、目标快照及完整差异。
type SchemaComparison struct {
	Source  SchemaSnapshot `json:"source"`
	Target  SchemaSnapshot `json:"target"`
	Changes []SchemaChange `json:"changes"`
}

// SchemaSyncPlan 是经用户选择后可执行的有序 SQL 计划。
type SchemaSyncPlan struct {
	Dialect           string                `json:"dialect"`
	Database          string                `json:"database"`
	SourceFingerprint string                `json:"sourceFingerprint"`
	TargetFingerprint string                `json:"targetFingerprint"`
	Statements        []SchemaSyncStatement `json:"statements"`
}

// SchemaSyncStatement 对应一项差异，执行时逐条记录结果。
type SchemaSyncStatement struct {
	ChangeID string `json:"changeId"`
	Risk     string `json:"risk"`
	SQL      string `json:"sql"`
}

// SchemaSyncExecutionResult 记录逐条执行结果和执行后的真实目标快照。
type SchemaSyncExecutionResult struct {
	Completed    bool                           `json:"completed"`
	Statements   []SchemaSyncStatementExecution `json:"statements"`
	Target       SchemaSnapshot                 `json:"target"`
	RefreshError *string                        `json:"refreshError,omitempty"`
}

// SchemaSyncStatementExecution 表示一条 DDL 的最终状态。
type SchemaSyncStatementExecution struct {
	SchemaSyncStatement
	Status string  `json:"status"`
	Error  *string `json:"error,omitempty"`
}

// schemaSyncDialect returns the built-in SQL dialect used for schema sync.
func schemaSyncDialect(session *dbSession) (string, error) {
	if session == nil {
		return "", fmt.Errorf("该数据库类型不支持结构同步")
	}
	if session.kind != kindSQL || !supportedSchemaSyncDialect(session.dialect) {
		return "", fmt.Errorf("该数据库类型不支持结构同步")
	}
	return session.dialect, nil
}

// execSchemaSyncStatement runs one generated DDL statement on the target.
func execSchemaSyncStatement(ctx context.Context, target *dbSession, statement string) error {
	_, err := target.db.ExecContext(ctx, statement)
	return err
}

func captureSchemaSnapshot(ctx context.Context, session *dbSession, database string) (SchemaSnapshot, error) {
	dialect, err := schemaSyncDialect(session)
	if err != nil {
		return SchemaSnapshot{}, err
	}
	if (dialect == "oracle" || dialect == "dm") && strings.TrimSpace(database) == "" {
		return SchemaSnapshot{}, fmt.Errorf("%s 结构同步需要指定 Schema", dialect)
	}

	schema, err := getSchema(ctx, session)
	if err != nil {
		return SchemaSnapshot{}, fmt.Errorf("读取数据库结构: %w", err)
	}
	var selected *SchemaDatabase
	for index := range schema.Databases {
		if strings.EqualFold(schema.Databases[index].Name, database) ||
			(dialect == "sqlite" && strings.EqualFold(database, "main")) {
			selected = &schema.Databases[index]
			break
		}
	}
	if selected == nil {
		return SchemaSnapshot{}, fmt.Errorf("找不到数据库或 Schema: %s", database)
	}

	snapshot := SchemaSnapshot{Dialect: dialect, Database: database, Tables: []SchemaTableSnapshot{}}
	for _, item := range selected.Tables {
		if !strings.Contains(strings.ToLower(item.Kind), "table") {
			continue
		}
		columns, err := getTableColumns(ctx, session, database, item.Name)
		if err != nil {
			return SchemaSnapshot{}, fmt.Errorf("读取表 %s 字段: %w", item.Name, err)
		}
		primaryKey, err := getPrimaryKeys(ctx, session, database, item.Name)
		if err != nil {
			return SchemaSnapshot{}, fmt.Errorf("读取表 %s 主键: %w", item.Name, err)
		}
		indexes, err := listIndexes(ctx, session, database, item.Name)
		if err != nil {
			return SchemaSnapshot{}, fmt.Errorf("读取表 %s 索引: %w", item.Name, err)
		}
		table := SchemaTableSnapshot{
			Name: item.Name, PrimaryKey: append([]string(nil), primaryKey.Columns...), PrimaryKeyName: primaryKey.ConstraintName,
			Columns: make([]SchemaColumnSnapshot, 0, len(columns)),
			Indexes: make([]SchemaIndexSnapshot, 0, len(indexes)),
		}
		for _, column := range columns {
			table.Columns = append(table.Columns, SchemaColumnSnapshot{
				Name: column.Name, DataType: column.DataType, Nullable: column.Nullable,
				DefaultValue: column.DefaultValue, AutoIncrement: column.AutoIncrement,
			})
		}
		for _, index := range indexes {
			table.Indexes = append(table.Indexes, SchemaIndexSnapshot{
				Name: index.Name, Columns: append([]string(nil), index.Columns...), Unique: index.Unique,
			})
		}
		snapshot.Tables = append(snapshot.Tables, table)
	}
	sortSchemaSnapshot(&snapshot)
	fingerprint, err := schemaSnapshotFingerprint(snapshot)
	if err != nil {
		return SchemaSnapshot{}, fmt.Errorf("生成结构指纹: %w", err)
	}
	snapshot.Fingerprint = fingerprint
	return snapshot, nil
}

func compareSchemaSessions(ctx context.Context, source *dbSession, sourceDatabase string, target *dbSession, targetDatabase string) (SchemaComparison, error) {
	if source == nil || target == nil {
		return SchemaComparison{}, fmt.Errorf("结构同步仅支持同类型数据库")
	}
	sourceDialect, err := schemaSyncDialect(source)
	if err != nil {
		return SchemaComparison{}, fmt.Errorf("源连接: %w", err)
	}
	targetDialect, err := schemaSyncDialect(target)
	if err != nil {
		return SchemaComparison{}, fmt.Errorf("目标连接: %w", err)
	}
	if sourceDialect != targetDialect {
		return SchemaComparison{}, fmt.Errorf("结构同步仅支持同类型数据库")
	}
	sourceSnapshot, err := captureSchemaSnapshot(ctx, source, sourceDatabase)
	if err != nil {
		return SchemaComparison{}, fmt.Errorf("读取源结构: %w", err)
	}
	targetSnapshot, err := captureSchemaSnapshot(ctx, target, targetDatabase)
	if err != nil {
		return SchemaComparison{}, fmt.Errorf("读取目标结构: %w", err)
	}
	changes, err := compareSchemaSnapshots(sourceSnapshot, targetSnapshot)
	if err != nil {
		return SchemaComparison{}, err
	}
	return SchemaComparison{Source: sourceSnapshot, Target: targetSnapshot, Changes: changes}, nil
}

// executeSchemaSync re-compares, regenerates the plan and runs it statement
// by statement. With guard (a consumed SQL safety check) the plan must be
// the one the check assessed and each statement runs through it.
func executeSchemaSync(ctx context.Context, source *dbSession, sourceDatabase string, target *dbSession, targetDatabase, expectedSourceFingerprint, expectedTargetFingerprint string, selectedIDs []string, guard *schemaSyncGuard) (SchemaSyncExecutionResult, error) {
	comparison, err := compareSchemaSessions(ctx, source, sourceDatabase, target, targetDatabase)
	if err != nil {
		return SchemaSyncExecutionResult{}, err
	}
	if comparison.Source.Fingerprint != expectedSourceFingerprint {
		return SchemaSyncExecutionResult{}, fmt.Errorf("源结构已变化，请重新对比后再执行")
	}
	if comparison.Target.Fingerprint != expectedTargetFingerprint {
		return SchemaSyncExecutionResult{}, fmt.Errorf("目标结构已变化，请重新对比后再执行")
	}
	plan, err := generateSchemaSyncPlan(comparison.Target.Dialect, targetDatabase, comparison.Changes, selectedIDs, expectedSourceFingerprint, expectedTargetFingerprint)
	if err != nil {
		return SchemaSyncExecutionResult{}, err
	}
	if guard != nil {
		if err := guard.verify(plan.Statements); err != nil {
			return SchemaSyncExecutionResult{}, err
		}
	}
	result := SchemaSyncExecutionResult{Completed: true, Statements: []SchemaSyncStatementExecution{}, Target: comparison.Target}
	for i, statement := range plan.Statements {
		execution := SchemaSyncStatementExecution{SchemaSyncStatement: statement, Status: "running"}
		var execErr error
		if guard != nil {
			execErr = guard.run(ctx, i)
		} else {
			execErr = execSchemaSyncStatement(ctx, target, statement.SQL)
		}
		if execErr != nil {
			message := execErr.Error()
			execution.Status = "failed"
			execution.Error = &message
			result.Completed = false
			result.Statements = append(result.Statements, execution)
			if refreshed, refreshErr := captureSchemaSnapshot(ctx, target, targetDatabase); refreshErr == nil {
				result.Target = refreshed
			} else {
				message := fmt.Sprintf("执行失败后读取目标结构: %v", refreshErr)
				result.RefreshError = &message
			}
			return result, nil
		}
		execution.Status = "succeeded"
		result.Statements = append(result.Statements, execution)
	}
	refreshed, err := captureSchemaSnapshot(ctx, target, targetDatabase)
	if err != nil {
		result.Completed = false
		message := fmt.Sprintf("同步后读取目标结构: %v", err)
		result.RefreshError = &message
		return result, nil
	}
	result.Target = refreshed
	return result, nil
}

func compareSchemaSnapshots(source, target SchemaSnapshot) ([]SchemaChange, error) {
	if source.Dialect == "" || target.Dialect == "" || source.Dialect != target.Dialect {
		return nil, fmt.Errorf("结构同步仅支持同类型数据库")
	}

	sourceTables := tableSnapshotMap(source.Tables)
	targetTables := tableSnapshotMap(target.Tables)
	changes := make([]SchemaChange, 0)

	for key, sourceTable := range sourceTables {
		targetTable, exists := targetTables[key]
		if !exists {
			table := sourceTable
			changes = append(changes, newSchemaChange("create_table", sourceTable.Name, sourceTable.Name, schemaRiskSafe, &table, nil))
			for _, index := range sourceTable.Indexes {
				indexCopy := index
				change := newSchemaChange("create_index", sourceTable.Name, index.Name, schemaRiskSafe, nil, nil)
				change.SourceIndex = &indexCopy
				changes = append(changes, change)
			}
			continue
		}
		changes = append(changes, compareTableSnapshots(sourceTable, targetTable)...)
	}
	for key, targetTable := range targetTables {
		if _, exists := sourceTables[key]; exists {
			continue
		}
		table := targetTable
		changes = append(changes, newSchemaChange("drop_table", targetTable.Name, targetTable.Name, schemaRiskDangerous, nil, &table))
	}

	sort.SliceStable(changes, func(i, j int) bool {
		left, right := changes[i], changes[j]
		if schemaChangePriority(left.Kind) != schemaChangePriority(right.Kind) {
			return schemaChangePriority(left.Kind) < schemaChangePriority(right.Kind)
		}
		if strings.ToLower(left.Table) != strings.ToLower(right.Table) {
			return strings.ToLower(left.Table) < strings.ToLower(right.Table)
		}
		return strings.ToLower(left.ObjectName) < strings.ToLower(right.ObjectName)
	})
	return changes, nil
}

func compareTableSnapshots(source, target SchemaTableSnapshot) []SchemaChange {
	changes := make([]SchemaChange, 0)
	sourceColumns := columnSnapshotMap(source.Columns)
	targetColumns := columnSnapshotMap(target.Columns)
	for key, sourceColumn := range sourceColumns {
		targetColumn, exists := targetColumns[key]
		if !exists {
			column := sourceColumn
			change := newSchemaChange("add_column", source.Name, sourceColumn.Name, schemaRiskSafe, nil, nil)
			change.SourceColumn = &column
			changes = append(changes, change)
			continue
		}
		if !equalSchemaColumn(sourceColumn, targetColumn) {
			sourceCopy, targetCopy := sourceColumn, targetColumn
			change := newSchemaChange("alter_column", source.Name, sourceColumn.Name, schemaRiskDangerous, nil, nil)
			change.SourceColumn, change.TargetColumn = &sourceCopy, &targetCopy
			changes = append(changes, change)
		}
	}
	for key, targetColumn := range targetColumns {
		if _, exists := sourceColumns[key]; exists {
			continue
		}
		column := targetColumn
		change := newSchemaChange("drop_column", source.Name, targetColumn.Name, schemaRiskDangerous, nil, nil)
		change.TargetColumn = &column
		changes = append(changes, change)
	}

	if !equalIdentifierList(source.PrimaryKey, target.PrimaryKey) {
		if len(target.PrimaryKey) > 0 {
			objectName := target.PrimaryKeyName
			if objectName == "" {
				objectName = "primary"
			}
			change := newSchemaChange("drop_primary_key", source.Name, objectName, schemaRiskDangerous, nil, nil)
			change.TargetColumns = append([]string(nil), target.PrimaryKey...)
			changes = append(changes, change)
		}
		if len(source.PrimaryKey) > 0 {
			change := newSchemaChange("add_primary_key", source.Name, "primary", schemaRiskReview, nil, nil)
			change.SourceColumns = append([]string(nil), source.PrimaryKey...)
			changes = append(changes, change)
		}
	}

	sourceIndexes := indexSnapshotMap(source.Indexes)
	targetIndexes := indexSnapshotMap(target.Indexes)
	for key, sourceIndex := range sourceIndexes {
		targetIndex, exists := targetIndexes[key]
		if exists && equalSchemaIndex(sourceIndex, targetIndex) {
			continue
		}
		if exists {
			targetCopy := targetIndex
			change := newSchemaChange("drop_index", source.Name, targetIndex.Name, schemaRiskReview, nil, nil)
			change.TargetIndex = &targetCopy
			changes = append(changes, change)
		}
		sourceCopy := sourceIndex
		change := newSchemaChange("create_index", source.Name, sourceIndex.Name, schemaRiskSafe, nil, nil)
		change.SourceIndex = &sourceCopy
		changes = append(changes, change)
	}
	for key, targetIndex := range targetIndexes {
		if _, exists := sourceIndexes[key]; exists {
			continue
		}
		targetCopy := targetIndex
		change := newSchemaChange("drop_index", source.Name, targetIndex.Name, schemaRiskReview, nil, nil)
		change.TargetIndex = &targetCopy
		changes = append(changes, change)
	}
	return changes
}

func newSchemaChange(kind, table, objectName, risk string, source, target *SchemaTableSnapshot) SchemaChange {
	return SchemaChange{
		ID:   kind + ":" + strings.ToLower(table) + ":" + strings.ToLower(objectName),
		Kind: kind, Table: table, ObjectName: objectName, Risk: risk,
		DefaultSelected: risk == schemaRiskSafe,
		SourceTable:     source, TargetTable: target,
	}
}

func generateSchemaSyncPlan(dialect, database string, changes []SchemaChange, selectedIDs []string, sourceFingerprint, targetFingerprint string) (SchemaSyncPlan, error) {
	if !supportedSchemaSyncDialect(dialect) {
		return SchemaSyncPlan{}, fmt.Errorf("不支持的结构同步数据库类型: %s", dialect)
	}
	selected := make(map[string]struct{}, len(selectedIDs))
	changesByID := make(map[string]SchemaChange, len(changes))
	for _, change := range changes {
		changesByID[change.ID] = change
	}
	for _, id := range selectedIDs {
		if _, exists := changesByID[id]; !exists {
			return SchemaSyncPlan{}, fmt.Errorf("所选结构变更不存在或已过期: %s", id)
		}
		selected[id] = struct{}{}
	}
	for _, change := range changes {
		if _, ok := selected[change.ID]; !ok {
			continue
		}
		switch change.Kind {
		case "create_index":
			createTableID := "create_table:" + strings.ToLower(change.Table) + ":" + strings.ToLower(change.Table)
			if _, tableIsNew := changesByID[createTableID]; tableIsNew {
				if _, dependencySelected := selected[createTableID]; !dependencySelected {
					return SchemaSyncPlan{}, fmt.Errorf("创建新表 %s 的索引 %s 依赖 create_table 变更", change.Table, change.ObjectName)
				}
			}
		case "add_primary_key":
			for _, candidate := range changes {
				if candidate.Kind == "drop_primary_key" && strings.EqualFold(candidate.Table, change.Table) {
					if _, dependencySelected := selected[candidate.ID]; !dependencySelected {
						return SchemaSyncPlan{}, fmt.Errorf("替换表 %s 的主键时 add_primary_key 依赖 drop_primary_key 变更", change.Table)
					}
					break
				}
			}
		}
	}
	plan := SchemaSyncPlan{Dialect: dialect, Database: database, SourceFingerprint: sourceFingerprint, TargetFingerprint: targetFingerprint, Statements: []SchemaSyncStatement{}}
	for _, change := range changes {
		if _, ok := selected[change.ID]; !ok {
			continue
		}
		sql, err := schemaChangeSQL(dialect, database, change)
		if err != nil {
			return SchemaSyncPlan{}, fmt.Errorf("生成 %s.%s 变更 SQL: %w", change.Table, change.ObjectName, err)
		}
		plan.Statements = append(plan.Statements, SchemaSyncStatement{ChangeID: change.ID, Risk: change.Risk, SQL: sql})
	}
	return plan, nil
}

func schemaChangeSQL(dialect, database string, change SchemaChange) (string, error) {
	table := schemaQualifiedTable(dialect, database, change.Table)
	switch change.Kind {
	case "create_table":
		if change.SourceTable == nil {
			return "", fmt.Errorf("缺少源表定义")
		}
		return createTableSQL(dialect, database, *change.SourceTable)
	case "drop_table":
		return "DROP TABLE " + table, nil
	case "add_column":
		if change.SourceColumn == nil {
			return "", fmt.Errorf("缺少源字段定义")
		}
		definition := schemaColumnDefinition(dialect, *change.SourceColumn)
		switch dialect {
		case "sqlserver":
			return "ALTER TABLE " + table + " ADD " + definition, nil
		case "oracle":
			return "ALTER TABLE " + table + " ADD (" + definition + ")", nil
		case "dm":
			return "ALTER TABLE " + table + " ADD " + definition, nil
		default:
			return "ALTER TABLE " + table + " ADD COLUMN " + definition, nil
		}
	case "drop_column":
		return "ALTER TABLE " + table + " DROP COLUMN " + schemaQuoteIdentifier(dialect, change.ObjectName), nil
	case "alter_column":
		if change.SourceColumn == nil {
			return "", fmt.Errorf("缺少源字段定义")
		}
		definition := schemaColumnDefinition(dialect, *change.SourceColumn)
		switch dialect {
		case "mysql":
			return "ALTER TABLE " + table + " MODIFY COLUMN " + definition, nil
		case "postgres":
			return postgresAlterColumnSQL(table, *change.SourceColumn), nil
		case "sqlserver":
			return "ALTER TABLE " + table + " ALTER COLUMN " + definition, nil
		case "oracle", "dm":
			return "ALTER TABLE " + table + " MODIFY (" + definition + ")", nil
		case "sqlite":
			return "", fmt.Errorf("SQLite 修改字段需要重建表")
		}
	case "add_primary_key":
		return "ALTER TABLE " + table + " ADD PRIMARY KEY (" + quoteIdentifierList(dialect, change.SourceColumns) + ")", nil
	case "drop_primary_key":
		switch dialect {
		case "mysql":
			return "ALTER TABLE " + table + " DROP PRIMARY KEY", nil
		case "sqlite":
			return "", fmt.Errorf("SQLite 删除主键需要重建表")
		default:
			if strings.TrimSpace(change.ObjectName) == "" || strings.EqualFold(change.ObjectName, "primary") {
				return "", fmt.Errorf("缺少目标主键真实约束名")
			}
			return "ALTER TABLE " + table + " DROP CONSTRAINT " + schemaQuoteIdentifier(dialect, change.ObjectName), nil
		}
	case "create_index":
		if change.SourceIndex == nil {
			return "", fmt.Errorf("缺少源索引定义")
		}
		unique := ""
		if change.SourceIndex.Unique {
			unique = "UNIQUE "
		}
		return "CREATE " + unique + "INDEX " + schemaQuoteIdentifier(dialect, change.SourceIndex.Name) + " ON " + table + " (" + quoteIdentifierList(dialect, change.SourceIndex.Columns) + ")", nil
	case "drop_index":
		if dialect == "mysql" || dialect == "sqlserver" {
			return "DROP INDEX " + schemaQuoteIdentifier(dialect, change.ObjectName) + " ON " + table, nil
		}
		return "DROP INDEX " + schemaQualifiedIndex(dialect, database, change.ObjectName), nil
	}
	return "", fmt.Errorf("不支持的结构变更类型: %s", change.Kind)
}

func createTableSQL(dialect, database string, table SchemaTableSnapshot) (string, error) {
	if len(table.Columns) == 0 {
		return "", fmt.Errorf("表没有字段")
	}
	definitions := make([]string, 0, len(table.Columns)+1)
	for _, column := range table.Columns {
		definitions = append(definitions, schemaColumnDefinition(dialect, column))
	}
	if len(table.PrimaryKey) > 0 {
		definitions = append(definitions, "PRIMARY KEY ("+quoteIdentifierList(dialect, table.PrimaryKey)+")")
	}
	return "CREATE TABLE " + schemaQualifiedTable(dialect, database, table.Name) + " (" + strings.Join(definitions, ", ") + ")", nil
}

func schemaColumnDefinition(dialect string, column SchemaColumnSnapshot) string {
	definition := schemaQuoteIdentifier(dialect, column.Name) + " " + column.DataType
	if column.AutoIncrement {
		switch dialect {
		case "mysql":
			definition += " AUTO_INCREMENT"
		case "sqlserver":
			definition += " IDENTITY(1,1)"
		case "oracle":
			definition += " GENERATED BY DEFAULT AS IDENTITY"
		case "dm":
			definition += " IDENTITY(1,1)"
		}
	}
	if !column.Nullable {
		definition += " NOT NULL"
	}
	if column.DefaultValue != nil && strings.TrimSpace(*column.DefaultValue) != "" {
		definition += " DEFAULT " + strings.TrimSpace(*column.DefaultValue)
	}
	return definition
}

func postgresAlterColumnSQL(table string, column SchemaColumnSnapshot) string {
	name := schemaQuoteIdentifier("postgres", column.Name)
	parts := []string{"ALTER TABLE " + table + " ALTER COLUMN " + name + " TYPE " + column.DataType}
	if column.Nullable {
		parts = append(parts, "ALTER TABLE "+table+" ALTER COLUMN "+name+" DROP NOT NULL")
	} else {
		parts = append(parts, "ALTER TABLE "+table+" ALTER COLUMN "+name+" SET NOT NULL")
	}
	if column.DefaultValue == nil || strings.TrimSpace(*column.DefaultValue) == "" {
		parts = append(parts, "ALTER TABLE "+table+" ALTER COLUMN "+name+" DROP DEFAULT")
	} else {
		parts = append(parts, "ALTER TABLE "+table+" ALTER COLUMN "+name+" SET DEFAULT "+strings.TrimSpace(*column.DefaultValue))
	}
	return strings.Join(parts, "; ")
}

func schemaSnapshotFingerprint(snapshot SchemaSnapshot) (string, error) {
	normalized := snapshot
	normalized.Fingerprint = ""
	sortSchemaSnapshot(&normalized)
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func sortSchemaSnapshot(snapshot *SchemaSnapshot) {
	sort.Slice(snapshot.Tables, func(i, j int) bool {
		return strings.ToLower(snapshot.Tables[i].Name) < strings.ToLower(snapshot.Tables[j].Name)
	})
	for index := range snapshot.Tables {
		table := &snapshot.Tables[index]
		sort.Slice(table.Indexes, func(i, j int) bool {
			return strings.ToLower(table.Indexes[i].Name) < strings.ToLower(table.Indexes[j].Name)
		})
	}
}

func schemaQualifiedTable(dialect, database, table string) string {
	if dialect == "sqlite" || strings.TrimSpace(database) == "" {
		return schemaQuoteIdentifier(dialect, table)
	}
	return schemaQuoteIdentifier(dialect, database) + "." + schemaQuoteIdentifier(dialect, table)
}

func schemaQualifiedIndex(dialect, database, index string) string {
	if dialect == "sqlite" || strings.TrimSpace(database) == "" {
		return schemaQuoteIdentifier(dialect, index)
	}
	return schemaQuoteIdentifier(dialect, database) + "." + schemaQuoteIdentifier(dialect, index)
}

func schemaQuoteIdentifier(dialect, identifier string) string {
	switch dialect {
	case "mysql":
		return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
	case "sqlserver":
		return "[" + strings.ReplaceAll(identifier, "]", "]]") + "]"
	default:
		return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
	}
}

func quoteIdentifierList(dialect string, identifiers []string) string {
	quoted := make([]string, len(identifiers))
	for index, identifier := range identifiers {
		quoted[index] = schemaQuoteIdentifier(dialect, identifier)
	}
	return strings.Join(quoted, ", ")
}

func tableSnapshotMap(tables []SchemaTableSnapshot) map[string]SchemaTableSnapshot {
	result := make(map[string]SchemaTableSnapshot, len(tables))
	for _, table := range tables {
		result[strings.ToLower(table.Name)] = table
	}
	return result
}

func columnSnapshotMap(columns []SchemaColumnSnapshot) map[string]SchemaColumnSnapshot {
	result := make(map[string]SchemaColumnSnapshot, len(columns))
	for _, column := range columns {
		result[strings.ToLower(column.Name)] = column
	}
	return result
}

func indexSnapshotMap(indexes []SchemaIndexSnapshot) map[string]SchemaIndexSnapshot {
	result := make(map[string]SchemaIndexSnapshot, len(indexes))
	for _, index := range indexes {
		result[strings.ToLower(index.Name)] = index
	}
	return result
}

func equalSchemaColumn(left, right SchemaColumnSnapshot) bool {
	return strings.EqualFold(strings.TrimSpace(left.DataType), strings.TrimSpace(right.DataType)) &&
		left.Nullable == right.Nullable && left.AutoIncrement == right.AutoIncrement &&
		equalOptionalExpression(left.DefaultValue, right.DefaultValue)
}

func equalSchemaIndex(left, right SchemaIndexSnapshot) bool {
	return left.Unique == right.Unique && equalIdentifierList(left.Columns, right.Columns)
}

func equalIdentifierList(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !strings.EqualFold(left[index], right[index]) {
			return false
		}
	}
	return true
}

func equalOptionalExpression(left, right *string) bool {
	if left == nil || strings.TrimSpace(*left) == "" {
		return right == nil || strings.TrimSpace(*right) == ""
	}
	return right != nil && strings.EqualFold(strings.TrimSpace(*left), strings.TrimSpace(*right))
}

func schemaChangePriority(kind string) int {
	priorities := map[string]int{
		"create_table": 10, "alter_column": 20, "add_column": 30,
		"drop_column": 40, "drop_primary_key": 45, "add_primary_key": 46,
		"drop_index": 49, "create_index": 50, "drop_table": 90,
	}
	if priority, ok := priorities[kind]; ok {
		return priority
	}
	return 100
}

func supportedSchemaSyncDialect(dialect string) bool {
	switch dialect {
	case "mysql", "postgres", "sqlite", "sqlserver", "oracle", "dm":
		return true
	default:
		return false
	}
}
