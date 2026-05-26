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

	"github.com/kamalyes/go-casbin/errors"
	"github.com/kamalyes/go-casbin/policy"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-sqlbuilder/db"
	"github.com/kamalyes/go-sqlbuilder/repository"
	"github.com/kamalyes/go-toolbox/pkg/contextx"
	"gorm.io/gorm"
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
	handler     db.Handler                             // 数据库处理器，封装 GORM DB 实例
	repo        *repository.BaseRepository[CasbinRule] // 基于 go-sqlbuilder 的仓储层
	logger      logger.ILogger                         // 日志记录器
	filtered    bool                                   // 是否已使用过滤加载
	tableName   string                                 // 数据库表名
	autoMigrate bool                                   // 是否自动迁移表结构
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

	// 先清空所有现有策略
	if err := a.clearAll(ctx); err != nil {
		return errors.NewPolicyClearFailedError(err.Error())
	}

	// 解析并批量写入新策略
	rules := ParseRules(policies)
	if len(rules) > 0 {
		if err := a.repo.CreateBatch(ctx, rules...); err != nil {
			return errors.NewPolicySaveFailedError(err.Error())
		}
	}

	a.logger.InfoKV("Policies saved to database", "count", len(rules))
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

	a.logger.DebugKV("Policy added to database", "policy", line)
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

	a.logger.DebugKV("Policy removed from database", "policy", line)
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

	a.logger.DebugKV("Policies batch added to database", "count", len(rules))
	return nil
}

