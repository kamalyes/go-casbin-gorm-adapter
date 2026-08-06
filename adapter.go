/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-03-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-03-28 00:00:00
 * @FilePath: \go-casbin-gorm-adapter\adapter.go
 * @Description: Casbin GORM 适配器 - 基于关系型数据库的策略持久化存储
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/kamalyes/go-casbin/errors"
	"github.com/kamalyes/go-casbin/policy"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-sqlbuilder/constants"
	"github.com/kamalyes/go-sqlbuilder/db"
	"github.com/kamalyes/go-sqlbuilder/repository"
	"github.com/kamalyes/go-toolbox/pkg/contextx"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// 编译时接口断言，确保 Adapter 实现了所有必需的接口
var (
	_ policy.Adapter               = (*Adapter)(nil) // 基础适配器接口
	_ policy.FilteredAdapter       = (*Adapter)(nil) // 过滤适配器接口
	_ policy.BatchAdapter          = (*Adapter)(nil) // 批量操作适配器接口
	_ policy.UpdatableAdapter      = (*Adapter)(nil) // 可更新适配器接口
	_ policy.PTypeUpdatableAdapter = (*Adapter)(nil)
	_ policy.TransactionalAdapter  = (*Adapter)(nil) // 事务适配器接口
)

// Adapter 基于 GORM 的策略存储适配器
// 支持 MySQL、PostgreSQL、SQLite 等关系型数据库
// 通过 go-sqlbuilder 的 BaseRepository 实现数据库 CRUD 操作
type Adapter struct {
	handler       db.Handler                             // 数据库处理器，封装 GORM DB 实例
	repo          *repository.BaseRepository[CasbinRule] // 基于 go-sqlbuilder 的仓储层
	logger        logger.ILogger                         // 日志记录器
	filtered      bool                                   // 是否已使用过滤加载
	tableName     string                                 // 数据库表名
	autoMigrate   bool                                   // 是否自动迁移表结构
	txCtx         context.Context                        // 事务上下文，ExecuteInTransaction 内设置，供非 ctx 方法继承使用
	inTransaction bool                                   // 是否已处于数据库事务中（作为 txAdapter 创建时为 true）
}

// defaultCtx 返回适配器当前应使用的上下文
// 处于事务中时返回事务上下文（支持取消和链路追踪），否则返回 context.Background()
func (a *Adapter) defaultCtx() context.Context {
	if a.txCtx != nil {
		return a.txCtx
	}
	return context.Background()
}

// NewAdapter 创建 GORM 适配器
// handler: go-sqlbuilder 的数据库处理器
// opts: 可选配置项（表名、日志、自动迁移等）
func NewAdapter(handler db.Handler, opts ...Option) (*Adapter, error) {
	if handler == nil {
		return nil, errors.NewPolicyAdapterFailedError("database handler is nil")
	}

	a := &Adapter{
		handler:     handler,
		tableName:   DefaultTableName,
		logger:      logger.NewEmptyLogger(),
		autoMigrate: true,
	}

	// 应用可选配置
	for _, opt := range opts {
		opt(a)
	}

	// 获取底层 GORM DB 实例
	gormDB := handler.GetDB()
	if gormDB == nil {
		return nil, errors.NewPolicyAdapterFailedError("gorm db instance is nil")
	}

	// 自动迁移表结构（默认开启）
	if a.autoMigrate {
		if err := gormDB.Table(a.tableName).AutoMigrate(&CasbinRule{}); err != nil {
			return nil, errors.NewPolicyAutoMigrateFailedError(err.Error())
		}
	}

	// 初始化 BaseRepository，用于后续的 CRUD 操作
	a.repo = repository.NewBaseRepository[CasbinRule](handler, a.logger, a.tableName)
	a.logger.InfoKV("GORM adapter initialized", "table", a.tableName)

	return a, nil
}

// NewAdapterByDB 通过已有的 GORM DB 实例创建适配器
// 适用于已有数据库连接的场景，避免重复创建连接
func NewAdapterByDB(gormDB *gorm.DB, opts ...Option) (*Adapter, error) {
	if gormDB == nil {
		return nil, errors.NewPolicyAdapterFailedError("gorm db is nil")
	}
	return NewAdapter(db.MustNewGormHandler(gormDB), opts...)
}

