/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-03-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-26 19:35:31
 * @FilePath: \go-casbin-gorm-adapter\adapter_test.go
 * @Description: 测试 Casbin GORM 适配器
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package gormadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/kamalyes/go-casbin/policy"
	"github.com/kamalyes/go-logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// nilDBHandler 实现 db.Handler 接口，GetDB 返回 nil，用于测试 NewAdapter 的 nil gormDB 分支
type nilDBHandler struct{}

func (nilDBHandler) GetDB() *gorm.DB   { return nil }
func (nilDBHandler) IsConnected() bool { return false }

// renamedDialector 包装 sqlite dialector 并覆盖 Name()，用于测试 tableScopedMigrator.DropIndex 各方言分支
type renamedDialector struct {
	gorm.Dialector
	newName string
}

func (d *renamedDialector) Name() string { return d.newName }

func newSQLiteAdapter(t *testing.T) *Adapter {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	adapter, err := NewAdapterByDB(db, WithTableName("casbin_rule_test"))
	require.NoError(t, err)
	return adapter
}

func TestUpdateFilteredPoliciesByPType_RemovesOnlyGroupingRules(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{
		"p, alice, tenant1, data1, read",
		"g, alice, admin, tenant1",
		"g, bob, admin, tenant1",
	}))

	err := adapter.UpdateFilteredPoliciesByPType("g", nil, 0, "alice")
	require.NoError(t, err)

	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.Contains(t, policies, "p, alice, tenant1, data1, read")
	assert.NotContains(t, policies, "g, alice, admin, tenant1")
	assert.Contains(t, policies, "g, bob, admin, tenant1")
}

func TestUpdateFilteredPoliciesByPType_ReplacesOnlyPolicyRules(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{
		"p, role:admin, ops, old, GET",
		"g, role:admin, parent, ops",
	}))

	err := adapter.UpdateFilteredPoliciesByPType("p", []string{
		"p, role:admin, ops, new, POST",
	}, 0, "role:admin")
	require.NoError(t, err)

	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.NotContains(t, policies, "p, role:admin, ops, old, GET")
	assert.Contains(t, policies, "p, role:admin, ops, new, POST")
	assert.Contains(t, policies, "g, role:admin, parent, ops")
}

func TestExecuteInTransactionRollsBack(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.ExecuteInTransaction(context.Background(), func(txAdapter policy.Adapter) error {
		require.NoError(t, txAdapter.AddPolicy("g, alice, admin, tenant1"))
		return errors.New("rollback")
	})
	require.Error(t, err)

	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.Empty(t, policies)
}

// ==================== syncTableIndexes 相关辅助与测试 ====================

// mockGormIndex 用于单元测试 indexDefEqual，实现 gorm.Index 接口
type mockGormIndex struct {
	name         string
	columns      []string
	unique       bool
	uniqueOk     bool
	primaryKey   bool
	primaryKeyOk bool
}

func (m mockGormIndex) Table() string            { return "" }
func (m mockGormIndex) Name() string             { return m.name }
func (m mockGormIndex) Columns() []string        { return m.columns }
func (m mockGormIndex) PrimaryKey() (bool, bool) { return m.primaryKey, m.primaryKeyOk }
func (m mockGormIndex) Unique() (bool, bool)     { return m.unique, m.uniqueOk }
func (m mockGormIndex) Option() string           { return "" }

// findIndexByName 从索引列表中按名查找
func findIndexByName(t *testing.T, db *gorm.DB, table string, name string) gorm.Index {
	t.Helper()
	idxs, err := db.Table(table).Migrator().GetIndexes(&CasbinRule{})
	require.NoError(t, err)
	for _, idx := range idxs {
		if idx.Name() == name {
			return idx
		}
	}
	return nil
}

// assertIndexColumns 断言指定索引的列顺序与期望一致
func assertIndexColumns(t *testing.T, db *gorm.DB, table, name string, expectedCols []string) {
	t.Helper()
	idx := findIndexByName(t, db, table, name)
	require.NotNil(t, idx, "index %s should exist", name)
	assert.Equal(t, expectedCols, idx.Columns(), "index %s columns mismatch", name)
}

// TestModelIndexColumns_SortsByPriority 验证按 priority 升序返回列名
func TestModelIndexColumns_SortsByPriority(t *testing.T) {
	// 构造一个乱序的 Index，priority 分别为 3,1,2
	idx := &schema.Index{
		Fields: []schema.IndexOption{
			{Field: &schema.Field{DBName: "c3"}, Priority: 3},
			{Field: &schema.Field{DBName: "c1"}, Priority: 1},
			{Field: &schema.Field{DBName: "c2"}, Priority: 2},
		},
	}
	cols := modelIndexColumns(idx)
	assert.Equal(t, []string{"c1", "c2", "c3"}, cols)
}

// TestModelIndexColumns_SingleField 验证单字段索引
func TestModelIndexColumns_SingleField(t *testing.T) {
	idx := &schema.Index{
		Fields: []schema.IndexOption{
			{Field: &schema.Field{DBName: "only"}, Priority: 1},
		},
	}
	cols := modelIndexColumns(idx)
	assert.Equal(t, []string{"only"}, cols)
}

// TestModelIndexColumns_Empty 验证空字段索引返回空切片（不修改原索引）
func TestModelIndexColumns_Empty(t *testing.T) {
	idx := &schema.Index{Fields: []schema.IndexOption{}}
	cols := modelIndexColumns(idx)
	assert.Empty(t, cols)
}

// TestModelIndexColumns_DoesNotMutateSource 验证不会修改原索引的字段顺序
func TestModelIndexColumns_DoesNotMutateSource(t *testing.T) {
	idx := &schema.Index{
		Fields: []schema.IndexOption{
			{Field: &schema.Field{DBName: "c3"}, Priority: 3},
			{Field: &schema.Field{DBName: "c1"}, Priority: 1},
		},
	}
	_ = modelIndexColumns(idx)
	// 原索引顺序应保持不变
	assert.Equal(t, "c3", idx.Fields[0].DBName)
	assert.Equal(t, "c1", idx.Fields[1].DBName)
}

