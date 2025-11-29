# TPC-C 基准测试指南

## 脚本简介

`tpcc_benchmark.sh` 是一个为 aproxy 设计的 TPC-C 风格 OLTP 基准测试脚本，用于评估 aproxy 在典型事务处理场景下的性能表现。

## TPC-C 工作负载特征

TPC-C（Transaction Processing Performance Council - Benchmark C）是业界标准的 OLTP 基准测试，模拟批发商业务场景。该脚本实现了以下核心特征：

### 数据模型（6 张表）

1. **warehouse** (仓库表) - 主键：`w_id`
2. **customer** (客户表) - 主键：`c_id`，外键：`c_w_id`
3. **item** (商品表) - 主键：`i_id`
4. **stock** (库存表) - 主键：`s_id`，外键：`s_w_id`, `s_i_id`
5. **orders** (订单表) - 主键：`o_id`，外键：`o_w_id`, `o_c_id`
6. **order_line** (订单明细表) - 主键：`ol_id`，外键：`ol_o_id`, `ol_w_id`

### 事务类型分布

- **New Order (45%)**: 新建订单，包含 INSERT 操作
- **Payment (43%)**: 付款更新，包含 UPDATE 操作
- **Order Status (4%)**: 订单查询，包含 JOIN 查询
- **Stock Level (4%)**: 库存查询，包含聚合查询
- **Delivery (4%)**: 配送处理

## 测试目标

该脚本主要用于验证以下性能指标：

1. ✅ **Schema Cache 性能**
   - AUTO_INCREMENT 列检测的缓存命中率
   - DDL 自动失效机制
   - 全局缓存共享效果

2. ✅ **SQL 改写性能**
   - INSERT 语句自动添加 RETURNING
   - 类型转换开销
   - 占位符转换（`?` → `$1, $2`）

3. ✅ **并发性能**
   - 多线程并发访问
   - 连接池效率
   - 事务吞吐量

4. ✅ **OLTP 典型场景**
   - 高频 INSERT/UPDATE
   - 多表 JOIN 查询
   - 聚合查询

## 使用方法

### 前置条件

1. **aproxy 已启动**
   ```bash
   cd /Users/bast/code/aproxy
   make build
   ./bin/aproxy &
   ```

2. **PostgreSQL 正在运行**
   ```bash
   psql -h localhost -p 5432 -U bast -d test -c "SELECT 1"
   ```

3. **MySQL 客户端已安装**
   ```bash
   mysql --version
   ```

4. **sysbench 已安装**
   ```bash
   sysbench --version
   ```

### 运行基准测试

#### 一键运行（交互式）
```bash
/tmp/tpcc_benchmark.sh
```

脚本会提示您选择：
- 是否创建 schema 和加载数据
- 是否运行 sysbench OLTP 基准测试
- 是否运行自定义 TPC-C 工作负载
- 是否清理测试数据

#### 非交互式运行（自动化）

如果您想完全自动化运行，可以使用 `yes` 命令：

```bash
yes | /tmp/tpcc_benchmark.sh
```

或者创建一个自动化脚本：

```bash
cat > /tmp/run_tpcc_auto.sh <<'EOF'
#!/bin/bash
cd /Users/bast/code/aproxy

# 1. Build and start aproxy
echo "Building aproxy..."
make build

echo "Starting aproxy..."
killall aproxy 2>/dev/null
sleep 1
./bin/aproxy > /tmp/aproxy_bench.log 2>&1 &
APROXY_PID=$!
sleep 3

# 2. Run benchmark
echo "Running benchmark..."
yes | /tmp/tpcc_benchmark.sh

# 3. Stop aproxy
echo "Stopping aproxy..."
kill $APROXY_PID 2>/dev/null

echo "Benchmark complete! Check /tmp/ for results."
EOF

chmod +x /tmp/run_tpcc_auto.sh
/tmp/run_tpcc_auto.sh
```

### 配置参数

您可以编辑脚本开头的配置参数：

