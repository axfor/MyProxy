# PostgreSQL 不支持的 MySQL 特性测试

本目录包含 **PostgreSQL 完全不支持或需要应用层改造** 的 MySQL 特性测试。

## ⚠️ 重要说明

这些测试使用 `t.Skip()` 跳过执行,因为:

1. **PostgreSQL 完全不支持这些特性**
2. **需要应用层代码改造才能迁移**
3. **作为文档保留,说明不兼容的功能**

## 测试文件分类

### 1. `mysql_specific_types_test.go` - MySQL 专有数据类型

| MySQL 类型 | 状态 | PostgreSQL 替代方案 |
|-----------|------|-------------------|
| ENUM | ❌ 不支持 | CREATE TYPE custom_enum AS ENUM (...) |
| SET | ❌ 不支持 | TEXT[] 数组或 bit string |
| YEAR | ✅ 已支持 | SMALLINT (自动转换) |
| UNSIGNED | ✅ 已支持 | 更大类型 (自动转换) |
| TINYINT(1) | ✅ 已支持 | SMALLINT (自动转换) |
| MEDIUMINT | ✅ 已支持 | INTEGER (自动转换) |
| INT(11) 显示宽度 | ✅ 已支持 | INT (自动移除显示宽度) |
| GEOMETRY, POINT | ❌ 不支持 | PostGIS 扩展 |

**测试用例:**
- `TestMySQLSpecific_ENUM` - ENUM 类型
- `TestMySQLSpecific_SET` - SET 类型
- `TestMySQLSpecific_SpatialTypes` - 空间数据类型
- `TestMySQLSpecific_DataTypes_Combined` - 混合类型

### 2. `mysql_specific_syntax_test.go` - MySQL 专有 SQL 语法

| MySQL 语法 | 状态 | PostgreSQL 替代方案 |
|-----------|------|-------------------|
| REPLACE INTO | ⚠️ 语义不同 | INSERT ... ON CONFLICT (不完全等价) |
| VALUES() 函数 | ❌ 不支持 | EXCLUDED 表引用 |
| UPDATE ... LIMIT | ❌ 不支持 | 使用子查询 + LIMIT |
| DELETE ... LIMIT | ❌ 不支持 | 使用子查询 + LIMIT |
| STRAIGHT_JOIN | ❌ 不支持 | 显式 JOIN 顺序或 pg_hint_plan |
| FORCE INDEX | ❌ 不支持 | pg_hint_plan 扩展 |
| USE INDEX | ❌ 不支持 | 查询重写或 pg_hint_plan |
| IGNORE INDEX | ❌ 不支持 | 查询重写 |
| LOCK IN SHARE MODE | ✅ 已支持 | FOR SHARE (自动转换) |
| FOR UPDATE SKIP LOCKED | ✅ 已支持 | 相同语法 (PG 9.5+) |
| INSERT DELAYED | ❌ 已废弃 | 无 (MySQL 5.7+ 也已移除) |
| PARTITION BY 语法 | ⚠️ 语法不同 | 声明式分区 (10+) |

**测试用例:**
- `TestMySQLSpecific_REPLACE_INTO` - REPLACE INTO 语句
- `TestMySQLSpecific_INSERT_VALUES_Function` - VALUES() 函数在 UPDATE 中
- `TestMySQLSpecific_UPDATE_LIMIT` - UPDATE 带 LIMIT
- `TestMySQLSpecific_DELETE_LIMIT` - DELETE 带 LIMIT
- `TestMySQLSpecific_STRAIGHT_JOIN` - 强制 JOIN 顺序
- `TestMySQLSpecific_FORCE_INDEX` - 强制使用索引
- `TestMySQLSpecific_USE_INDEX` - 建议使用索引
- `TestMySQLSpecific_IGNORE_INDEX` - 忽略索引
- `TestMySQLSpecific_INSERT_DELAYED` - 延迟插入
- `TestMySQLSpecific_PARTITION_Syntax` - 分区表语法

### 3. `mysql_specific_functions_test.go` - MySQL 专有函数

| MySQL 函数 | 状态 | PostgreSQL 替代方案 |
|-----------|------|-------------------|
| MATCH() AGAINST() | ✅ 已支持 | to_tsvector() / to_tsquery() (自动转换) |
| FOUND_ROWS() | ❌ 不支持 | COUNT(*) OVER() 或单独查询 |
| GET_LOCK() | ❌ 不支持 | pg_advisory_lock() |
| RELEASE_LOCK() | ❌ 不支持 | pg_advisory_unlock() |
| IS_FREE_LOCK() | ❌ 不支持 | 查询 pg_locks 视图 |
| DATE_FORMAT() | ⚠️ 语法不同 | TO_CHAR() (格式字符串不同) |
| STR_TO_DATE() | ⚠️ 语法不同 | TO_DATE() / TO_TIMESTAMP() |
| TIMESTAMPDIFF() | ❌ 不支持 | EXTRACT(EPOCH FROM ...) |
| GROUP_CONCAT() | ✅ 已支持 | string_agg() (自动转换) |
| ENCRYPT() | ❌ 不支持 | pgcrypto 扩展 |
| PASSWORD() | ❌ 已废弃 | 无 |
| LAST_INSERT_ID() | ✅ 已支持 | lastval() (自动转换) |
| FORMAT() | ⚠️ 语法不同 | TO_CHAR() |
| INET_ATON() | ❌ 不支持 | inet 数据类型 |
| INET_NTOA() | ❌ 不支持 | inet 数据类型 |
| LOAD_FILE() | ❌ 安全风险 | 无 |