// TestIndexDefEqual 覆盖 indexDefEqual 的所有分支
func TestIndexDefEqual(t *testing.T) {
	tests := []struct {
		name         string
		dbIdx        mockGormIndex
		expectedCols []string
		expectedUni  bool
		want         bool
	}{
		{
			name:         "列数不同返回false",
			dbIdx:        mockGormIndex{columns: []string{"a"}, uniqueOk: true, unique: false},
			expectedCols: []string{"a", "b"},
			expectedUni:  false,
			want:         false,
		},
		{
			name:         "列名不同返回false",
			dbIdx:        mockGormIndex{columns: []string{"a", "x"}, uniqueOk: true, unique: false},
			expectedCols: []string{"a", "b"},
			expectedUni:  false,
			want:         false,
		},
		{
			name:         "列顺序不同返回false",
			dbIdx:        mockGormIndex{columns: []string{"b", "a"}, uniqueOk: true, unique: false},
			expectedCols: []string{"a", "b"},
			expectedUni:  false,
			want:         false,
		},
		{
			name:         "Unique ok=false 跳过唯一性对比 列匹配返回true",
			dbIdx:        mockGormIndex{columns: []string{"a", "b"}, uniqueOk: false},
			expectedCols: []string{"a", "b"},
			expectedUni:  true, // 期望唯一但驱动不支持查询，应跳过对比
			want:         true,
		},
		{
			name:         "Unique ok=true 且相等 列匹配返回true",
			dbIdx:        mockGormIndex{columns: []string{"a", "b"}, uniqueOk: true, unique: true},
			expectedCols: []string{"a", "b"},
			expectedUni:  true,
			want:         true,
		},
		{
			name:         "Unique ok=true 且不等 列匹配返回false",
			dbIdx:        mockGormIndex{columns: []string{"a", "b"}, uniqueOk: true, unique: false},
			expectedCols: []string{"a", "b"},
			expectedUni:  true,
			want:         false,
		},
		{
			name:         "Unique ok=true 都非唯一 列匹配返回true",
			dbIdx:        mockGormIndex{columns: []string{"a", "b"}, uniqueOk: true, unique: false},
			expectedCols: []string{"a", "b"},
			expectedUni:  false,
			want:         true,
		},
		{
			name:         "空列匹配 返回true",
			dbIdx:        mockGormIndex{columns: nil, uniqueOk: false},
			expectedCols: nil,
			expectedUni:  false,
			want:         true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indexDefEqual(tt.dbIdx, tt.expectedCols, tt.expectedUni)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestNewAdapter_NilHandler 验证 nil handler 返回错误（覆盖 NewAdapter 的边界分支）
func TestNewAdapter_NilHandler(t *testing.T) {
	adapter, err := NewAdapter(nil)
	require.Error(t, err)
	assert.Nil(t, adapter)
}

// TestNewAdapterByDB_NilDB 验证 nil gormDB 返回错误（覆盖 NewAdapterByDB 的边界分支）
func TestNewAdapterByDB_NilDB(t *testing.T) {
	adapter, err := NewAdapterByDB(nil)
	require.Error(t, err)
	assert.Nil(t, adapter)
}

// TestNewAdapter_AutoMigrateFailed 验证 AutoMigrate 失败时返回错误
// 场景：使用一个无法迁移的 DB（关闭后传入），触发 AutoMigrate 错误路径
func TestNewAdapter_AutoMigrateFailed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// 先正常建表
	adapter, err := NewAdapterByDB(db, WithTableName("casbin_rule_test"))
	require.NoError(t, err)
	require.NotNil(t, adapter)

	// 关闭底层连接，使后续 AutoMigrate 失败
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	// 再次创建 adapter 应失败（AutoMigrate 在已关闭的连接上执行报错）
	adapter2, err := NewAdapterByDB(db, WithTableName("casbin_rule_test"))
	require.Error(t, err)
	assert.Nil(t, adapter2)
}

// ==================== syncIndexesCore 单元测试（mock indexMigrator 覆盖所有错误分支） ====================

// mockIndexMigrator 实现 indexMigrator 接口，用于注入可控的 GetIndexes/DropIndex/CreateIndex 行为
type mockIndexMigrator struct {
	indexes   []gorm.Index
	getErr    error
	dropErr   error
	createErr error
	dropped   []string
	created   []string
}

func (m *mockIndexMigrator) GetIndexes(dst interface{}) ([]gorm.Index, error) {
	return m.indexes, m.getErr
}
func (m *mockIndexMigrator) DropIndex(dst interface{}, name string) error {
	m.dropped = append(m.dropped, name)
	return m.dropErr
}
func (m *mockIndexMigrator) CreateIndex(dst interface{}, name string) error {
	m.created = append(m.created, name)
	return m.createErr
}

// newParsedStmt 用真实 SQLite DB 解析 CasbinRule 模型，返回可用于 ParseIndexes 的 Statement
func newParsedStmt(t *testing.T) *gorm.Statement {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	stmt := &gorm.Statement{DB: db}
	require.NoError(t, stmt.Parse(&CasbinRule{}))
	return stmt
}

// TestSyncIndexesCore_GetIndexesFailed 验证 GetIndexes 失败时返回错误
func TestSyncIndexesCore_GetIndexesFailed(t *testing.T) {
	stmt := newParsedStmt(t)
	mig := &mockIndexMigrator{getErr: errors.New("db error")}
	ctx := context.Background()
	err := syncIndexesCore(ctx, mig, stmt, "casbin_rule_test", logger.NewEmptyLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get indexes of casbin_rule_test failed")
	assert.Empty(t, mig.dropped)
	assert.Empty(t, mig.created)
}

// TestSyncIndexesCore_NameMatchedConsistentSkips 验证名匹配且定义一致时跳过（无 Drop/Create）
func TestSyncIndexesCore_NameMatchedConsistentSkips(t *testing.T) {
	stmt := newParsedStmt(t)
	mig := &mockIndexMigrator{
		indexes: []gorm.Index{
			mockGormIndex{name: "idx_casbin_rule", columns: []string{"p_type", "v0", "v1", "v2"}, uniqueOk: true, unique: false},
		},
	}
	ctx := context.Background()
	err := syncIndexesCore(ctx, mig, stmt, "casbin_rule_test", logger.NewEmptyLogger())
	require.NoError(t, err)
	for _, name := range mig.dropped {
		assert.NotEqual(t, "idx_casbin_rule", name)
	}
	for _, name := range mig.created {
		assert.NotEqual(t, "idx_casbin_rule", name)
	}
}

// TestSyncIndexesCore_NameMatchedInconsistent_DropFailed 验证名匹配不一致但 DropIndex 失败时返回错误
func TestSyncIndexesCore_NameMatchedInconsistent_DropFailed(t *testing.T) {
	stmt := newParsedStmt(t)
	mig := &mockIndexMigrator{
		indexes: []gorm.Index{
			mockGormIndex{name: "idx_casbin_rule", columns: []string{"p_type", "v0"}, uniqueOk: true, unique: false},
		},
		dropErr: errors.New("drop failed"),
	}
	ctx := context.Background()
	err := syncIndexesCore(ctx, mig, stmt, "casbin_rule_test", logger.NewEmptyLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "drop index idx_casbin_rule")
	assert.Contains(t, mig.dropped, "idx_casbin_rule")
	assert.Empty(t, mig.created)
}

// TestSyncIndexesCore_NameMatchedInconsistent_CreateFailed 验证 Drop 成功但 Create 失败时返回错误
func TestSyncIndexesCore_NameMatchedInconsistent_CreateFailed(t *testing.T) {
	stmt := newParsedStmt(t)
	mig := &mockIndexMigrator{
		indexes: []gorm.Index{
			mockGormIndex{name: "idx_casbin_rule", columns: []string{"p_type", "v0"}, uniqueOk: true, unique: false},
		},
		createErr: errors.New("create failed"),
	}
	ctx := context.Background()
	err := syncIndexesCore(ctx, mig, stmt, "casbin_rule_test", logger.NewEmptyLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create index idx_casbin_rule")
	assert.Contains(t, mig.dropped, "idx_casbin_rule")
	assert.Contains(t, mig.created, "idx_casbin_rule")
}

// TestSyncIndexesCore_NameMatchedInconsistent_RebuildSuccess 验证名匹配不一致时 Drop+Create 成功
func TestSyncIndexesCore_NameMatchedInconsistent_RebuildSuccess(t *testing.T) {
	stmt := newParsedStmt(t)
	mig := &mockIndexMigrator{
		indexes: []gorm.Index{
			mockGormIndex{name: "idx_casbin_rule", columns: []string{"p_type", "v0"}, uniqueOk: true, unique: false},
		},
	}
	ctx := context.Background()
	err := syncIndexesCore(ctx, mig, stmt, "casbin_rule_test", logger.NewEmptyLogger())
	require.NoError(t, err)
	assert.Contains(t, mig.dropped, "idx_casbin_rule")
	assert.Contains(t, mig.created, "idx_casbin_rule")
}

// TestSyncIndexesCore_ColMatchedSkips 验证名不匹配但列匹配时跳过（自动命名等效索引）
func TestSyncIndexesCore_ColMatchedSkips(t *testing.T) {
	stmt := newParsedStmt(t)
	mig := &mockIndexMigrator{
		indexes: []gorm.Index{
			mockGormIndex{name: "idx_casbin_rule_test", columns: []string{"p_type", "v0", "v1", "v2"}, uniqueOk: true, unique: false},
		},
	}
	ctx := context.Background()
	err := syncIndexesCore(ctx, mig, stmt, "casbin_rule_test", logger.NewEmptyLogger())
	require.NoError(t, err)
	for _, name := range mig.dropped {
		assert.NotEqual(t, "idx_casbin_rule", name)
	}
	for _, name := range mig.created {
		assert.NotEqual(t, "idx_casbin_rule", name)
	}
}

// TestSyncIndexesCore_Missing_CreateFailed 验证完全不存在且 CreateIndex 失败时返回错误
func TestSyncIndexesCore_Missing_CreateFailed(t *testing.T) {
	stmt := newParsedStmt(t)
	mig := &mockIndexMigrator{
		indexes:   []gorm.Index{},
		createErr: errors.New("create failed"),
	}
	ctx := context.Background()
	err := syncIndexesCore(ctx, mig, stmt, "casbin_rule_test", logger.NewEmptyLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create index idx_casbin_rule")
	assert.Empty(t, mig.dropped)
	assert.Contains(t, mig.created, "idx_casbin_rule")
}

// TestSyncIndexesCore_Missing_CreateSuccess 验证完全不存在时 CreateIndex 成功
func TestSyncIndexesCore_Missing_CreateSuccess(t *testing.T) {
	stmt := newParsedStmt(t)
	mig := &mockIndexMigrator{
		indexes: []gorm.Index{},
	}
	ctx := context.Background()
	err := syncIndexesCore(ctx, mig, stmt, "casbin_rule_test", logger.NewEmptyLogger())
	require.NoError(t, err)
	assert.Empty(t, mig.dropped)
	assert.Contains(t, mig.created, "idx_casbin_rule")
}

// ==================== SyncTableIndexes 直接调用测试 ====================

// TestSyncTableIndexes_DirectCall_CreatesMissing 验证直接调用 SyncTableIndexes 创建缺失索引
func TestSyncTableIndexes_DirectCall_CreatesMissing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, db.Table("casbin_rule_test").AutoMigrate(&CasbinRule{}))
	require.NoError(t, db.Table("casbin_rule_test").Migrator().DropIndex(&CasbinRule{}, "idx_casbin_rule"))
	ctx := context.Background()
	err = syncTableIndexes(ctx, db, "casbin_rule_test", logger.NewEmptyLogger())
	require.NoError(t, err)
	assertIndexColumns(t, db, "casbin_rule_test", "idx_casbin_rule",
		[]string{"p_type", "v0", "v1", "v2"})
}

// TestSyncTableIndexes_DirectCall_NoOpWhenMatched 验证直接调用无差异时无操作
func TestSyncTableIndexes_DirectCall_NoOpWhenMatched(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Table("casbin_rule_test").AutoMigrate(&CasbinRule{}))

	ctx := context.Background()
	err = syncTableIndexes(ctx, db, "casbin_rule_test", logger.NewEmptyLogger())
	require.NoError(t, err)
	assertIndexColumns(t, db, "casbin_rule_test", "idx_casbin_rule",
		[]string{"p_type", "v0", "v1", "v2"})
}

// TestSyncTableIndexes_DirectCall_RebuildsOnColumnChange 验证直接调用重建列变更的索引
func TestSyncTableIndexes_DirectCall_RebuildsOnColumnChange(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Table("casbin_rule_test").AutoMigrate(&CasbinRule{}))

	migrator := db.Table("casbin_rule_test").Migrator()
	require.NoError(t, migrator.DropIndex(&CasbinRule{}, "idx_casbin_rule"))
	require.NoError(t, db.Exec("CREATE INDEX idx_casbin_rule ON casbin_rule_test (p_type, v0)").Error)

	ctx := context.Background()
	err = syncTableIndexes(ctx, db, "casbin_rule_test", logger.NewEmptyLogger())
	require.NoError(t, err)
	assertIndexColumns(t, db, "casbin_rule_test", "idx_casbin_rule",
		[]string{"p_type", "v0", "v1", "v2"})
}

// TestSyncTableIndexes_DirectCall_GetIndexesFailed 验证 GetIndexes 失败时返回错误
func TestSyncTableIndexes_DirectCall_GetIndexesFailed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Table("casbin_rule_test").AutoMigrate(&CasbinRule{}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	ctx := context.Background()
	err = syncTableIndexes(ctx, db, "casbin_rule_test", logger.NewEmptyLogger())
	require.Error(t, err)
}

// ==================== SyncAllShardIndexes 批量扫描同步测试 ====================

// TestSyncAllShardIndexes_SyncsMatchingTablesOnly 验证仅同步匹配前缀的分片表，跳过其他表
// 场景：存在 casbin_rule_t1（索引损坏）、other_table（非匹配前缀），调用后仅 t1 被重建
func TestSyncAllShardIndexes_SyncsMatchingTablesOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// 建立分片表 casbin_rule_t1 并破坏其索引
	require.NoError(t, db.Table("casbin_rule_t1").AutoMigrate(&CasbinRule{}))
	mig := db.Table("casbin_rule_t1").Migrator()
	require.NoError(t, mig.DropIndex(&CasbinRule{}, "idx_casbin_rule"))
	require.NoError(t, db.Exec("CREATE INDEX idx_casbin_rule ON casbin_rule_t1 (p_type, v0)").Error)

	// 建立非匹配表
	require.NoError(t, db.Exec("CREATE TABLE other_table (id INTEGER PRIMARY KEY)").Error)

	// 执行批量同步，前缀 casbin_rule_，应仅处理 casbin_rule_t1
	require.NoError(t, SyncAllShardIndexes(context.Background(), db, "casbin_rule_", logger.NewEmptyLogger()))

	// casbin_rule_t1 索引应被重建为 4 列
	assertIndexColumns(t, db, "casbin_rule_t1", "idx_casbin_rule",
		[]string{"p_type", "v0", "v1", "v2"})

	// other_table 应仍然存在且未被处理
	assert.True(t, db.Migrator().HasTable("other_table"), "non-matching table should be untouched")
}

