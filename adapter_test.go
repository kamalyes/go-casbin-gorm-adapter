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
	"gorm.io/gorm/schema"
)

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