```bash
# Configuration
MYSQL_HOST="127.0.0.1"
MYSQL_PORT="3306"
MYSQL_USER="root"
MYSQL_DB="test"

# Test parameters
WAREHOUSES=10      # 仓库数量（影响数据规模）
THREADS=10         # 并发线程数
DURATION=60        # 测试持续时间（秒）
RAMP_TIME=10       # 预热时间（秒）
```

**推荐配置**：

| 场景 | WAREHOUSES | THREADS | DURATION | 数据规模 |
|------|-----------|---------|----------|---------|
| 快速测试 | 5 | 5 | 30 | ~5MB |
| 标准测试 | 10 | 10 | 60 | ~10MB |
| 压力测试 | 50 | 50 | 300 | ~50MB |
| 高负载 | 100 | 100 | 600 | ~100MB |

## 测试输出

### 1. Sysbench OLTP 基准测试结果

保存在：`/tmp/aproxy_benchmark.txt`

示例输出：
```
SQL statistics:
    queries performed:
        read:                            70000
        write:                           20000
        other:                           10000
        total:                           100000
    transactions:                        5000   (83.33 per sec.)
    queries:                             100000 (1666.67 per sec.)
    ignored errors:                      0      (0.00 per sec.)
    reconnects:                          0      (0.00 per sec.)

Throughput:
    events/s (eps):                      83.3333
    time elapsed:                        60.0015s
    total number of events:              5000

Latency (ms):
         min:                                    5.23
         avg:                                   12.01
         max:                                   89.34
         95th percentile:                       21.11
         sum:                               60048.23
```

### 2. 自定义 TPC-C 工作负载结果

保存在：`/tmp/tpcc_custom_results.txt`

示例输出：
```
Custom TPC-C Workload Results
=============================
Total Transactions: 5234
Successful: 5198
Failed: 36
Duration: 60s
TPS: 86.63
```

### 3. Schema Cache 统计

脚本会显示各表的记录数：

```
====================================
  Schema Cache Statistics
====================================
table_count
-----------
10          (warehouses)
1000        (customers)
1000        (items)
10000       (stock)
2345        (orders)
11725       (order_lines)
```

**重要提示**：Schema Cache 的详细统计（命中率、失效次数等）需要查看 aproxy 日志：

```bash
# 查看 aproxy 日志中的缓存统计
grep -i "cache" /tmp/aproxy_bench.log
```

## 性能分析

### 关键指标

1. **TPS (Transactions Per Second)**: 每秒事务数
   - 目标：> 80 TPS（10 并发）
   - 目标：> 500 TPS（100 并发）

2. **P95 延迟**: 95% 的请求响应时间
   - 目标：< 50ms（标准测试）
   - 目标：< 100ms（高负载）

3. **成功率**: `Success / Total * 100%`
   - 目标：> 99%

### Schema Cache 性能验证

测试 Schema Cache 效果的方法：

#### 测试 1：缓存命中率

```bash
# 运行 2 次相同的基准测试，观察第二次是否更快

# 第一次（冷启动，缓存未命中）
yes | /tmp/tpcc_benchmark.sh 2>&1 | grep "TPS"

# 第二次（缓存已加载，高命中率）
yes | /tmp/tpcc_benchmark.sh 2>&1 | grep "TPS"
```

**预期**：第二次 TPS 应该提高 5-10%

#### 测试 2：DDL 自动失效

```bash
# 1. 运行一次测试（建立缓存）
yes | /tmp/tpcc_benchmark.sh

# 2. 修改表结构
mysql -h 127.0.0.1 -P 3306 -u root test -e "ALTER TABLE orders ADD COLUMN test_col INT;"

# 3. 再次运行测试（验证缓存自动失效）
yes | /tmp/tpcc_benchmark.sh
```

**预期**：ALTER TABLE 后，缓存自动失效，下次查询重新加载，无错误发生

#### 测试 3：全局缓存共享