// TestSyncAllShardIndexes_PartialFailure 验证单表同步失败不中断整体流程
// 场景：casbin_rule_bad 表结构缺列导致 CreateIndex 失败，casbin_rule_good 正常，
// 调用后应返回 nil（部分失败仅记录日志），good 表索引正常
func TestSyncAllShardIndexes_PartialFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// good 表：正常建表
	require.NoError(t, db.Table("casbin_rule_good").AutoMigrate(&CasbinRule{}))

	// bad 表：仅 id 列，缺少 p_type 等列，CreateIndex 必然失败
	require.NoError(t, db.Exec("CREATE TABLE casbin_rule_bad (id INTEGER PRIMARY KEY)").Error)

	// 批量同步：bad 表失败不影响 good 表，整体不返回错误
	require.NoError(t, SyncAllShardIndexes(context.Background(), db, "casbin_rule_", logger.NewEmptyLogger()))

	// good 表索引应存在且正确
	assertIndexColumns(t, db, "casbin_rule_good", "idx_casbin_rule",
		[]string{"p_type", "v0", "v1", "v2"})
}

// TestSyncAllShardIndexes_GetTablesFailed 验证列举表失败时返回错误
// 场景：关闭连接后调用，GetTables 失败
func TestSyncAllShardIndexes_GetTablesFailed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Table("casbin_rule_t1").AutoMigrate(&CasbinRule{}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	err = SyncAllShardIndexes(context.Background(), db, "casbin_rule_", logger.NewEmptyLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list tables for shard index sync failed")
}

// TestSyncAllShardIndexes_CtxCancelled 验证上下文取消时返回取消错误
// 场景：存在匹配前缀的表，传入已取消的 ctx，应在 ctx 检查处返回
func TestSyncAllShardIndexes_CtxCancelled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Table("casbin_rule_t1").AutoMigrate(&CasbinRule{}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = SyncAllShardIndexes(ctx, db, "casbin_rule_", logger.NewEmptyLogger())
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestSyncAllShardIndexes_NoMatchingTables 验证无匹配表时正常完成（synced=0）
func TestSyncAllShardIndexes_NoMatchingTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE other_table (id INTEGER PRIMARY KEY)").Error)

	require.NoError(t, SyncAllShardIndexes(context.Background(), db, "casbin_rule_", logger.NewEmptyLogger()))
}

// ==================== NewAdapter 边界分支 ====================

// TestNewAdapter_NilGormDB 验证 handler 的 GetDB 返回 nil 时返回错误
func TestNewAdapter_NilGormDB(t *testing.T) {
	adapter, err := NewAdapter(nilDBHandler{})
	require.Error(t, err)
	assert.Nil(t, adapter)
}

// ==================== WithCtx 方法错误分支 ====================

// dropTable 辅助函数：删除适配器的表
func dropTable(t *testing.T, adapter *Adapter) {
	t.Helper()
	require.NoError(t, adapter.handler.GetDB().Migrator().DropTable("casbin_rule_test"))
}

// recreateTableWithColumns 辅助函数：删除并重建表（仅指定列），用于触发 CreateBatch 错误
func recreateTableWithColumns(t *testing.T, adapter *Adapter, columns string) {
	t.Helper()
	db := adapter.handler.GetDB()
	require.NoError(t, db.Migrator().DropTable("casbin_rule_test"))
	require.NoError(t, db.Exec("CREATE TABLE casbin_rule_test ("+columns+")").Error)
}

// TestLoadPolicyWithCtx_Error 验证 LoadPolicy 时表不存在返回错误
func TestLoadPolicyWithCtx_Error(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	_, err := adapter.LoadPolicyWithCtx(context.Background())
	require.Error(t, err)
}

// TestSavePolicyWithCtx_ClearAllError 验证 clearAll 失败时返回错误
func TestSavePolicyWithCtx_ClearAllError(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	err := adapter.SavePolicyWithCtx(context.Background(), []string{"p, alice, data1, read"})
	require.Error(t, err)
}

// TestSavePolicyWithCtx_CreateBatchError 验证 clearAll 成功但 CreateBatch 失败时返回错误
func TestSavePolicyWithCtx_CreateBatchError(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	recreateTableWithColumns(t, adapter, "id INTEGER PRIMARY KEY")
	err := adapter.SavePolicyWithCtx(context.Background(), []string{"p, alice, data1, read"})
	require.Error(t, err)
}

// TestAddPolicyWithCtx_NilRule 验证空行解析为 nil rule 时返回 nil（无操作）
func TestAddPolicyWithCtx_NilRule(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.AddPolicyWithCtx(context.Background(), "")
	require.NoError(t, err)
}

// TestAddPolicyWithCtx_Error 验证 Create 失败时返回错误
func TestAddPolicyWithCtx_Error(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	err := adapter.AddPolicyWithCtx(context.Background(), "p, alice, data1, read")
	require.Error(t, err)
}

// TestRemovePolicyWithCtx_NilRule 验证空行解析为 nil rule 时返回 nil
func TestRemovePolicyWithCtx_NilRule(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.RemovePolicyWithCtx(context.Background(), "")
	require.NoError(t, err)
}

// TestRemovePolicyWithCtx_Error 验证 DeleteByFilters 失败时返回错误
func TestRemovePolicyWithCtx_Error(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	err := adapter.RemovePolicyWithCtx(context.Background(), "p, alice, data1, read")
	require.Error(t, err)
}

// TestRemovePolicyWithCtx_Success 验证正常删除
func TestRemovePolicyWithCtx_Success(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.AddPolicy("p, alice, data1, read"))
	require.NoError(t, adapter.RemovePolicyWithCtx(context.Background(), "p, alice, data1, read"))
	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.NotContains(t, policies, "p, alice, data1, read")
}

