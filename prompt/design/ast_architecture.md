# 架构升级
 我们的 aproxy 应该是基于抽象语法树AST转换，把 mysql 语法映射到 pg 语法，而不是硬编码规则（如正则等方式）转换，最好的是基于AST，用“SQL 抽象语法树（AST）+ 重写”的方式，而不是字符串替换
 
 ```
      1. 用“SQL 抽象语法树（AST）+ 重写”的方式，而不是字符串替换
      思路是：
      用一个支持多种 SQL 方言的 parser 把 MySQL 语句解析成 AST 在 AST 层面做方言转换（函数名、关键字、语义差异）再用 PostgreSQL 的 SQL generator 把 AST 打印回字符串
 ```

# 问题背景

```
      在 **PostgreSQL** 中没有 `MATCH ... AGAINST`，但有更强大的 **全文检索（Full Text Search, FTS）**。
      你的 MySQL 语句：

      ```sql
      SELECT title 
      FROM test_fulltext
      WHERE MATCH(title, content) AGAINST('MySQL' IN BOOLEAN MODE);
      ```

      在 **PostgreSQL** 中可写成以下等价形式：

      ---

      # ✅ **PostgreSQL 等价写法（使用 to\_tsvector / to\_tsquery）**

      ```sql
      SELECT title
      FROM test_fulltext
      WHERE to_tsvector('simple', title || ' ' || content) @@ to_tsquery('simple', 'MySQL');
      ```

      ---

      # 📌 如果需要 BOOLEAN MODE（布尔模式）的写法

      MySQL BOOLEAN MODE 支持 `+ - *` 等，而 PostgreSQL 的等价方式是 `tsquery`。

      例如搜索 `MySQL`：

      ```sql
      WHERE to_tsvector('simple', title || ' ' || content)
            @@ to_tsquery('simple', 'MySQL');
      ```

      如果是更复杂的布尔逻辑，例如：

      ### MySQL：

      ```sql
      AGAINST('+MySQL -Oracle' IN BOOLEAN MODE)
      ```

      ### PostgreSQL：

      ```sql
      @@ to_tsquery('simple', 'MySQL & !Oracle')
      ```

      ---

      # 📝 推荐创建索引（性能优化）

      ```sql
      CREATE INDEX idx_test_fulltext_fts
      ON test_fulltext USING GIN (to_tsvector('simple', title || ' ' || content));
      ```

```

---

## AST 结构化转换：INDEX/KEY 关键字处理

### 问题背景

在早期实现中，为了移除 MySQL 的 INDEX 定义（PostgreSQL 不支持内联 INDEX），使用了字符串模式匹配的方式：

```go
// ❌ 错误的方式：字符串匹配
func removeIndexClauses(sql string) string {
    // 使用正则表达式或字符串查找 "INDEX" 关键字
    result = regexp.MustCompile(`\bINDEX\b.*?(?:,|\))`).ReplaceAllString(sql, "")
    return result
}
```

**这种方式的致命缺陷：**

1. **无法区分表名、列名和关键字**
   - 假如用户的表名是 `test_indexes`
   - 假如用户的列名是 `indexes` 或 `my_key_field`
   - 字符串匹配会错误地匹配这些名称，即使加了 `\b` 词边界检查

2. **上下文无关**
   - 无法知道 "INDEX" 是出现在约束定义中，还是在标识符中
   - 无法准确定位哪些是需要删除的 INDEX 定义，哪些是合法的列名/表名

### 正确解决方案：AST 结构化转换

**核心原则：**
> "应该使用结构化替换。AST 应该可以解析出 SQL 的每个部分，比如是不是表名、列名、还是约束定义，而不是通过关键字匹配。"

**AST 方式的优势：**

1. **TiDB Parser 将 CREATE TABLE 解析为结构化的 AST**
   ```go
   type CreateTableStmt struct {
       Table       *TableName
       Cols        []*ColumnDef      // 列定义
       Constraints []*Constraint     // 约束定义（包括 INDEX、UNIQUE、PRIMARY KEY 等）
       Options     []*TableOption    // 表选项
   }
   ```

2. **约束类型明确定义**
   ```go
   type Constraint struct {
       Tp   ConstraintType  // 类型：Index, Key, Unique, PrimaryKey, ForeignKey, Check
       Name string          // 约束名称
       Keys []*IndexPartSpecification  // 索引列
   }
   ```

3. **在 AST 层面过滤约束**
   ```go
   // ✅ 正确的方式：AST 级别过滤
   func (v *ASTVisitor) visitCreateTable(node *ast.CreateTableStmt) (ast.Node, bool) {
       filteredConstraints := make([]*ast.Constraint, 0, len(node.Constraints))

       for _, constraint := range node.Constraints {
           // 只移除 INDEX 和 KEY 类型的约束
           // 保留 PRIMARY KEY、UNIQUE、FOREIGN KEY、CHECK 等
           if constraint.Tp != ast.ConstraintIndex && constraint.Tp != ast.ConstraintKey {
               filteredConstraints = append(filteredConstraints, constraint)
           }
       }

       node.Constraints = filteredConstraints
       return node, false
   }
   ```

