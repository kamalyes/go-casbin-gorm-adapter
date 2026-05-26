/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-03-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-03-28 00:00:00
 * @FilePath: \go-casbin-gorm-adapter\model.go
 * @Description: Casbin 规则数据模型 - 定义数据库表结构与策略字符串转换
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

// CasbinRule 策略规则数据模型
// 对应数据库中的 casbin_rule 表
// PType 为策略类型（p=权限策略, g=角色分组），V0-V5 为策略参数
type CasbinRule struct {
	ID        uint           `json:"id" gorm:"primaryKey;autoIncrement"`             // 主键ID
	PType     string         `json:"p_type" gorm:"size:128;index;comment:策略类型(p/g)"` // 策略类型
	V0        string         `json:"v0" gorm:"size:256;comment:第1个参数"`               // 策略参数0（通常为 sub）
	V1        string         `json:"v1" gorm:"size:256;comment:第2个参数"`               // 策略参数1（通常为 obj）
	V2        string         `json:"v2" gorm:"size:256;comment:第3个参数"`               // 策略参数2（通常为 act）
	V3        string         `json:"v3" gorm:"size:256;comment:第4个参数"`               // 策略参数3（通常为 eft 或 dom）
	V4        string         `json:"v4" gorm:"size:256;comment:第5个参数"`               // 策略参数4
	V5        string         `json:"v5" gorm:"size:256;comment:第6个参数"`               // 策略参数5
	CreatedAt time.Time      `json:"created_at" gorm:"autoCreateTime"`               // 创建时间
	UpdatedAt time.Time      `json:"updated_at" gorm:"autoUpdateTime"`               // 更新时间
	DeletedAt gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`              // 软删除时间
}

// TableName 返回数据库表名
func (CasbinRule) TableName() string {
	return "casbin_rule"
}

// TableComment 指定表注释
func (CasbinRule) TableComment() string {
	return "Casbin策略规则表"
}

// ToString 将规则转换为策略字符串
// 格式: "p, alice, data1, read" 或 "g, alice, admin"
// 自动跳过尾部空字段
func (r *CasbinRule) ToString() string {
	var tokens []string
	tokens = append(tokens, r.PType)

	fields := r.Values()
	// 找到最后一个非空字段的索引
	lastNonEmpty := -1
	for i, f := range fields {
		if f != "" {
			lastNonEmpty = i
		}
	}

	// 只拼接到最后一个非空字段
	for i := 0; i <= lastNonEmpty; i++ {
		tokens = append(tokens, fields[i])
	}

	if len(tokens) <= 1 {
		return ""
	}

	return strings.Join(tokens, ", ")
}

// Values 返回 V0-V5 字段值列表
func (r *CasbinRule) Values() []string {
	return []string{r.V0, r.V1, r.V2, r.V3, r.V4, r.V5}
}

// ValuePtrs 返回字段指针列表（用于赋值）
// 在解析策略字符串时，通过指针直接修改字段值
func (r *CasbinRule) ValuePtrs() []*string {
	return []*string{&r.V0, &r.V1, &r.V2, &r.V3, &r.V4, &r.V5}
}

// FromString 从策略字符串解析规则
// 输入格式: "p, alice, data1, read"
// 返回是否解析成功
func (r *CasbinRule) FromString(line string) bool {
	if line == "" {
		return false
	}

	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	if len(parts) == 0 {
		return false
	}

	// 第一个部分是策略类型
	r.PType = parts[0]

	// 后续部分依次填入 V0-V5
	ptrs := r.ValuePtrs()
	for i := 1; i < len(parts) && i-1 < len(ptrs); i++ {
		*ptrs[i-1] = parts[i]
	}

	return true
}

// ParseRule 从策略字符串创建新规则
// 如果解析失败返回 nil
func ParseRule(line string) *CasbinRule {
	rule := &CasbinRule{}
	if rule.FromString(line) {
		return rule
	}
	return nil
}

// ParseRules 批量解析策略字符串
// 返回指针切片，用于 BaseRepository 的 CreateBatch 方法
func ParseRules(lines []string) []*CasbinRule {
	rules := make([]*CasbinRule, 0, len(lines))
	for _, line := range lines {
		if rule := ParseRule(line); rule != nil {
			rules = append(rules, rule)
		}
	}
	return rules
}

// ParseRulesByPType 根据策略类型解析规则
func ParseRulesByPType(ptype string, lines []string) []*CasbinRule {
	rules := make([]*CasbinRule, 0, len(lines))
	for _, line := range lines {
		rule := ParseRule(line)
		if rule != nil && rule.PType == ptype {
			rules = append(rules, rule)
		}
	}
	return rules
}

// RulesToStrings 批量转换规则为字符串
// 支持指针切片，用于适配器加载策略时将数据库记录转为策略字符串
func RulesToStrings(rules []*CasbinRule) []string {
	policies := make([]string, 0, len(rules))
	for _, rule := range rules {
		if rule != nil {
			if line := rule.ToString(); line != "" {
				policies = append(policies, line)
			}
		}
	}
	return policies
}