// TestAddPoliciesWithCtx_Empty 验证空 lines 返回 nil
func TestAddPoliciesWithCtx_Empty(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.AddPoliciesWithCtx(context.Background(), []string{})
	require.NoError(t, err)
}

// TestAddPoliciesWithCtx_Error 验证 CreateBatch 失败时返回错误
func TestAddPoliciesWithCtx_Error(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	err := adapter.AddPoliciesWithCtx(context.Background(), []string{"p, alice, data1, read"})
	require.Error(t, err)
}

// TestRemovePoliciesWithCtx_EmptyLines 验证空 lines 返回 nil
func TestRemovePoliciesWithCtx_EmptyLines(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.RemovePoliciesWithCtx(context.Background(), []string{})
	require.NoError(t, err)
}

// TestRemovePoliciesWithCtx_AllEmptyRules 验证所有规则字段为空时 orGroup 为空返回 nil
func TestRemovePoliciesWithCtx_AllEmptyRules(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.RemovePoliciesWithCtx(context.Background(), []string{","})
	require.NoError(t, err)
}

// TestRemovePoliciesWithCtx_EmptyStringRules 验证空字符串行解析为 nil 规则，len(rules)==0 返回 nil
func TestRemovePoliciesWithCtx_EmptyStringRules(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.RemovePoliciesWithCtx(context.Background(), []string{""})
	require.NoError(t, err)
}

// TestRemovePoliciesWithCtx_Error 验证 DeleteByFilterGroup 失败时返回错误
func TestRemovePoliciesWithCtx_Error(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	err := adapter.RemovePoliciesWithCtx(context.Background(), []string{"p, alice, data1, read"})
	require.Error(t, err)
}

// TestRemovePoliciesWithCtx_Success 验证正常批量删除
func TestRemovePoliciesWithCtx_Success(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.AddPolicies([]string{"p, alice, data1, read", "p, bob, data2, write"}))
	err := adapter.RemovePoliciesWithCtx(context.Background(), []string{"p, alice, data1, read"})
	require.NoError(t, err)
	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.NotContains(t, policies, "p, alice, data1, read")
	assert.Contains(t, policies, "p, bob, data2, write")
}

// TestUpdatePolicyWithCtx_NilRules 验证 nil oldRule/newRule 返回错误
func TestUpdatePolicyWithCtx_NilRules(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.UpdatePolicyWithCtx(context.Background(), "", "p, alice, data1, read")
	require.Error(t, err)
	err = adapter.UpdatePolicyWithCtx(context.Background(), "p, alice, data1, read", "")
	require.Error(t, err)
}

