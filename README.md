# go-casbin-gorm-adapter

[go-casbin](https://github.com/kamalyes/go-casbin) 的 GORM 适配器，基于 [go-sqlbuilder](https://github.com/kamalyes/go-sqlbuilder) 实现策略持久化存储，支持 MySQL/PostgreSQL/SQLite

## 安装

```bash
go get github.com/kamalyes/go-casbin-gorm-adapter
```

## 基本使用

```go
package main

import (
    "github.com/kamalyes/go-casbin/enforcer"
    gormadapter "github.com/kamalyes/go-casbin-gorm-adapter"
    "github.com/kamalyes/go-logger"
    "gorm.io/driver/mysql"
    "gorm.io/gorm"
)

func main() {
    log := logger.NewLogger().WithLevel(logger.INFO)

    // 1. 连接数据库
    dsn := "user:pass@tcp(127.0.0.1:3306)/dbname?charset=utf8mb4&parseTime=True&loc=Local"
    db, _ := gorm.Open(mysql.Open(dsn), &gorm.Config{})

    // 2. 创建适配器
    adapter, _ := gormadapter.NewAdapterByDB(db,
        gormadapter.WithTableName("casbin_rule"),
        gormadapter.WithLogger(log),
    )

    // 3. 创建 enforcer（使用 WithAdapter 注入适配器）
    e, _ := enforcer.NewEnforcer(
        enforcer.WithModelPath("resources/rbac_model.conf"),
        enforcer.WithAdapter(adapter),
        enforcer.WithLogger(log),
    )

    // 4. 权限检查
    ok, _ := e.Enforce("alice", "data1", "read")
}
```

## 多租户使用

每个租户使用独立的数据库表，实现策略隔离：

```go
// 租户1：独立表 + RBAC 模型
adapter1, _ := gormadapter.NewAdapterByDB(db,
    gormadapter.WithTableName("casbin_rule_tenant1"),
)
e1, _ := enforcer.NewEnforcer(
    enforcer.WithModelPath("resources/rbac_with_domains_model.conf"),
    enforcer.WithAdapter(adapter1),
    enforcer.WithLogger(log),
)

// 租户2：独立表 + ABAC 规则策略模型
adapter2, _ := gormadapter.NewAdapterByDB(db,
    gormadapter.WithTableName("casbin_rule_tenant2"),
)
e2, _ := enforcer.NewEnforcer(
    enforcer.WithModelPath("resources/abac_rule_model.conf"),
    enforcer.WithAdapter(adapter2),
    enforcer.WithLogger(log),
)
```

## ABAC 规则策略 + ORM 持久化

ABAC 规则策略的条件表达式存储在数据库中，支持动态管理：

```go
adapter, _ := gormadapter.NewAdapterByDB(db)

e, _ := enforcer.NewEnforcer(
    enforcer.WithModelPath("resources/abac_rule_model.conf"),
    enforcer.WithAdapter(adapter),
    enforcer.WithLogger(log),
)

// 添加 ABAC 规则策略（自动持久化到数据库）
_ = e.AddPolicy(`r.sub == "alice"`, "data1", "read")
_ = e.AddPolicy(`r.sub != "eve"`, "data3", "read")

// 权限检查
ok, _ := e.Enforce("alice", "data1", "read")  // true
ok, _ = e.Enforce("eve", "data3", "read")      // false
```

## 配置选项

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `WithTableName(name)` | 设置表名，多租户场景下可设置不同表名实现隔离 | `casbin_rule` |
| `WithLogger(logger)` | 设置日志记录器 | 空日志 |
| `WithAutoMigrate(enabled)` | 是否自动迁移表结构 | `true` |

## 数据表结构

```sql
CREATE TABLE casbin_rule (
    id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    p_type     VARCHAR(128) NOT NULL,
    v0         VARCHAR(256) DEFAULT '',
    v1         VARCHAR(256) DEFAULT '',
    v2         VARCHAR(256) DEFAULT '',
    v3         VARCHAR(256) DEFAULT '',
    v4         VARCHAR(256) DEFAULT '',
    v5         VARCHAR(256) DEFAULT '',
    created_at DATETIME,
    updated_at DATETIME,
    deleted_at DATETIME,
    INDEX idx_p_type (p_type),
    INDEX idx_deleted_at (deleted_at)
);
```

## 支持的接口

- `Adapter` - 基础加载/保存
- `FilteredAdapter` - 过滤加载
- `BatchAdapter` - 批量操作
- `UpdatableAdapter` - 更新操作

## License

Apache-2.0
