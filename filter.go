/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-03-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-03-28 00:00:00
 * @FilePath: \go-casbin-gorm-adapter\filter.go
 * @Description: GORM 适配器过滤器转换 - 将策略过滤条件转换为 go-sqlbuilder 查询条件
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"github.com/kamalyes/go-casbin/policy"
	"github.com/kamalyes/go-sqlbuilder/repository"
)

// PolicyFilterToRepoFilters 将 policy.Filter 转换为 repository.Filter 切片
// 用于 Count 等需要 Filter 切片的方法
func PolicyFilterToRepoFilters(f *policy.Filter) []*repository.Filter {
	if f == nil {
		return nil
	}
	var filters []*repository.Filter
	for field, val := range f.NonEmptyFields() {
		filters = append(filters, repository.NewEqFilter(field, val))
	}
	return filters
}

// PolicyFilterToQuery 将 policy.Filter 转换为 repository.Query
// 用于 List 等需要 Query 对象的方法
func PolicyFilterToQuery(f *policy.Filter) *repository.Query {
	if f == nil {
		return repository.NewQuery()
	}
	query := repository.NewQuery()
	for field, val := range f.NonEmptyFields() {
		query.AddFilterIfNotEmpty(field, val)
	}
	return query
}

// RuleToFilters 从 CasbinRule 创建 repository.Filter 切片
// 将规则的非空字段转为等值过滤条件，用于精确匹配删除和更新
func RuleToFilters(rule *CasbinRule) []*repository.Filter {
	if rule == nil {
		return nil
	}
	var filters []*repository.Filter
	if rule.PType != "" {
		filters = append(filters, repository.NewEqFilter(policy.FieldPType, rule.PType))
	}
	for i, val := range rule.Values() {
		if val != "" {
			filters = append(filters, repository.NewEqFilter(policy.PolicyFields[i], val))
		}
	}
	return filters
}

// FieldIndexToFilters 根据字段索引和值创建 repository.Filter 切片
// fieldIndex 为起始字段索引（0=V0, 1=V1, ...）
// fieldValues 为从该索引开始的字段值
// 例如: FieldIndexToFilters(0, "alice") 会创建 V0=="alice" 的过滤条件
func FieldIndexToFilters(fieldIndex int, fieldValues ...string) []*repository.Filter {
	var filters []*repository.Filter
	for i, val := range fieldValues {
		if val != "" {
			if field := policy.GetFieldByIndex(fieldIndex + i); field != "" {
				filters = append(filters, repository.NewEqFilter(field, val))
			}
		}
	}
	return filters
}

// FieldIndexToFiltersByPType 根据策略类型（p/g）和字段索引和值创建 repository.Filter 切片
func FieldIndexToFiltersByPType(ptype string, fieldIndex int, fieldValues ...string) []*repository.Filter {
	filters := make([]*repository.Filter, 0, len(fieldValues)+1)
	if ptype != "" {
		filters = append(filters, repository.NewEqFilter(policy.FieldPType, ptype))
	}
	filters = append(filters, FieldIndexToFilters(fieldIndex, fieldValues...)...)
	return filters
}

// FieldIndexToQuery 根据字段索引和值创建 repository.Query
// 与 FieldIndexToFilters 类似，但返回 Query 对象
func FieldIndexToQuery(fieldIndex int, fieldValues ...string) *repository.Query {
	query := repository.NewQuery()
	for i, val := range fieldValues {
		if field := policy.GetFieldByIndex(fieldIndex + i); field != "" {
			query.AddFilterIfNotEmpty(field, val)
		}
	}
	return query
}
