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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
