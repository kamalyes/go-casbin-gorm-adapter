/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-03-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-03-28 00:00:00
 * @FilePath: \go-casbin-gorm-adapter\options.go
 * @Description: GORM 适配器配置选项 - 支持函数式选项模式
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package gormadapter

import "github.com/kamalyes/go-logger"

// DefaultTableName 默认数据库表名
const (
	DefaultTableName = "casbin_rule"
)

// Option 适配器配置选项函数
// 使用函数式选项模式，支持链式调用
type Option func(*Adapter)

// WithTableName 设置自定义表名
// 默认为 "casbin_rule"，多应用共享数据库时可设置不同表名
func WithTableName(name string) Option {
	return func(a *Adapter) {
		if name != "" {
			a.tableName = name
		}
	}
}

// WithLogger 设置日志记录器
// 默认使用空日志（不输出），生产环境建议传入 go-logger 实例
func WithLogger(l logger.ILogger) Option {
	return func(a *Adapter) {
		if l != nil {
			a.logger = l
		}
	}
}

// WithAutoMigrate 设置是否自动迁移表结构
// 默认为 true，首次运行时自动创建表
// 生产环境中如果表已存在，可设为 false 避免不必要的迁移检查
func WithAutoMigrate(enabled bool) Option {
	return func(a *Adapter) {
		a.autoMigrate = enabled
	}
}