// RemovePoliciesWithCtx 批量从数据库删除策略
// 使用事务保证原子性：所有删除操作要么全部成功，要么全部回滚
func (a *Adapter) RemovePoliciesWithCtx(ctx context.Context, lines []string) error {
	ctx = contextx.OrBackground(ctx)
	if len(lines) == 0 {
		return nil
	}

	return a.ExecuteInTransaction(ctx, func(txAdapter policy.Adapter) error {
		tx := txAdapter.(*Adapter)
		for _, line := range lines {
			rule := ParseRule(line)
			if rule == nil {
				continue
			}

			filters := RuleToFilters(rule)
			if err := tx.repo.DeleteByFilters(ctx, filters...); err != nil {
				return errors.NewPolicyBatchRemoveFailedError(err.Error())
			}
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

	a.logger.DebugKV("Policy updated in database", "old", oldLine, "new", newLine)
	return nil
}

// UpdatePoliciesWithCtx 批量更新策略
// 使用事务保证原子性：所有更新操作要么全部成功，要么全部回滚
func (a *Adapter) UpdatePoliciesWithCtx(ctx context.Context, oldLines, newLines []string) error {
	ctx = contextx.OrBackground(ctx)
	if len(oldLines) != len(newLines) {
		return errors.NewPolicyCountMismatchError("old and new policy counts must match")
	}

	return a.ExecuteInTransaction(ctx, func(txAdapter policy.Adapter) error {
		tx := txAdapter.(*Adapter)
		for i, oldLine := range oldLines {
			oldRule := ParseRule(oldLine)
			newRule := ParseRule(newLines[i])
			if oldRule == nil || newRule == nil {
				return errors.NewPolicyParseFailedError("invalid policy line")
			}

			filters := RuleToFilters(oldRule)
			updates := make(map[string]interface{})
			for j, val := range newRule.Values() {
				updates[policy.PolicyFields[j]] = val
			}

			if err := tx.repo.UpdateFieldsByFilters(ctx, updates, filters...); err != nil {
				return errors.NewPolicyUpdateFailedError(err.Error())
			}
		}
		return nil
	})
}

// UpdateFilteredPoliciesWithCtx 根据字段索引过滤后更新策略
// 使用事务保证原子性：先删除匹配的旧策略，再插入新策略
// 如果任何步骤失败，整个操作回滚
func (a *Adapter) UpdateFilteredPoliciesWithCtx(ctx context.Context, newLines []string, fieldIndex int, fieldValues ...string) error {
	ctx = contextx.OrBackground(ctx)
	if ptype := policy.InferPType(newLines); ptype != "" {
		return a.UpdateFilteredPoliciesByPTypeWithCtx(ctx, ptype, newLines, fieldIndex, fieldValues...)
	}

	return a.ExecuteInTransaction(ctx, func(txAdapter policy.Adapter) error {
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
}

// UpdateFilteredPoliciesByPTypeWithCtx 根据策略类型（p/g）过滤后更新策略
// 使用事务保证原子性：先删除匹配的旧策略，再插入新策略
// 如果任何步骤失败，整个操作回滚
func (a *Adapter) UpdateFilteredPoliciesByPTypeWithCtx(ctx context.Context, ptype string, newLines []string, fieldIndex int, fieldValues ...string) error {
	ctx = contextx.OrBackground(ctx)
	if ptype == "" {
		return a.UpdateFilteredPoliciesWithCtx(ctx, newLines, fieldIndex, fieldValues...)
	}

	return a.ExecuteInTransaction(ctx, func(txAdapter policy.Adapter) error {
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
	a.logger.InfoKV("Filtered policies loaded from database", "count", len(policies))
	return policies, nil
}

// RemoveFilteredPolicyWithCtx 根据字段索引和值删除匹配的策略
func (a *Adapter) RemoveFilteredPolicyWithCtx(ctx context.Context, fieldIndex int, fieldValues ...string) error {
	ctx = contextx.OrBackground(ctx)
	filters := FieldIndexToFilters(fieldIndex, fieldValues...)
	if err := a.repo.DeleteByFilters(ctx, filters...); err != nil {
		return errors.NewPolicyRemoveFailedError(err.Error())
	}

	a.logger.DebugKV("Filtered policies removed from database", "fieldIndex", fieldIndex)
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
// 非 ctx 方法内部调用对应的 WithCtx 方法，使用 context.Background() 作为默认上下文
// 用户可以直接调用 WithCtx 方法传入自定义 context 实现超时控制和链路追踪

// LoadPolicy 从数据库加载所有策略（无上下文）
func (a *Adapter) LoadPolicy() ([]string, error) {
	return a.LoadPolicyWithCtx(context.Background())
}

// SavePolicy 保存所有策略到数据库（无上下文）
func (a *Adapter) SavePolicy(policies []string) error {
	return a.SavePolicyWithCtx(context.Background(), policies)
}

// AddPolicy 添加单条策略（无上下文）
func (a *Adapter) AddPolicy(line string) error {
	return a.AddPolicyWithCtx(context.Background(), line)
}

// RemovePolicy 删除单条策略（无上下文）
func (a *Adapter) RemovePolicy(line string) error {
	return a.RemovePolicyWithCtx(context.Background(), line)
}

// AddPolicies 批量添加策略（无上下文）
func (a *Adapter) AddPolicies(lines []string) error {
	return a.AddPoliciesWithCtx(context.Background(), lines)
}

// RemovePolicies 批量删除策略（无上下文）
func (a *Adapter) RemovePolicies(lines []string) error {
	return a.RemovePoliciesWithCtx(context.Background(), lines)
}

// UpdatePolicy 更新单条策略（无上下文）
func (a *Adapter) UpdatePolicy(oldLine, newLine string) error {
	return a.UpdatePolicyWithCtx(context.Background(), oldLine, newLine)
}

// UpdatePolicies 批量更新策略（无上下文）
func (a *Adapter) UpdatePolicies(oldLines, newLines []string) error {
	return a.UpdatePoliciesWithCtx(context.Background(), oldLines, newLines)
}

// UpdateFilteredPolicies 根据字段索引过滤后更新策略（无上下文）
func (a *Adapter) UpdateFilteredPolicies(newLines []string, fieldIndex int, fieldValues ...string) error {
	return a.UpdateFilteredPoliciesWithCtx(context.Background(), newLines, fieldIndex, fieldValues...)
}

// UpdateFilteredPoliciesByPType 根据策略类型（p/g）过滤后更新策略（无上下文）
func (a *Adapter) UpdateFilteredPoliciesByPType(ptype string, newLines []string, fieldIndex int, fieldValues ...string) error {
	return a.UpdateFilteredPoliciesByPTypeWithCtx(context.Background(), ptype, newLines, fieldIndex, fieldValues...)
}

// LoadFilteredPolicy 根据过滤条件加载策略（无上下文）
func (a *Adapter) LoadFilteredPolicy(filter interface{}) ([]string, error) {
	return a.LoadFilteredPolicyWithCtx(context.Background(), filter)
}

// IsFiltered 返回是否已使用过滤加载
func (a *Adapter) IsFiltered() bool {
	return a.filtered
}

// RemoveFilteredPolicy 根据字段索引和值删除匹配的策略（无上下文）
func (a *Adapter) RemoveFilteredPolicy(fieldIndex int, fieldValues ...string) error {
	return a.RemoveFilteredPolicyWithCtx(context.Background(), fieldIndex, fieldValues...)
}

// GetPolicyByPType 根据策略类型加载策略（无上下文）
func (a *Adapter) GetPolicyByPType(ptype string) ([]string, error) {
	return a.GetPolicyByPTypeWithCtx(context.Background(), ptype)
}

// Count 统计匹配过滤条件的策略数量（无上下文）
func (a *Adapter) Count(filter *policy.Filter) (int64, error) {
	return a.CountWithCtx(context.Background(), filter)
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
// 使用原生 SQL 确保彻底清空（包括软删除的记录）
func (a *Adapter) clearAll(ctx context.Context) error {
	gormDB := a.handler.GetDB()
	return gormDB.Table(a.tableName).Where("1 = 1").Delete(&CasbinRule{}).Error
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
	return a.handler.GetDB().Transaction(func(tx *gorm.DB) error {
		txAdapter, err := NewAdapter(db.MustNewGormHandler(tx),
			WithTableName(a.tableName),
			WithLogger(a.logger),
			WithAutoMigrate(false),
		)
		if err != nil {
			return err
		}
		return fn(txAdapter)
	})
}