// ==================== WithCtx 方法（核心实现） ====================
// 所有 WithCtx 方法接受 context.Context 参数，支持超时控制和链路追踪
// 使用 contextx.OrBackground 确保 ctx 不为 nil

// LoadPolicyWithCtx 从数据库加载所有策略规则
// 返回策略字符串切片，格式为 "p, sub, obj, act" 或 "g, user, role"
func (a *Adapter) LoadPolicyWithCtx(ctx context.Context) ([]string, error) {
	ctx = contextx.OrBackground(ctx)
	rules, err := a.repo.List(ctx, repository.NewQuery())
	if err != nil {
		return nil, errors.NewPolicyLoadFailedError(err.Error())
	}

	policies := RulesToStrings(rules)
	a.logger.InfoKV("Policies loaded from database", "count", len(policies))
	return policies, nil
}

// SavePolicyWithCtx 将所有策略保存到数据库（先清空再写入）
func (a *Adapter) SavePolicyWithCtx(ctx context.Context, policies []string) error {
	ctx = contextx.OrBackground(ctx)
	rules := ParseRules(policies)
	a.logger.DebugContextKV(ctx, "Casbin savePolicy start", "table", a.tableName, "count", len(policies))

	err := a.ExecuteInTransaction(ctx, func(txAdapter policy.Adapter) error {
		tx := txAdapter.(*Adapter)
		// 先清空所有现有策略
		if err := tx.clearAll(ctx); err != nil {
			return errors.NewPolicyClearFailedError(err.Error())
		}
		// 批量写入新策略
		if len(rules) > 0 {
			if err := tx.repo.CreateBatch(ctx, rules...); err != nil {
				return errors.NewPolicySaveFailedError(err.Error())
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.logger.DebugContextKV(ctx, "Casbin savePolicy done", "table", a.tableName, "count", len(policies))
	return nil
}

// AddPolicyWithCtx 向数据库添加单条策略
func (a *Adapter) AddPolicyWithCtx(ctx context.Context, line string) error {
	ctx = contextx.OrBackground(ctx)
	rule := ParseRule(line)
	if rule == nil {
		return nil
	}

	if _, err := a.repo.Create(ctx, rule); err != nil {
		return errors.NewPolicyAddFailedError(err.Error())
	}

	a.logger.DebugContextKV(ctx, "Policy added to database", "policy", line)
	return nil
}

// RemovePolicyWithCtx 从数据库删除单条策略
// 通过将策略行解析为过滤条件，精确匹配删除
func (a *Adapter) RemovePolicyWithCtx(ctx context.Context, line string) error {
	ctx = contextx.OrBackground(ctx)
	rule := ParseRule(line)
	if rule == nil {
		return nil
	}

	filters := RuleToFilters(rule)
	if err := a.repo.DeleteByFilters(ctx, filters...); err != nil {
		return errors.NewPolicyRemoveFailedError(err.Error())
	}

	a.logger.DebugContextKV(ctx, "Policy removed from database", "policy", line)
	return nil
}

// AddPoliciesWithCtx 批量添加策略到数据库
func (a *Adapter) AddPoliciesWithCtx(ctx context.Context, lines []string) error {
	ctx = contextx.OrBackground(ctx)
	rules := ParseRules(lines)
	if len(rules) == 0 {
		return nil
	}

	if err := a.repo.CreateBatch(ctx, rules...); err != nil {
		return errors.NewPolicyBatchAddFailedError(err.Error())
	}

	a.logger.DebugContextKV(ctx, "Policies batch added to database", "count", len(rules))
	return nil
}

// RemovePoliciesWithCtx 批量从数据库删除策略
// 使用事务保证原子性：所有删除操作要么全部成功，要么全部回滚
func (a *Adapter) RemovePoliciesWithCtx(ctx context.Context, lines []string) error {
	ctx = contextx.OrBackground(ctx)
	if len(lines) == 0 {
		return nil
	}

	rules := ParseRules(lines)
	if len(rules) == 0 {
		return nil
	}

	orGroup := RulesToOrFilterGroup(rules)
	if orGroup.IsEmpty() {
		return nil
	}

	return a.ExecuteInTransaction(ctx, func(txAdapter policy.Adapter) error {
		tx := txAdapter.(*Adapter)
		// 批量删除策略（单条 OR 条件 DELETE，复用 BaseRepository.DeleteByFilterGroup）
		if err := tx.repo.DeleteByFilterGroup(ctx, orGroup); err != nil {
			return errors.NewPolicyBatchRemoveFailedError(err.Error())
		}
		return nil
	})
}

// UpdatePolicyWithCtx 更新单条策略
// 先根据旧策略定位记录，再更新为新策略的字段值
func (a *Adapter) UpdatePolicyWithCtx(ctx context.Context, oldLine, newLine string) error {
	ctx = contextx.OrBackground(ctx)
	oldRule := ParseRule(oldLine)
	newRule := ParseRule(newLine)
	if oldRule == nil || newRule == nil {
		return errors.NewPolicyParseFailedError("invalid policy line")
	}

	// 构建旧策略的过滤条件
	filters := RuleToFilters(oldRule)

	// 构建新策略的字段更新映射
	updates := make(map[string]interface{})
	for i, val := range newRule.Values() {
		updates[policy.PolicyFields[i]] = val
	}

	if err := a.repo.UpdateFieldsByFilters(ctx, updates, filters...); err != nil {
		return errors.NewPolicyUpdateFailedError(err.Error())
	}

	a.logger.DebugContextKV(ctx, "Policy updated in database", "old", oldLine, "new", newLine)
	return nil
}

// UpdatePoliciesWithCtx 批量更新策略
// 使用事务保证原子性：所有更新操作要么全部成功，要么全部回滚
func (a *Adapter) UpdatePoliciesWithCtx(ctx context.Context, oldLines, newLines []string) error {
	ctx = contextx.OrBackground(ctx)
	if len(oldLines) != len(newLines) {
		return errors.NewPolicyCountMismatchError("old and new policy counts must match")
	}
	if len(oldLines) == 0 {
		return nil
	}

	oldRules := ParseRules(oldLines)
	newRules := ParseRules(newLines)
	if len(oldRules) != len(oldLines) || len(newRules) != len(newLines) {
		return errors.NewPolicyParseFailedError("invalid policy line")
	}
	a.logger.DebugContextKV(ctx, "Casbin updatePolicies start", "table", a.tableName, "count", len(oldRules))

	err := a.ExecuteInTransaction(ctx, func(txAdapter policy.Adapter) error {
		tx := txAdapter.(*Adapter)
		// 批量删除旧策略（单条 OR 条件 DELETE，复用 BaseRepository.DeleteByFilterGroup）
		orGroup := RulesToOrFilterGroup(oldRules)
		if !orGroup.IsEmpty() {
			if err := tx.repo.DeleteByFilterGroup(ctx, orGroup); err != nil {
				return errors.NewPolicyBatchRemoveFailedError(err.Error())
			}
		}
		// 批量插入新策略（1 次 INSERT batch）
		if err := tx.repo.CreateBatch(ctx, newRules...); err != nil {
			return errors.NewPolicyBatchAddFailedError(err.Error())
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.logger.DebugContextKV(ctx, "Casbin updatePolicies done", "table", a.tableName, "count", len(newRules))
	return nil
}

// UpdateFilteredPoliciesWithCtx 根据字段索引过滤后更新策略
// 使用事务保证原子性：先删除匹配的旧策略，再插入新策略
// 如果任何步骤失败，整个操作回滚
func (a *Adapter) UpdateFilteredPoliciesWithCtx(ctx context.Context, newLines []string, fieldIndex int, fieldValues ...string) error {
	ctx = contextx.OrBackground(ctx)
	if ptype := policy.InferPType(newLines); ptype != "" {
		return a.UpdateFilteredPoliciesByPTypeWithCtx(ctx, ptype, newLines, fieldIndex, fieldValues...)
	}
	a.logger.DebugContextKV(ctx, "Casbin updateFilteredPolicies start", "table", a.tableName, "fieldIndex", fieldIndex, "count", len(newLines))

	err := a.ExecuteInTransaction(ctx, func(txAdapter policy.Adapter) error {
		tx := txAdapter.(*Adapter)

		// 根据字段索引构建过滤条件并删除匹配的策略
		filters := FieldIndexToFilters(fieldIndex, fieldValues...)
		if err := tx.repo.DeleteByFilters(ctx, filters...); err != nil {
			return errors.NewPolicyRemoveFailedError(err.Error())
		}

		// 插入新策略
		rules := ParseRules(newLines)
		if len(rules) > 0 {
			if err := tx.repo.CreateBatch(ctx, rules...); err != nil {
				return errors.NewPolicyBatchAddFailedError(err.Error())
			}
		}

		return nil
	})
	if err != nil {
		return err
	}
	a.logger.DebugContextKV(ctx, "Casbin updateFilteredPolicies done", "table", a.tableName, "fieldIndex", fieldIndex, "count", len(newLines))
	return nil
}

// UpdateFilteredPoliciesByPTypeWithCtx 根据策略类型（p/g）过滤后更新策略
// 使用事务保证原子性：先删除匹配的旧策略，再插入新策略
// 如果任何步骤失败，整个操作回滚
func (a *Adapter) UpdateFilteredPoliciesByPTypeWithCtx(ctx context.Context, ptype string, newLines []string, fieldIndex int, fieldValues ...string) error {
	ctx = contextx.OrBackground(ctx)
	if ptype == "" {
		return a.UpdateFilteredPoliciesWithCtx(ctx, newLines, fieldIndex, fieldValues...)
	}
	a.logger.DebugContextKV(ctx, "Casbin updateFilteredPoliciesByPType start", "table", a.tableName, "ptype", ptype, "fieldIndex", fieldIndex, "count", len(newLines))

	err := a.ExecuteInTransaction(ctx, func(txAdapter policy.Adapter) error {
		tx := txAdapter.(*Adapter)

		filters := FieldIndexToFiltersByPType(ptype, fieldIndex, fieldValues...)
		if err := tx.repo.DeleteByFilters(ctx, filters...); err != nil {
			return errors.NewPolicyRemoveFailedError(err.Error())
		}

		rules := ParseRulesByPType(ptype, newLines)
		if len(rules) > 0 {
			if err := tx.repo.CreateBatch(ctx, rules...); err != nil {
				return errors.NewPolicyBatchAddFailedError(err.Error())
			}
		}

		return nil
	})
	if err != nil {
		return err
	}
	a.logger.DebugContextKV(ctx, "Casbin updateFilteredPoliciesByPType done", "table", a.tableName, "ptype", ptype, "fieldIndex", fieldIndex, "count", len(newLines))
	return nil
}

// LoadFilteredPolicyWithCtx 根据过滤条件加载策略
// 支持 policy.Filter 和 []string 两种过滤格式
func (a *Adapter) LoadFilteredPolicyWithCtx(ctx context.Context, filter interface{}) ([]string, error) {
	a.filtered = true
	ctx = contextx.OrBackground(ctx)

	query := a.buildFilterQuery(filter)
	rules, err := a.repo.List(ctx, query)
	if err != nil {
		return nil, errors.NewPolicyFilterFailedError(err.Error())
	}

	policies := RulesToStrings(rules)
	a.logger.InfoContextKV(ctx, "Filtered policies loaded from database", "count", len(policies))
	return policies, nil
}

// RemoveFilteredPolicyWithCtx 根据字段索引和值删除匹配的策略
func (a *Adapter) RemoveFilteredPolicyWithCtx(ctx context.Context, fieldIndex int, fieldValues ...string) error {
	ctx = contextx.OrBackground(ctx)
	filters := FieldIndexToFilters(fieldIndex, fieldValues...)
	if err := a.repo.DeleteByFilters(ctx, filters...); err != nil {
		return errors.NewPolicyRemoveFailedError(err.Error())
	}

	a.logger.DebugContextKV(ctx, "Filtered policies removed from database", "fieldIndex", fieldIndex)
	return nil
}

// GetPolicyByPTypeWithCtx 根据策略类型（p/g）加载策略
func (a *Adapter) GetPolicyByPTypeWithCtx(ctx context.Context, ptype string) ([]string, error) {
	ctx = contextx.OrBackground(ctx)
	query := repository.NewQuery().AddFilterIfNotEmpty(policy.FieldPType, ptype)
	rules, err := a.repo.List(ctx, query)
	if err != nil {
		return nil, errors.NewPolicyLoadFailedError(err.Error())
	}
	return RulesToStrings(rules), nil
}

// CountWithCtx 统计匹配过滤条件的策略数量
func (a *Adapter) CountWithCtx(ctx context.Context, filter *policy.Filter) (int64, error) {
	ctx = contextx.OrBackground(ctx)
	filters := PolicyFilterToRepoFilters(filter)
	return a.repo.Count(ctx, filters...)
}

// ==================== 非 ctx 方法（包装 WithCtx） ====================
// 非 ctx 方法内部调用对应的 WithCtx 方法
// 处于事务中时（txCtx 已设置）继承事务上下文，使取消信号和 trace-id 透传到子操作
// 否则使用 context.Background() 作为默认上下文

// LoadPolicy 从数据库加载所有策略（无上下文）
func (a *Adapter) LoadPolicy() ([]string, error) {
	return a.LoadPolicyWithCtx(a.defaultCtx())
}

// SavePolicy 保存所有策略到数据库（无上下文）
func (a *Adapter) SavePolicy(policies []string) error {
	return a.SavePolicyWithCtx(a.defaultCtx(), policies)
}

// AddPolicy 添加单条策略（无上下文）
func (a *Adapter) AddPolicy(line string) error {
	return a.AddPolicyWithCtx(a.defaultCtx(), line)
}

// RemovePolicy 删除单条策略（无上下文）
func (a *Adapter) RemovePolicy(line string) error {
	return a.RemovePolicyWithCtx(a.defaultCtx(), line)
}

// AddPolicies 批量添加策略（无上下文）
func (a *Adapter) AddPolicies(lines []string) error {
	return a.AddPoliciesWithCtx(a.defaultCtx(), lines)
}

// RemovePolicies 批量删除策略（无上下文）
func (a *Adapter) RemovePolicies(lines []string) error {
	return a.RemovePoliciesWithCtx(a.defaultCtx(), lines)
}

// UpdatePolicy 更新单条策略（无上下文）
func (a *Adapter) UpdatePolicy(oldLine, newLine string) error {
	return a.UpdatePolicyWithCtx(a.defaultCtx(), oldLine, newLine)
}

// UpdatePolicies 批量更新策略（无上下文）
func (a *Adapter) UpdatePolicies(oldLines, newLines []string) error {
	return a.UpdatePoliciesWithCtx(a.defaultCtx(), oldLines, newLines)
}

// UpdateFilteredPolicies 根据字段索引过滤后更新策略（无上下文）
func (a *Adapter) UpdateFilteredPolicies(newLines []string, fieldIndex int, fieldValues ...string) error {
	return a.UpdateFilteredPoliciesWithCtx(a.defaultCtx(), newLines, fieldIndex, fieldValues...)
}

// UpdateFilteredPoliciesByPType 根据策略类型（p/g）过滤后更新策略（无上下文）
func (a *Adapter) UpdateFilteredPoliciesByPType(ptype string, newLines []string, fieldIndex int, fieldValues ...string) error {
	return a.UpdateFilteredPoliciesByPTypeWithCtx(a.defaultCtx(), ptype, newLines, fieldIndex, fieldValues...)
}

// LoadFilteredPolicy 根据过滤条件加载策略（无上下文）
func (a *Adapter) LoadFilteredPolicy(filter interface{}) ([]string, error) {
	return a.LoadFilteredPolicyWithCtx(a.defaultCtx(), filter)
}

// IsFiltered 返回是否已使用过滤加载
func (a *Adapter) IsFiltered() bool {
	return a.filtered
}

// RemoveFilteredPolicy 根据字段索引和值删除匹配的策略（无上下文）
func (a *Adapter) RemoveFilteredPolicy(fieldIndex int, fieldValues ...string) error {
	return a.RemoveFilteredPolicyWithCtx(a.defaultCtx(), fieldIndex, fieldValues...)
}

// GetPolicyByPType 根据策略类型加载策略（无上下文）
func (a *Adapter) GetPolicyByPType(ptype string) ([]string, error) {
	return a.GetPolicyByPTypeWithCtx(a.defaultCtx(), ptype)
}

// Count 统计匹配过滤条件的策略数量（无上下文）
func (a *Adapter) Count(filter *policy.Filter) (int64, error) {
	return a.CountWithCtx(a.defaultCtx(), filter)
}

// ==================== 辅助方法 ====================

// buildFilterQuery 根据过滤条件构建查询对象
// 支持 policy.Filter 和 []string 两种格式
func (a *Adapter) buildFilterQuery(filter interface{}) *repository.Query {
	switch f := filter.(type) {
	case *policy.Filter:
		return PolicyFilterToQuery(f)
	case []string:
		return PolicyFilterToQuery(policy.FilterFromSlice(f))
	default:
		return repository.NewQuery()
	}
}

// clearAll 清空数据库中所有策略记录
// CasbinRule 不使用软删除，Delete 即硬删，直接清空整表数据
func (a *Adapter) clearAll(ctx context.Context) error {
	gormDB := a.handler.GetDB()
	result := gormDB.Table(a.tableName).Where("1 = 1").Delete(&CasbinRule{})
	if result.Error != nil {
		a.logger.DebugContextKV(ctx, "Casbin clearAll failed", "table", a.tableName, "error", result.Error.Error())
		return result.Error
	}
	a.logger.DebugContextKV(ctx, "Casbin clearAll done", "table", a.tableName, "rows", result.RowsAffected)
	return nil
}

// syncTableIndexes 检测指定表上模型定义的索引与数据库实际索引的差异
// 当显式命名的索引缺失、或列/顺序/唯一性不一致时，自动重建（先删后建）
// 解决 GORM AutoMigrate 不会更新已存在索引的问题，确保旧库索引与模型保持一致
//
// 这是一个独立的包级函数，无需创建完整 Adapter 即可对已存在的分片表执行索引同步，
// 适用于服务冷启动时扫描所有 casbin_rule_* 表批量同步索引的场景
//
// 匹配策略：
//  1. 按索引名匹配：若名字一致且定义一致则跳过；名字一致但定义不一致则 Drop+Create
//  2. 按列匹配：名字不一致但列与唯一性完全一致（如 gorm:"index" 自动命名的索引，
//     其名字依赖动态表名，与 ParseIndexes 基于 model.TableName 生成的名字不同），
//     视为已存在等效索引，跳过避免重复创建
//  3. 完全不存在则 CreateIndex
func syncTableIndexes(ctx context.Context, gormDB *gorm.DB, tableName string, log logger.ILogger) error {
	// 使用 tableScopedMigrator 包装 GORM Migrator，覆盖 DropIndex 使用 table-scoped 语法
	// 解决 CockroachDB 多分表同名索引 DROP INDEX ambiguous 问题
	migrator := &tableScopedMigrator{
		Migrator:  gormDB.Table(tableName).Migrator(),
		gormDB:    gormDB,
		tableName: tableName,
	}

	// 解析模型获取索引定义
	stmt := &gorm.Statement{DB: gormDB}
	_ = stmt.Parse(&CasbinRule{})

	return syncIndexesCore(ctx, migrator, stmt, tableName, log)
}

// tableScopedMigrator 包装 GORM Migrator，覆盖 DropIndex 生成 table-scoped SQL
// GORM PostgreSQL 驱动的 DropIndex 生成 "DROP INDEX name"（不带表名），
// CockroachDB 多分表共用同名索引时报 ambiguous 错误。
// 此 wrapper 按 dialect 直接生成正确的 table-scoped DROP INDEX 语句。
type tableScopedMigrator struct {
	gorm.Migrator
	gormDB    *gorm.DB
	tableName string
}

func (m *tableScopedMigrator) DropIndex(dst interface{}, name string) error {
	// 从模型 schema 解析出实际索引名
	stmt := &gorm.Statement{DB: m.gormDB}
	if stmt.Parse(dst) == nil {
		if idx := stmt.Schema.LookIndex(name); idx != nil {
			name = idx.Name
		}
	}

	dialector := m.gormDB.Dialector.Name()
	var sql string
	switch {
	case constants.IsClickHouseDialector(dialector):
		sql = fmt.Sprintf(`ALTER TABLE "%s" DROP INDEX IF EXISTS "%s"`, m.tableName, name)
	case constants.IsSQLiteDialector(dialector):
		sql = fmt.Sprintf(`DROP INDEX IF EXISTS "%s"`, name)
	case constants.IsMySQLDialector(dialector):
		sql = fmt.Sprintf(`DROP INDEX IF EXISTS "%s" ON "%s"`, name, m.tableName)
	default:
		// CockroachDB: table@index 语法（table-scoped，避免多表同名索引歧义）
		// 标准 PostgreSQL 不允许同名索引，不会走到这里
		sql = fmt.Sprintf(`DROP INDEX IF EXISTS "%s"@"%s"`, m.tableName, name)
	}
	return m.gormDB.Exec(sql).Error
}

// SyncAllShardIndexes 扫描数据库中所有以 tablePrefix 开头的分片表，并对每张表执行索引同步
// 适用于服务冷启动时批量预热所有已存在的分片表索引，避免懒加载导致的索引同步延迟
//
// 该函数是 go-casbin-gorm-adapter 提供的自包含能力，调用方只需传入 DB、表名前缀和日志器即可，
// 无需在业务/基础设施层编写 GetTables、过滤、循环、统计等扫描逻辑
//
// tablePrefix: 分片表名前缀（如 "casbin_rule_"），仅处理以此开头的表
// 单张表同步失败不会中断整体流程，仅记录警告日志；返回的 error 仅表示致命错误（列举表失败或 ctx 取消）
func SyncAllShardIndexes(ctx context.Context, gormDB *gorm.DB, tablePrefix string, log logger.ILogger) error {
	tables, err := gormDB.Migrator().GetTables()
	if err != nil {
		return fmt.Errorf("list tables for shard index sync failed: %w", err)
	}

	var synced, failed, skipped int
	for _, table := range tables {
		// 仅处理匹配前缀的分片表，跳过其他业务表
		if !strings.HasPrefix(table, tablePrefix) {
			skipped++
			continue
		}
		// 响应上下文取消，避免冷启动期间被中断时长时间阻塞
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := syncTableIndexes(ctx, gormDB, table, log); err != nil {
			log.WarnContextKV(ctx, "Sync shard index failed, skipped", "table", table, "error", err.Error())
			failed++
			continue
		}
		synced++
	}
	log.InfoContextKV(ctx, "Shard indexes sync completed", "synced", synced, "failed", failed, "skipped", skipped)
	return nil
}

// indexMigrator 抽象索引迁移所需的三个操作，便于测试注入 mock 覆盖错误分支
type indexMigrator interface {
	GetIndexes(dst interface{}) ([]gorm.Index, error)
	DropIndex(dst interface{}, name string) error
	CreateIndex(dst interface{}, name string) error
}

// syncIndexesCore 执行索引差异检测与重建的核心逻辑
// 从 syncTableIndexes 拆出，接受 indexMigrator 接口以便测试注入 mock 覆盖所有错误分支
func syncIndexesCore(ctx context.Context, migrator indexMigrator, stmt *gorm.Statement, tableName string, log logger.ILogger) error {
	// 获取数据库实际索引
	dbIndexes, err := migrator.GetIndexes(&CasbinRule{})
	if err != nil {
		return fmt.Errorf("get indexes of %s failed: %w", tableName, err)
	}

	for _, modelIndex := range stmt.Schema.ParseIndexes() {
		expectedCols := modelIndexColumns(modelIndex)
		expectedUnique := modelIndex.Class == "UNIQUE"

		// 1) 按名匹配
		var nameMatched gorm.Index
		for _, di := range dbIndexes {
			if di.Name() == modelIndex.Name {
				nameMatched = di
				break
			}
		}
		if nameMatched != nil {
			if indexDefEqual(nameMatched, expectedCols, expectedUnique) {
				continue
			}
			// 名字一致但定义变更：先删后建
			if err := migrator.DropIndex(&CasbinRule{}, modelIndex.Name); err != nil {
				return fmt.Errorf("drop index %s on %s failed: %w", modelIndex.Name, tableName, err)
			}
			if err := migrator.CreateIndex(&CasbinRule{}, modelIndex.Name); err != nil {
				return fmt.Errorf("create index %s on %s failed: %w", modelIndex.Name, tableName, err)
			}
			log.InfoContextKV(ctx, "Casbin index synced", "table", tableName, "name", modelIndex.Name, "reason", "definition changed")
			continue
		}

		// 2) 名不匹配，按列匹配（处理自动命名索引名依赖表名的情况）
		var colMatched gorm.Index
		for _, di := range dbIndexes {
			if indexDefEqual(di, expectedCols, expectedUnique) {
				colMatched = di
				break
			}
		}
		if colMatched != nil {
			// 已存在等效索引（名字不同），跳过避免重复创建
			continue
		}

		// 3) 完全不存在，创建
		if err := migrator.CreateIndex(&CasbinRule{}, modelIndex.Name); err != nil {
			return fmt.Errorf("create index %s on %s failed: %w", modelIndex.Name, tableName, err)
		}
		log.InfoContextKV(ctx, "Casbin index synced", "table", tableName, "name", modelIndex.Name, "reason", "missing")
	}
	return nil
}

// modelIndexColumns 返回模型索引的列名（按 tag 中的 priority 升序排序）
func modelIndexColumns(modelIndex *schema.Index) []string {
	fields := append([]schema.IndexOption(nil), modelIndex.Fields...)
	sort.SliceStable(fields, func(i, j int) bool {
		return fields[i].Priority < fields[j].Priority
	})
	cols := make([]string, len(fields))
	for i, f := range fields {
		cols[i] = f.DBName
	}
	return cols
}

// indexDefEqual 判断数据库索引是否与期望的列顺序和唯一性一致
// 驱动不支持唯一性查询时（ok=false）跳过唯一性对比，仅对比列
func indexDefEqual(dbIdx gorm.Index, expectedCols []string, expectedUnique bool) bool {
	dbCols := dbIdx.Columns()
	if len(dbCols) != len(expectedCols) {
		return false
	}
	for i, c := range expectedCols {
		if c != dbCols[i] {
			return false
		}
	}
	if dbUnique, ok := dbIdx.Unique(); ok && dbUnique != expectedUnique {
		return false
	}
	return true
}

// Close 关闭适配器（GORM 适配器无需特殊关闭操作）
func (a *Adapter) Close() error {
	return nil
}

// GetHandler 获取底层数据库处理器
// 可用于执行自定义 SQL 或事务操作
func (a *Adapter) GetHandler() db.Handler {
	return a.handler
}

// WithTransaction 在事务中执行函数
// 适用于需要原子性操作的场景，如同时更新多条策略
func (a *Adapter) WithTransaction(fn func(tx *gorm.DB) error) error {
	return a.handler.GetDB().Transaction(fn)
}

// ExecuteInTransaction 实现 TransactionalAdapter 接口
// 在数据库事务中执行回调函数，确保多个适配器操作的原子性
// 如果事务中任何操作失败，所有操作都会回滚
func (a *Adapter) ExecuteInTransaction(ctx context.Context, fn func(policy.Adapter) error) error {
	// 快速失败：上下文已取消时不再开启事务，避免 driver: bad connection 等级联错误
	if err := ctx.Err(); err != nil {
		return err
	}
	// 已在事务中（作为 txAdapter 被调用）：直接复用当前事务，不再开 SAVEPOINT
	// 避免嵌套 SAVEPOINT 带来的额外往返和坏连接风险
	if a.inTransaction {
		return fn(a)
	}
	// 使用 WithoutCancel 防止 database/sql 在 ctx 取消时自动回滚事务
	// 原因：database/sql 的 BeginTx 会启动 awaitDone goroutine 监听 ctx，
	// ctx 取消时自动 Rollback，但此时回调可能仍在执行 → "transaction has already been committed or rolled back"
	// WithoutCancel 保留 trace-id 等值（用于日志），但不传播取消信号（由顶部 ctx.Err() 检查负责）
	// 事务仍会在回调返回后由 GORM 正常提交/回滚
	txCtx := context.WithoutCancel(ctx)
	return a.handler.GetDB().WithContext(txCtx).Transaction(func(tx *gorm.DB) error {
		txAdapter, _ := NewAdapter(db.MustNewGormHandler(tx),
			WithTableName(a.tableName),
			WithLogger(a.logger),
			WithAutoMigrate(false),
		)
		// 标记为已在事务中，使后续 ExecuteInTransaction 调用直接复用此事务
		txAdapter.inTransaction = true
		// 传播事务上下文（WithoutCancel），使 txAdapter 的非 ctx 方法也能继承 trace-id
		txAdapter.txCtx = txCtx
		return fn(txAdapter)
	})
}