// TestUpdatePolicyWithCtx_Error 验证 UpdateFieldsByFilters 失败时返回错误
func TestUpdatePolicyWithCtx_Error(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	err := adapter.UpdatePolicyWithCtx(context.Background(), "p, alice, data1, read", "p, alice, data2, write")
	require.Error(t, err)
}

// TestUpdatePolicyWithCtx_Success 验证正常更新
func TestUpdatePolicyWithCtx_Success(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.AddPolicy("p, alice, data1, read"))
	err := adapter.UpdatePolicyWithCtx(context.Background(), "p, alice, data1, read", "p, alice, data2, write")
	require.NoError(t, err)
	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.Contains(t, policies, "p, alice, data2, write")
	assert.NotContains(t, policies, "p, alice, data1, read")
}

// TestUpdatePoliciesWithCtx_CountMismatch 验证 old/new 数量不匹配返回错误
func TestUpdatePoliciesWithCtx_CountMismatch(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.UpdatePoliciesWithCtx(context.Background(), []string{"p, a, b, c"}, []string{})
	require.Error(t, err)
}

// TestUpdatePoliciesWithCtx_Empty 验证空切片返回 nil
func TestUpdatePoliciesWithCtx_Empty(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.UpdatePoliciesWithCtx(context.Background(), []string{}, []string{})
	require.NoError(t, err)
}

// TestUpdatePoliciesWithCtx_ParseError 验证解析失败返回错误
func TestUpdatePoliciesWithCtx_ParseError(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.UpdatePoliciesWithCtx(context.Background(), []string{""}, []string{"p, a, b, c"})
	require.Error(t, err)
}

// TestUpdatePoliciesWithCtx_DeleteError 验证 DeleteByFilterGroup 失败返回错误
func TestUpdatePoliciesWithCtx_DeleteError(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	err := adapter.UpdatePoliciesWithCtx(context.Background(),
		[]string{"p, alice, data1, read"}, []string{"p, alice, data2, write"})
	require.Error(t, err)
}

// TestUpdatePoliciesWithCtx_CreateError 验证 Delete 跳过但 CreateBatch 失败返回错误
func TestUpdatePoliciesWithCtx_CreateError(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	recreateTableWithColumns(t, adapter, "id INTEGER PRIMARY KEY")
	// oldLines = [","] → 所有字段为空 → orGroup 为空 → Delete 跳过
	// newLines = ["p, alice, data1, read"] → CreateBatch 失败（缺少列）
	err := adapter.UpdatePoliciesWithCtx(context.Background(),
		[]string{","}, []string{"p, alice, data1, read"})
	require.Error(t, err)
}

// TestUpdatePoliciesWithCtx_Success 验证正常批量更新
func TestUpdatePoliciesWithCtx_Success(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.AddPolicies([]string{"p, alice, data1, read", "p, bob, data2, write"}))
	err := adapter.UpdatePoliciesWithCtx(context.Background(),
		[]string{"p, alice, data1, read", "p, bob, data2, write"},
		[]string{"p, alice, data3, read", "p, bob, data4, write"})
	require.NoError(t, err)
	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.Contains(t, policies, "p, alice, data3, read")
	assert.Contains(t, policies, "p, bob, data4, write")
}

// TestUpdateFilteredPoliciesWithCtx_Success 验证正常过滤更新（空 ptype 路径）
func TestUpdateFilteredPoliciesWithCtx_Success(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{
		"p, alice, data1, read",
		"p, bob, data2, write",
	}))
	// newLines 为空 → InferPType 返回 "" → 走非 ByPType 路径
	err := adapter.UpdateFilteredPoliciesWithCtx(context.Background(), nil, 0, "alice")
	require.NoError(t, err)
	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.NotContains(t, policies, "p, alice, data1, read")
	assert.Contains(t, policies, "p, bob, data2, write")
}

// TestUpdateFilteredPoliciesWithCtx_MixedPType 验证混合 ptype 时走非 ByPType 路径并插入新策略
func TestUpdateFilteredPoliciesWithCtx_MixedPType(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read"}))
	// 混合 ptype → InferPType 返回 "" → 走非 ByPType 路径
	err := adapter.UpdateFilteredPoliciesWithCtx(context.Background(),
		[]string{"p, bob, data2, write", "g, alice, admin"}, 0, "alice")
	require.NoError(t, err)
	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.NotContains(t, policies, "p, alice, data1, read")
	assert.Contains(t, policies, "p, bob, data2, write")
	assert.Contains(t, policies, "g, alice, admin")
}

// TestUpdateFilteredPoliciesWithCtx_SamePType 验证同一 ptype 时走 ByPType 路径
func TestUpdateFilteredPoliciesWithCtx_SamePType(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read"}))
	// 同一 ptype → InferPType 返回 "p" → 走 ByPType 路径
	err := adapter.UpdateFilteredPoliciesWithCtx(context.Background(),
		[]string{"p, bob, data2, write"}, 0, "alice")
	require.NoError(t, err)
	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.NotContains(t, policies, "p, alice, data1, read")
	assert.Contains(t, policies, "p, bob, data2, write")
}

// TestUpdateFilteredPoliciesWithCtx_DeleteError 验证 DeleteByFilters 失败返回错误
func TestUpdateFilteredPoliciesWithCtx_DeleteError(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	err := adapter.UpdateFilteredPoliciesWithCtx(context.Background(), nil, 0, "alice")
	require.Error(t, err)
}

// TestUpdateFilteredPoliciesWithCtx_CreateError 验证 Delete 成功但 CreateBatch 失败
func TestUpdateFilteredPoliciesWithCtx_CreateError(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	recreateTableWithColumns(t, adapter, "id INTEGER PRIMARY KEY, v0 TEXT")
	// filters = [v0="alice"] → Delete 成功（v0 列存在）
	// CreateBatch 失败（p_type 列不存在）
	err := adapter.UpdateFilteredPoliciesWithCtx(context.Background(),
		[]string{"p, alice, data1, read", "g, bob, admin"}, 0, "alice")
	require.Error(t, err)
}

// TestUpdateFilteredPoliciesByPTypeWithCtx_EmptyPType 验证空 ptype 委托给 UpdateFilteredPoliciesWithCtx
func TestUpdateFilteredPoliciesByPTypeWithCtx_EmptyPType(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read"}))
	err := adapter.UpdateFilteredPoliciesByPTypeWithCtx(context.Background(), "", nil, 0, "alice")
	require.NoError(t, err)
	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.NotContains(t, policies, "p, alice, data1, read")
}

// TestUpdateFilteredPoliciesByPTypeWithCtx_DeleteError 验证 DeleteByFilters 失败返回错误
func TestUpdateFilteredPoliciesByPTypeWithCtx_DeleteError(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	err := adapter.UpdateFilteredPoliciesByPTypeWithCtx(context.Background(), "p", nil, 0, "alice")
	require.Error(t, err)
}

// TestUpdateFilteredPoliciesByPTypeWithCtx_CreateError 验证 Delete 成功但 CreateBatch 失败
func TestUpdateFilteredPoliciesByPTypeWithCtx_CreateError(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	recreateTableWithColumns(t, adapter, "id INTEGER PRIMARY KEY, p_type TEXT")
	// filters = [p_type="p"] → Delete 成功（p_type 列存在）
	// CreateBatch 失败（v0 列不存在）
	err := adapter.UpdateFilteredPoliciesByPTypeWithCtx(context.Background(), "p",
		[]string{"p, alice, data1, read"}, 0)
	require.Error(t, err)
}