**测试用例:**
- `TestMySQLSpecific_FOUND_ROWS` - 查询总行数
- `TestMySQLSpecific_GET_LOCK` - 命名锁
- `TestMySQLSpecific_IS_FREE_LOCK` - 检查锁状态
- `TestMySQLSpecific_DATE_FORMAT` - 日期格式化
- `TestMySQLSpecific_STR_TO_DATE` - 字符串转日期
- `TestMySQLSpecific_TIMESTAMPDIFF` - 时间差计算
- `TestMySQLSpecific_ENCRYPT` - 加密函数
- `TestMySQLSpecific_PASSWORD` - 密码哈希
- `TestMySQLSpecific_FORMAT` - 数字格式化
- `TestMySQLSpecific_INET_ATON` - IP 地址转数字
- `TestMySQLSpecific_INET_NTOA` - 数字转 IP 地址
- `TestMySQLSpecific_LOAD_FILE` - 加载文件内容

## 运行测试

### 运行所有 pg-unsupported 测试 (大部分会被跳过)

```bash
cd test/pg-unsupported
go test -v ./...
```

### 运行特定测试

```bash
# 测试 ENUM 类型
go test -v -run TestMySQLSpecific_ENUM

# 测试 REPLACE INTO
go test -v -run TestMySQLSpecific_REPLACE_INTO

# 测试全文搜索
go test -v -run TestMySQLSpecific_MATCH_AGAINST
```

### 查看所有跳过的测试

```bash
go test -v ./... 2>&1 | grep SKIP
```

## 状态图例

- ❌ **完全不支持**: PostgreSQL 没有对应功能
- ⚠️ **语法不同**: PostgreSQL 有对应功能但语法不同
- ✅ **支持**: PostgreSQL 支持 (这些测试应该在主测试集中)

## 迁移建议

### 应用层改造优先级

#### 🔴 高优先级 (必须改造)

1. **ENUM/SET 类型**
   - 改为 PostgreSQL 自定义 ENUM 类型
   - 或使用 CHECK 约束 + VARCHAR

2. **REPLACE INTO**
   - 改为 `INSERT ... ON CONFLICT ... DO UPDATE`
   - 注意语义差异 (delete+insert vs update)

3. **全文搜索 (MATCH/AGAINST)**
   - 完全重写为 PostgreSQL 全文搜索 API
   - 使用 `to_tsvector()` 和 `to_tsquery()`

#### 🟡 中优先级 (建议改造)

4. **UPDATE/DELETE LIMIT**
   - 改为子查询: `DELETE FROM t WHERE id IN (SELECT id FROM t LIMIT n)`

5. **日期函数 (DATE_FORMAT, STR_TO_DATE)**
   - 使用 PostgreSQL 的 `TO_CHAR()` 和 `TO_DATE()`
   - 更新格式字符串语法

6. **用户变量和锁**
   - `GET_LOCK()` → `pg_advisory_lock()`
   - `@变量` → 临时表或会话变量

#### 🟢 低优先级 (可选改造)

7. **类型映射**
   - `TINYINT(1)` → `BOOLEAN`
   - `MEDIUMINT` → `INT`
   - `YEAR` → `SMALLINT`

8. **索引提示**
   - 移除 `FORCE INDEX` / `USE INDEX`
   - 让 PostgreSQL 查询优化器自动选择

## 测试覆盖目标

- ✅ 文档所有不兼容特性
- ✅ 为每个特性提供 PostgreSQL 替代方案
- ✅ 作为迁移参考指南
- ❌ 不要求这些测试通过 (它们应该被跳过)

## 相关文档

- [PG_UNSUPPORTED_FEATURES.md](../../../docs/PG_UNSUPPORTED_FEATURES.md) - 完整的不兼容特性清单
- [TEST_ORGANIZATION.md](../../../docs/TEST_ORGANIZATION.md) - 测试组织策略
- [MYSQL_TO_PG_CASES.md](../../../docs/MYSQL_TO_PG_CASES.md) - SQL 转换案例

## 贡献指南

添加新的不兼容特性测试时:

1. **确认 PostgreSQL 确实不支持**
2. **使用 `t.Skip()` 跳过测试**
3. **在 Skip 消息中说明替代方案**
4. **添加详细的注释**
5. **更新本 README 文件**

### 测试模板

```go
// TestMySQLSpecific_FeatureName tests <feature description>
// PG Alternative: <PostgreSQL alternative>
func TestMySQLSpecific_FeatureName(t *testing.T) {
	t.Skip("<feature> not supported by PostgreSQL - <alternative solution>")

	// Test code here (for documentation purposes)
	// This code won't run due to t.Skip()
}
```

## 更新记录

- **2025-11-07**: 初始版本
  - 创建 pg-unsupported 测试目录
  - 添加 MySQL 专有类型、语法、函数测试
  - 所有测试使用 t.Skip() 标记为跳过