```bash
# 对比 Session 级别缓存 vs 全局缓存

# 禁用全局缓存（修改 config.yaml）
sed -i '' 's/enabled: true/enabled: false/' configs/config.yaml

# 运行测试（Session 级别缓存）
make build && killall aproxy; sleep 1; ./bin/aproxy &
sleep 3
yes | /tmp/tpcc_benchmark.sh 2>&1 | tee /tmp/session_cache_result.txt

# 启用全局缓存
sed -i '' 's/enabled: false/enabled: true/' configs/config.yaml

# 运行测试（全局缓存）
make build && killall aproxy; sleep 1; ./bin/aproxy &
sleep 3
yes | /tmp/tpcc_benchmark.sh 2>&1 | tee /tmp/global_cache_result.txt

# 对比结果
echo "=== Session Cache TPS ==="
grep "TPS" /tmp/session_cache_result.txt

echo "=== Global Cache TPS ==="
grep "TPS" /tmp/global_cache_result.txt
```

**预期**：全局缓存 TPS 提升 5-15%

## 故障排查

### 问题 1：连接失败

**错误信息**：`Error: Cannot connect to aproxy via MySQL protocol`

**解决方法**：
1. 确认 aproxy 正在运行：`pgrep -x aproxy`
2. 检查端口占用：`lsof -i :3306`
3. 查看 aproxy 日志：`tail -f /tmp/aproxy_bench.log`

### 问题 2：PostgreSQL 连接失败

**错误信息**：`Error: Cannot connect to PostgreSQL`

**解决方法**：
1. 确认 PostgreSQL 正在运行：`pg_isready -h localhost -p 5432`
2. 检查数据库是否存在：`psql -h localhost -p 5432 -U bast -d test -c "SELECT 1"`
3. 检查连接配置：编辑 `configs/config.yaml` 中的 `postgres` 部分

### 问题 3：测试数据冲突

**错误信息**：`Table already exists`

**解决方法**：
```bash
# 手动清理测试数据
mysql -h 127.0.0.1 -P 3306 -u root test <<EOF
DROP TABLE IF EXISTS order_line;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS stock;
DROP TABLE IF EXISTS item;
DROP TABLE IF EXISTS customer;
DROP TABLE IF EXISTS warehouse;
EOF
```

### 问题 4：sysbench 命令未找到

**错误信息**：`sysbench: command not found`

**解决方法**：
```bash
# 跳过 sysbench 部分，只运行自定义 TPC-C 工作负载
# 编辑脚本，注释掉 sysbench 相关代码
```

## 性能优化建议

基于测试结果，可以进行以下优化：

### 1. 如果 TPS 低于预期

- 检查 PostgreSQL 配置（`shared_buffers`, `work_mem`）
- 增加连接池大小（`configs/config.yaml` 中的 `max_pool_size`）
- 检查 CPU 和内存使用率

### 2. 如果延迟高于预期

- 启用 SQL 查询日志：`debug_sql: true`
- 分析慢查询
- 检查网络延迟

### 3. 如果缓存命中率低

- 增加 TTL：`ttl: 10m`
- 检查 DDL 是否频繁执行
- 查看 aproxy 日志中的缓存失效记录

## 下一步

测试完成后，您可以：

1. **对比直连 PostgreSQL 的性能**
   ```bash
   sysbench oltp_read_write \
       --pgsql-host=localhost \
       --pgsql-port=5432 \
       --pgsql-user=bast \
       --pgsql-db=test \
       --threads=10 \
       --time=60 \
       run
   ```

2. **分析性能瓶颈**
   - 使用 `pprof` 分析 aproxy 的 CPU 和内存使用
   - 启用 Prometheus metrics（`:9090/metrics`）

3. **调整配置参数**
   - 修改 `configs/config.yaml`
   - 重新运行基准测试
   - 对比性能差异

## 总结

该 TPC-C 基准测试脚本提供了一个全面的性能评估工具，可以验证 aproxy 在 OLTP 场景下的性能表现，特别是 Schema Cache 的优化效果。通过该测试，您可以量化全局缓存带来的性能提升（目标：99% 查询减少，5-15% TPS 提升）。