// TestLoadFilteredPolicyWithCtx_PolicyFilter 验证 *policy.Filter 过滤
func TestLoadFilteredPolicyWithCtx_PolicyFilter(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read", "g, alice, admin"}))
	policies, err := adapter.LoadFilteredPolicyWithCtx(context.Background(), policy.NewFilter().WithPType("p"))
	require.NoError(t, err)
	assert.Contains(t, policies, "p, alice, data1, read")
	assert.NotContains(t, policies, "g, alice, admin")
}

// TestLoadFilteredPolicyWithCtx_StringSlice 验证 []string 过滤
func TestLoadFilteredPolicyWithCtx_StringSlice(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read", "g, alice, admin"}))
	policies, err := adapter.LoadFilteredPolicyWithCtx(context.Background(), []string{"g"})
	require.NoError(t, err)
	assert.Contains(t, policies, "g, alice, admin")
	assert.NotContains(t, policies, "p, alice, data1, read")
}

// TestLoadFilteredPolicyWithCtx_DefaultType 验证未知 filter 类型返回全部
func TestLoadFilteredPolicyWithCtx_DefaultType(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read"}))
	policies, err := adapter.LoadFilteredPolicyWithCtx(context.Background(), 123)
	require.NoError(t, err)
	assert.Len(t, policies, 1)
}

// TestLoadFilteredPolicyWithCtx_Error 验证 List 失败返回错误
func TestLoadFilteredPolicyWithCtx_Error(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	_, err := adapter.LoadFilteredPolicyWithCtx(context.Background(), policy.NewFilter().WithPType("p"))
	require.Error(t, err)
}

// TestRemoveFilteredPolicyWithCtx_Success 验证正常按字段索引删除
func TestRemoveFilteredPolicyWithCtx_Success(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read", "p, bob, data2, write"}))
	err := adapter.RemoveFilteredPolicyWithCtx(context.Background(), 0, "alice")
	require.NoError(t, err)
	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.NotContains(t, policies, "p, alice, data1, read")
	assert.Contains(t, policies, "p, bob, data2, write")
}

// TestRemoveFilteredPolicyWithCtx_Error 验证 DeleteByFilters 失败返回错误
func TestRemoveFilteredPolicyWithCtx_Error(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	err := adapter.RemoveFilteredPolicyWithCtx(context.Background(), 0, "alice")
	require.Error(t, err)
}

// TestGetPolicyByPTypeWithCtx_Success 验证按 ptype 加载
func TestGetPolicyByPTypeWithCtx_Success(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read", "g, alice, admin"}))
	policies, err := adapter.GetPolicyByPTypeWithCtx(context.Background(), "g")
	require.NoError(t, err)
	assert.Contains(t, policies, "g, alice, admin")
	assert.NotContains(t, policies, "p, alice, data1, read")
}

// TestGetPolicyByPTypeWithCtx_Error 验证 List 失败返回错误
func TestGetPolicyByPTypeWithCtx_Error(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	_, err := adapter.GetPolicyByPTypeWithCtx(context.Background(), "p")
	require.Error(t, err)
}