### 对比示例

#### 场景：表名和列名包含 "index" 关键字

**测试 SQL：**
```sql
CREATE TABLE test_indexes (
    id INT PRIMARY KEY,
    indexes VARCHAR(100),       -- 列名包含 "index"
    my_key_field VARCHAR(50),   -- 列名包含 "key"
    INDEX idx_name (indexes)    -- 真正的 INDEX 定义
)
```

**字符串匹配方式的结果：**
- ❌ 可能错误地修改表名 `test_indexes`
- ❌ 可能错误地修改列名 `indexes`
- ❌ 难以精确定位需要删除的 INDEX 定义

**AST 方式的结果：**
- ✅ 表名 `test_indexes` 保持不变
- ✅ 列名 `indexes` 和 `my_key_field` 保持不变
- ✅ INDEX 约束被准确识别并移除
- ✅ 最终生成的 PostgreSQL SQL：
  ```sql
  CREATE TABLE "test_indexes" (
      "id" INT PRIMARY KEY,
      "indexes" VARCHAR(100),
      "my_key_field" VARCHAR(50)
  )
  ```

### 实现位置

1. **AST 转换**：[`pkg/sqlrewrite/ast_visitor.go:visitCreateTable()`](../pkg/sqlrewrite/ast_visitor.go)
   - 在 AST 层面过滤 INDEX/KEY 约束
   - 保留所有其他约束（PRIMARY KEY、UNIQUE、FOREIGN KEY、CHECK）

2. **后处理修复**：[`pkg/sqlrewrite/pg_generator.go:PostProcess()`](../pkg/sqlrewrite/pg_generator.go)
   - 修复 TiDB Parser 生成的 "UNIQUE KEY" 为 "UNIQUE"（PostgreSQL 语法）
   - 清理 MySQL 字符集前缀（如 `_UTF8MB4'text'` → `'text'`）

### 架构原则总结

| 操作类型                                    | 推荐方式        | 原因                                 |
| ------------------------------------------- | --------------- | ------------------------------------ |
| **语义级转换**<br/>（如移除 INDEX 约束）    | ✅ AST 级别      | 准确识别语义结构，不受标识符命名影响 |
| **语法修正**<br/>（如 UNIQUE KEY → UNIQUE） | ✅ 字符串替换    | 简单语法差异，PostProcess 阶段处理   |
| **类型映射**                                | ✅ AST + Visitor | 在 AST 遍历时转换类型节点            |
| **函数转换**                                | ✅ AST + Visitor | 在表达式节点层面替换函数调用         |

**核心思想：**
- 能在 AST 层面做的，绝不在字符串层面做
- AST 提供结构化上下文，避免误判
- 字符串处理仅用于简单的语法修正和清理

## 设计要求
```

    1、使用 AST 结构化转换，不依赖关键字匹配
    2、准确区分约束定义 vs 标识符（表名/列名）
    3、保留所有非 INDEX 约束（PRIMARY KEY、UNIQUE、FOREIGN KEY 等）
    4、符合项目的 AST 驱动架构设计原则
    5、全部改成AST 结构化转换， 扩展性与准确性都非常好
    6、能在 AST 层面做的，绝不在字符串层面做

```
## 完全采用 AST 结构化转换受益
```
    通过完全采用 AST 结构化转换，我们实现了：
    1、准确性：完全避免列名与类型关键字冲突（如 datetime_field 不会变成 timestamp_field）
    2、扩展性：新增类型转换只需在 AST visitor 中添加新的 case，无需担心字符串匹配的边界情况
    3、可维护性：所有转换逻辑集中在 convertColumnType() 方法中，代码清晰易懂
    4、健壮性：AST 提供结构化上下文，能准确区分表名、列名、类型和约束
    5、...

```
---

## AI思考回答

```
  下一个里程碑是完成 AST 实现的细节：
      修复 API 兼容性 - 调整 TiDB Parser API 使用
      完善类型映射 - 实现完整的 MySQL → PostgreSQL 类型转换
      集成测试 - 确保与现有系统兼容
      性能优化 - 添加缓存和池化
      MATCH AGAINST - 实现全文搜索转换 🎯
  💡 你的洞察非常准确
      "我们的 aproxy 应该是基于抽象语法树转换，把 mysql 语法映射到 pg 语法，而不是硬编码规则转换"
      这正是现代 SQL 代理工具的正确方向！通过这次升级：
          ✅ 明确了问题（正则方案的局限）
          ✅ 设计了方案（AST 转换）
          ✅ 选择了技术（TiDB Parser）
          ✅ 学习了最佳实践（从 TiDB 源码）
          ✅ 实现了原型（Visitor + Generator）
      这为 AProxy 未来支持更复杂的 MySQL 特性（MATCH AGAINST、存储过程、窗口函数）奠定了坚实基础！
```