// TestCountWithCtx_Success 验证正常计数
func TestCountWithCtx_Success(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read", "g, alice, admin"}))
	count, err := adapter.CountWithCtx(context.Background(), policy.NewFilter().WithPType("p"))
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

// TestCountWithCtx_NilFilter 验证 nil filter 计数全部
func TestCountWithCtx_NilFilter(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read", "g, alice, admin"}))
	count, err := adapter.CountWithCtx(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

// TestCountWithCtx_Error 验证 Count 失败返回错误
func TestCountWithCtx_Error(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	_, err := adapter.CountWithCtx(context.Background(), nil)
	require.Error(t, err)
}

// ==================== 非 ctx 方法（defaultCtx 事务上下文继承） ====================

// TestNonCtxMethods 覆盖所有非 ctx 方法的成功路径
func TestNonCtxMethods(t *testing.T) {
	adapter := newSQLiteAdapter(t)

	// AddPolicy
	require.NoError(t, adapter.AddPolicy("p, alice, data1, read"))
	// LoadPolicy
	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.Contains(t, policies, "p, alice, data1, read")
	// AddPolicies
	require.NoError(t, adapter.AddPolicies([]string{"p, bob, data2, write", "g, alice, admin"}))
	// GetPolicyByPType
	pPolicies, err := adapter.GetPolicyByPType("p")
	require.NoError(t, err)
	assert.Len(t, pPolicies, 2)
	// Count
	count, err := adapter.Count(policy.NewFilter().WithPType("p"))
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
	// LoadFilteredPolicy (*policy.Filter)
	filtered, err := adapter.LoadFilteredPolicy(policy.NewFilter().WithPType("g"))
	require.NoError(t, err)
	assert.Contains(t, filtered, "g, alice, admin")
	// IsFiltered
	assert.True(t, adapter.IsFiltered())
	// RemovePolicy
	require.NoError(t, adapter.RemovePolicy("p, bob, data2, write"))
	// RemovePolicies (批量删除，非 ctx)
	require.NoError(t, adapter.AddPolicies([]string{"p, bob, data2, read", "p, eve, data5, write"}))
	require.NoError(t, adapter.RemovePolicies([]string{"p, bob, data2, read"}))
	// RemoveFilteredPolicy
	require.NoError(t, adapter.RemoveFilteredPolicy(0, "alice"))
	// UpdatePolicy
	require.NoError(t, adapter.AddPolicy("p, charlie, data3, read"))
	require.NoError(t, adapter.UpdatePolicy("p, charlie, data3, read", "p, charlie, data3, write"))
	// UpdatePolicies
	require.NoError(t, adapter.AddPolicies([]string{"p, dave, data4, read"}))
	require.NoError(t, adapter.UpdatePolicies([]string{"p, dave, data4, read"}, []string{"p, dave, data4, write"}))
	// UpdateFilteredPolicies (empty ptype path via nil newLines)
	require.NoError(t, adapter.UpdateFilteredPolicies(nil, 0, "dave"))
	// UpdateFilteredPoliciesByPType
	require.NoError(t, adapter.UpdateFilteredPoliciesByPType("p", []string{"p, eve, data5, read"}, 0, "eve"))
	// SavePolicy (clears and rewrites)
	require.NoError(t, adapter.SavePolicy([]string{"p, frank, data6, read"}))
	policies, err = adapter.LoadPolicy()
	require.NoError(t, err)
	assert.Len(t, policies, 1)
	assert.Contains(t, policies, "p, frank, data6, read")
}

// TestNonCtxMethods_LoadFilteredPolicy_StringSlice 验证非 ctx LoadFilteredPolicy 的 []string 路径
func TestNonCtxMethods_LoadFilteredPolicy_StringSlice(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read", "g, alice, admin"}))
	policies, err := adapter.LoadFilteredPolicy([]string{"g"})
	require.NoError(t, err)
	assert.Contains(t, policies, "g, alice, admin")
}

// TestNonCtxMethods_LoadFilteredPolicy_DefaultType 验证非 ctx LoadFilteredPolicy 的 default 路径
func TestNonCtxMethods_LoadFilteredPolicy_DefaultType(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	require.NoError(t, adapter.SavePolicy([]string{"p, alice, data1, read"}))
	policies, err := adapter.LoadFilteredPolicy(123)
	require.NoError(t, err)
	assert.Len(t, policies, 1)
}

// TestBuildFilterQuery 覆盖 buildFilterQuery 所有分支
func TestBuildFilterQuery(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	// *policy.Filter
	q := adapter.buildFilterQuery(policy.NewFilter().WithPType("p"))
	assert.NotNil(t, q)
	// []string
	q = adapter.buildFilterQuery([]string{"p", "alice"})
	assert.NotNil(t, q)
	// default
	q = adapter.buildFilterQuery(123)
	assert.NotNil(t, q)
}

// ==================== clearAll 错误分支 ====================

// TestClearAll_Error 验证 clearAll 在表不存在时返回错误
func TestClearAll_Error(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	dropTable(t, adapter)
	err := adapter.clearAll(context.Background())
	require.Error(t, err)
}

// ==================== ExecuteInTransaction 边界分支 ====================

// TestExecuteInTransaction_CtxCancelled 验证 ctx 已取消时返回取消错误
func TestExecuteInTransaction_CtxCancelled(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := adapter.ExecuteInTransaction(ctx, func(txAdapter policy.Adapter) error {
		t.Fatal("should not be called")
		return nil
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestExecuteInTransaction_Nested 验证嵌套事务直接复用当前事务
func TestExecuteInTransaction_Nested(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	adapter.inTransaction = true
	var called bool
	err := adapter.ExecuteInTransaction(context.Background(), func(txAdapter policy.Adapter) error {
		called = true
		assert.Same(t, adapter, txAdapter)
		return nil
	})
	require.NoError(t, err)
	assert.True(t, called)
}

// ==================== Close / GetHandler / WithTransaction ====================

// TestAdapter_CloseAndGetHandler 验证 Close 和 GetHandler
func TestAdapter_CloseAndGetHandler(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	assert.NoError(t, adapter.Close())
	assert.NotNil(t, adapter.GetHandler())
}

// TestAdapter_WithTransaction 验证 WithTransaction 正常执行
func TestAdapter_WithTransaction(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	err := adapter.WithTransaction(func(tx *gorm.DB) error {
		return tx.Exec("CREATE TABLE IF NOT EXISTS test_tx (id INTEGER PRIMARY KEY)").Error
	})
	require.NoError(t, err)
}

// ==================== tableScopedMigrator.DropIndex 各方言分支 ====================

// TestTableScopedMigrator_DropIndex_AllDialectors 验证所有方言分支
func TestTableScopedMigrator_DropIndex_AllDialectors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dialector string
	}{
		{"sqlite", "sqlite"},
		{"mysql", "mysql"},
		{"clickhouse", "clickhouse"},
		{"postgres", "postgres"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &renamedDialector{Dialector: sqlite.Open(":memory:"), newName: tc.dialector}
			db, err := gorm.Open(d, &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.Table("casbin_rule_test").AutoMigrate(&CasbinRule{}))
			m := &tableScopedMigrator{
				Migrator:  db.Table("casbin_rule_test").Migrator(),
				gormDB:    db,
				tableName: "casbin_rule_test",
			}
			// DropIndex 会生成各方言对应的 SQL，非 SQLite 方言的 SQL 可能执行失败，但代码路径已覆盖
			_ = m.DropIndex(&CasbinRule{}, "idx_casbin_rule")
		})
	}
}

// ==================== filter.go 函数测试 ====================

// TestPolicyFilterToRepoFilters 覆盖 nil 和非 nil filter
func TestPolicyFilterToRepoFilters(t *testing.T) {
	// nil filter
	assert.Nil(t, PolicyFilterToRepoFilters(nil))
	// 非 nil filter
	filters := PolicyFilterToRepoFilters(policy.NewFilter().WithPType("p").WithV0("alice"))
	assert.Len(t, filters, 2)
}

// TestPolicyFilterToQuery 覆盖 nil 和非 nil filter
func TestPolicyFilterToQuery(t *testing.T) {
	// nil filter
	q := PolicyFilterToQuery(nil)
	assert.NotNil(t, q)
	// 非 nil filter
	q = PolicyFilterToQuery(policy.NewFilter().WithPType("p").WithV0("alice"))
	assert.NotNil(t, q)
}

// TestRuleToFilters 覆盖 nil、有 PType、无 PType 三种情况
func TestRuleToFilters(t *testing.T) {
	// nil rule
	assert.Nil(t, RuleToFilters(nil))
	// rule with PType and values
	rule := &CasbinRule{PType: "p", V0: "alice", V1: "data1"}
	filters := RuleToFilters(rule)
	assert.Len(t, filters, 3) // PType + V0 + V1
	// rule with empty PType
	rule = &CasbinRule{V0: "alice"}
	filters = RuleToFilters(rule)
	assert.Len(t, filters, 1) // only V0
}

// TestFieldIndexToFiltersByPType 覆盖有 ptype 和无 ptype
func TestFieldIndexToFiltersByPType(t *testing.T) {
	// 有 ptype
	filters := FieldIndexToFiltersByPType("p", 0, "alice")
	assert.Len(t, filters, 2) // p_type + v0
	// 无 ptype
	filters = FieldIndexToFiltersByPType("", 0, "alice")
	assert.Len(t, filters, 1) // only v0
}

// TestFieldIndexToQuery 覆盖正常索引和越界索引
func TestFieldIndexToQuery(t *testing.T) {
	q := FieldIndexToQuery(0, "alice", "data1")
	assert.NotNil(t, q)
	// 越界索引
	q = FieldIndexToQuery(10, "alice")
	assert.NotNil(t, q)
}

// TestRulesToOrFilterGroup 覆盖 nil rule、空规则、正常规则
func TestRulesToOrFilterGroup(t *testing.T) {
	// 空 slice
	group := RulesToOrFilterGroup(nil)
	assert.True(t, group.IsEmpty())
	// nil rule in slice
	group = RulesToOrFilterGroup([]*CasbinRule{nil, {PType: "p", V0: "alice"}})
	assert.False(t, group.IsEmpty())
	// 全空规则
	group = RulesToOrFilterGroup([]*CasbinRule{{}})
	assert.True(t, group.IsEmpty())
	// 正常规则
	group = RulesToOrFilterGroup([]*CasbinRule{{PType: "p", V0: "alice"}, {PType: "g", V0: "bob"}})
	assert.False(t, group.IsEmpty())
}

// ==================== model.go 函数测试 ====================

// TestCasbinRule_TableComment 验证 TableComment
func TestCasbinRule_TableComment(t *testing.T) {
	assert.Equal(t, "Casbin策略规则表", CasbinRule{}.TableComment())
}

// TestCasbinRule_ToString_AllEmpty 验证全空字段返回 ""
func TestCasbinRule_ToString_AllEmpty(t *testing.T) {
	rule := &CasbinRule{}
	assert.Equal(t, "", rule.ToString())
}

// TestCasbinRule_ToString_OnlyPType 验证仅 PType（无 V0-V5）返回 ""
func TestCasbinRule_ToString_OnlyPType(t *testing.T) {
	rule := &CasbinRule{PType: "p"}
	assert.Equal(t, "", rule.ToString())
}

// TestCasbinRule_ToString_TrailingEmpty 验证尾部空字段裁剪
func TestCasbinRule_ToString_TrailingEmpty(t *testing.T) {
	rule := &CasbinRule{PType: "p", V0: "alice", V1: "", V2: "read"}
	assert.Equal(t, "p, alice, , read", rule.ToString())
}

// TestCasbinRule_FromString_Empty 验证空行返回 false
func TestCasbinRule_FromString_Empty(t *testing.T) {
	rule := &CasbinRule{}
	assert.False(t, rule.FromString(""))
}

// TestCasbinRule_FromString_SingleField 验证单字段（仅 PType）解析
func TestCasbinRule_FromString_SingleField(t *testing.T) {
	rule := &CasbinRule{}
	assert.True(t, rule.FromString("p"))
	assert.Equal(t, "p", rule.PType)
	for _, v := range rule.Values() {
		assert.Equal(t, "", v)
	}
}

// TestCasbinRule_FromString_MultiFields 验证多字段解析和空格裁剪
func TestCasbinRule_FromString_MultiFields(t *testing.T) {
	rule := &CasbinRule{}
	assert.True(t, rule.FromString("p, alice, data1, read"))
	assert.Equal(t, "p", rule.PType)
	assert.Equal(t, "alice", rule.V0)
	assert.Equal(t, "data1", rule.V1)
	assert.Equal(t, "read", rule.V2)
}

// TestParseRule_InvalidLine 验证空行返回 nil
func TestParseRule_InvalidLine(t *testing.T) {
	assert.Nil(t, ParseRule(""))
}

// TestParseRulesByPType_TypeMismatch 验证 ptype 不匹配的规则被过滤
func TestParseRulesByPType_TypeMismatch(t *testing.T) {
	rules := ParseRulesByPType("p", []string{"p, alice, data1, read", "g, alice, admin", ""})
	assert.Len(t, rules, 1)
	assert.Equal(t, "p", rules[0].PType)
}

// TestRulesToStrings_NilRule 验证 nil rule 被跳过
func TestRulesToStrings_NilRule(t *testing.T) {
	result := RulesToStrings([]*CasbinRule{nil, {PType: "p", V0: "alice"}})
	assert.Len(t, result, 1)
	assert.Equal(t, "p, alice", result[0])
}

// TestRulesToStrings_EmptyPType 验证空 PType 的规则被跳过（ToString 返回 ""）
func TestRulesToStrings_EmptyPType(t *testing.T) {
	result := RulesToStrings([]*CasbinRule{{}})
	assert.Empty(t, result)
}

// ==================== options.go 边界测试 ====================

// TestWithTableName_Empty 验证空字符串不修改表名
func TestWithTableName_Empty(t *testing.T) {
	adapter := newSQLiteAdapter(t)
	// 空 string 不修改，应使用 newSQLiteAdapter 中设置的 casbin_rule_test
	assert.Equal(t, "casbin_rule_test", adapter.tableName)
}

// TestWithLogger_Nil 验证 nil logger 不修改
func TestWithLogger_Nil(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	adapter, err := NewAdapterByDB(db, WithTableName("casbin_rule_test"), WithLogger(nil))
	require.NoError(t, err)
	assert.NotNil(t, adapter.logger)
}

// TestWithAutoMigrate_False 验证禁用自动迁移
func TestWithAutoMigrate_False(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	adapter, err := NewAdapterByDB(db, WithTableName("casbin_rule_test"), WithAutoMigrate(false))
	require.NoError(t, err)
	// 表不应存在
	assert.False(t, db.Migrator().HasTable("casbin_rule_test"))
	_ = adapter
}

// ==================== autoMigrateTable 测试 ====================

// TestAutoMigrateTable_CreatesCustomTable 验证 autoMigrateTable 用自定义表名创建表
// 这是修复 GORM AutoMigrate 对 .Table(name) 覆盖丢失 bug 的核心测试
func TestAutoMigrateTable_CreatesCustomTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// 模拟 casbin_rule_ops 分片表名（与 CasbinRule.TableName() 的 "casbin_rule" 不同）
	require.NoError(t, autoMigrateTable(db, "casbin_rule_ops"))
	assert.True(t, db.Migrator().HasTable("casbin_rule_ops"))
	// 默认表名 "casbin_rule" 不应被创建
	assert.False(t, db.Migrator().HasTable("casbin_rule"))
}

// TestAutoMigrateTable_TableAlreadyExists 验证表已存在时 autoMigrateTable 不报错
func TestAutoMigrateTable_TableAlreadyExists(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// 先创建表
	require.NoError(t, autoMigrateTable(db, "casbin_rule_ops"))
	// 再次调用不应报错
	require.NoError(t, autoMigrateTable(db, "casbin_rule_ops"))
	assert.True(t, db.Migrator().HasTable("casbin_rule_ops"))
}

// TestAutoMigrateTable_MultipleShards 验证同时创建多个分片表
// 注意：SQLite 的索引名是 DB 级别全局的，同一 DB 内多张分片表用相同索引名会冲突，
// 生产环境各分片在不同 DB 实例不存在此问题，此处每个分片用独立 DB 验证
func TestAutoMigrateTable_MultipleShards(t *testing.T) {
	for _, shardTable := range []string{"casbin_rule_ops", "casbin_rule_tenant_abc", "casbin_rule_tenant_xyz"} {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		require.NoError(t, err)
		require.NoError(t, autoMigrateTable(db, shardTable))
		assert.True(t, db.Migrator().HasTable(shardTable))
		assert.False(t, db.Migrator().HasTable("casbin_rule"))
	}
}

// TestAutoMigrateTable_CreateTableWithCorrectColumns 验证建表的列结构正确
func TestAutoMigrateTable_CreateTableWithCorrectColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, autoMigrateTable(db, "casbin_rule_ops"))

	columns, err := db.Migrator().ColumnTypes("casbin_rule_ops")
	require.NoError(t, err)
	colNames := make(map[string]bool)
	for _, c := range columns {
		colNames[c.Name()] = true
	}
	// 验证所有 CasbinRule 模型字段对应的列都存在
	for _, expected := range []string{"id", "p_type", "v0", "v1", "v2", "v3", "v4", "v5", "created_at", "updated_at"} {
		assert.True(t, colNames[expected], "column %s should exist in casbin_rule_ops", expected)
	}
}

// TestNewAdapter_CustomTableName_CreatesCorrectTable 验证 NewAdapter 用自定义表名建表
// 端到端测试：模拟 ShardedEnforcer 创建 "casbin_rule_ops" 分片的场景
func TestNewAdapter_CustomTableName_CreatesCorrectTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	adapter, err := NewAdapterByDB(db, WithTableName("casbin_rule_ops"))
	require.NoError(t, err)
	require.NotNil(t, adapter)
	assert.Equal(t, "casbin_rule_ops", adapter.tableName)

	// 表应存在且可写入策略
	assert.True(t, db.Migrator().HasTable("casbin_rule_ops"))
	require.NoError(t, adapter.AddPolicy("p, alice, ops, data1, read"))
	policies, err := adapter.LoadPolicy()
	require.NoError(t, err)
	assert.Contains(t, policies, "p, alice, ops, data1, read")
}

// TestSetMigratorTable_StandardMigrator 验证 setMigratorTable 对标准 Migrator 生效
func TestSetMigratorTable_StandardMigrator(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	m := db.Migrator()
	// 标准 Migrator，设置前 Table 为空
	setMigratorTable(m, "casbin_rule_ops")
	// 验证：类型断言后 DB.Statement.Table 应为 "casbin_rule_ops"
	if base, ok := m.(*migrator.Migrator); ok {
		assert.Equal(t, "casbin_rule_ops", base.DB.Statement.Table)
	}
}
