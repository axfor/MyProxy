# MySQL-PG Proxy 运维手册

## 目录

1. [部署指南](#部署指南)
2. [监控与告警](#监控与告警)
3. [常见问题排查](#常见问题排查)
4. [性能优化](#性能优化)
5. [故障恢复](#故障恢复)
6. [维护操作](#维护操作)

## 部署指南

### 系统要求

- **硬件**:
  - CPU: 4+ 核心
  - 内存: 8GB+ (基础) + 1MB/连接
  - 磁盘: 10GB+ (用于日志)
  - 网络: 1Gbps+

- **软件**:
  - PostgreSQL 12+
  - Docker 20+ (容器部署)
  - Kubernetes 1.20+ (K8s部署)

### 本地部署

1. **准备配置文件**

```bash
cp configs/config.yaml configs/production.yaml
```

编辑 `configs/production.yaml`:

```yaml
server:
  host: "0.0.0.0"
  port: 3306
  max_connections: 1000

postgres:
  host: "your-postgres-host"
  port: 5432
  database: "your-database"
  user: "proxy_user"
  password: "secure-password"
  max_pool_size: 200
  connection_mode: "session_affinity"

database_mapping:
  default_schema: "public"
  fallback_to_public: false
```

说明:

- `postgres.database` 是固定的 PostgreSQL physical database，不会随着 MySQL 客户端 `USE db` 切换。
- 在 `session_affinity` 模式下，MySQL logical database 会映射到 PostgreSQL schema。
- `database_mapping.fallback_to_public: false` 是默认严格边界，避免未限定对象静默回退到 `public`。
- `SHOW DATABASES` 返回的是 logical database 列表，而不是 PostgreSQL physical database 列表。

2. **构建和运行**

```bash
make build
./bin/aproxy -config configs/production.yaml
```

3. **验证运行**

```bash
# 健康检查
curl http://localhost:9090/health

# 指标检查
curl http://localhost:9090/metrics

# MySQL 连接测试
mysql -h 127.0.0.1 -P 3306 -u your_user -p
```

### Docker 部署

```bash
# 构建镜像
docker build -t aproxy:v1.0.0 -f deployments/docker/Dockerfile .

# 运行容器
docker run -d \
  --name aproxy \
  -p 3306:3306 \
  -p 9090:9090 \
  -v $(pwd)/configs/production.yaml:/app/config.yaml \
  --restart unless-stopped \
  aproxy:v1.0.0
```

### Kubernetes 部署

```bash
# 创建命名空间
kubectl create namespace aproxy

# 创建 Secret (PostgreSQL 凭证)
kubectl create secret generic pg-credentials \
  --from-literal=username=proxy_user \
  --from-literal=password=secure-password \
  -n aproxy

# 部署
kubectl apply -f deployments/kubernetes/deployment.yaml -n aproxy

# 检查状态
kubectl get pods -n aproxy
kubectl get svc -n aproxy
```

## 监控与告警

### 关键指标

#### 连接指标

- `mysql_pg_proxy_active_connections` - 活跃连接数
  - **正常范围**: < max_connections * 0.8
  - **告警阈值**: > max_connections * 0.9

#### 查询指标

- `mysql_pg_proxy_total_queries` - 总查询数
  - **监控**: QPS 趋势

- `mysql_pg_proxy_query_duration_seconds` - 查询延迟
  - **P50**: < 10ms
  - **P95**: < 50ms
  - **P99**: < 100ms
  - **告警**: P99 > 200ms

#### 错误指标

- `mysql_pg_proxy_errors_total{type="connection"}` - 连接错误
  - **告警**: 增长率 > 10/min

- `mysql_pg_proxy_errors_total{type="query"}` - 查询错误
  - **告警**: 错误率 > 1%

#### PostgreSQL 连接池

- `mysql_pg_proxy_pg_pool_size` - PG 连接池大小
  - **告警**: > max_pool_size * 0.9

### Prometheus 告警规则

```yaml
groups:
- name: mysql_pg_proxy
  interval: 30s
  rules:
  # 高连接数告警
  - alert: HighActiveConnections
    expr: mysql_pg_proxy_active_connections > 900
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "高活跃连接数"
      description: "当前连接数 {{ $value }}, 超过阈值"

  # 高延迟告警
  - alert: HighQueryLatency
    expr: histogram_quantile(0.99, rate(mysql_pg_proxy_query_duration_seconds_bucket[5m])) > 0.2
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "查询延迟过高"
      description: "P99 延迟: {{ $value }}s"

  # 错误率告警
  - alert: HighErrorRate
    expr: rate(mysql_pg_proxy_errors_total[5m]) > 10
    for: 2m
    labels:
      severity: critical
    annotations:
      summary: "错误率过高"
      description: "错误率: {{ $value }}/s"

  # PostgreSQL 连接失败
  - alert: PostgreSQLConnectionFailed
    expr: up{job="aproxy"} == 0
    for: 1m
    labels:
      severity: critical
    annotations:
      summary: "无法连接 PostgreSQL"
```

### Grafana Dashboard

导入预设的 Dashboard (JSON 配置见 `deployments/grafana/dashboard.json`)

关键面板:
- 连接数趋势图
- QPS 实时图
- 延迟分位数图 (P50/P95/P99)
- 错误率图
- PostgreSQL 连接池状态

## 常见问题排查

### 问题 1: 连接失败

**症状**: 客户端无法连接到代理

**排查步骤**:

1. 检查代理是否运行
```bash
# Docker
docker ps | grep aproxy

# K8s
kubectl get pods -n aproxy
```

2. 检查端口是否监听
```bash
netstat -tlnp | grep 3306
```

3. 检查日志
```bash
# Docker
docker logs aproxy

# K8s
kubectl logs -f deployment/aproxy -n aproxy
```

4. 测试 PostgreSQL 连接
```bash
psql -h pg-host -U proxy_user -d your_db
```

**解决方案**:
- 检查配置文件中的 PostgreSQL 凭证
- 确认防火墙规则
- 检查网络连通性

### 问题 2: 查询延迟高

**症状**: P99 延迟 > 200ms

**排查步骤**:

1. 检查 PostgreSQL 性能
```sql
-- 查看慢查询
SELECT * FROM pg_stat_statements
ORDER BY mean_exec_time DESC
LIMIT 10;
```

2. 检查连接池状态
```bash
curl http://localhost:9090/metrics | grep pool_size
```

3. 检查系统资源
```bash
top
iostat
vmstat
```

**解决方案**:
- 增加 PostgreSQL 连接池大小
- 优化慢查询
- 增加代理实例数量
- 调整 `connection_mode` 为 `pooled`

### 问题 3: 内存占用高

**症状**: 内存使用持续增长

**排查步骤**:

1. 检查活跃连接数
```bash
curl http://localhost:9090/metrics | grep active_connections
```

2. 检查预编译语句数量
```bash
curl http://localhost:9090/metrics | grep prepared_statements
```

3. 检查内存使用
```bash
# Docker
docker stats aproxy

# K8s
kubectl top pod -n aproxy
```

**解决方案**:
- 降低 `max_connections`
- 定期清理空闲连接
- 增加容器内存限制
- 检查是否有内存泄漏 (通过 pprof)

### 问题 4: SQL 语法错误

**症状**: 特定查询失败,返回语法错误

**排查步骤**:

1. 检查日志中的原始 SQL
```bash
kubectl logs deployment/aproxy | grep "query_error"
```

2. 验证 SQL 重写
```bash
# 测试重写规则
# 检查是否有不支持的 MySQL 语法
```

**解决方案**:
- 添加自定义重写规则
- 禁用 SQL 重写 (`sql_rewrite.enabled: false`)
- 修改应用程序 SQL 为兼容语法

### 问题 5: 连接泄漏

**症状**: PostgreSQL 连接数持续增长

**排查步骤**:

1. 检查 PostgreSQL 活跃连接
```sql
SELECT count(*) FROM pg_stat_activity
WHERE application_name = 'aproxy';
```

2. 检查代理连接池
```bash
curl http://localhost:9090/metrics | grep pg_pool_size
```

3. 检查长事务
```sql
SELECT * FROM pg_stat_activity
WHERE state = 'idle in transaction'
  AND application_name = 'aproxy';
```

**解决方案**:
- 设置连接超时
- 强制回滚长事务
- 重启代理实例
- 检查客户端是否正确关闭连接

## 性能优化

### 配置优化

1. **连接模式选择**

```yaml
# 高并发场景,推荐 pooled
postgres:
  connection_mode: "pooled"
  max_pool_size: 200

# 需要会话隔离,使用 session_affinity
postgres:
  connection_mode: "session_affinity"
  max_pool_size: 1000
```

`session_affinity` 也是 schema mapping / `USE db` 语义的必需模式；`pooled` 与 `hybrid` 不提供同等的 schema 重放保证。

2. **连接池大小**

```yaml
# 公式: max_pool_size = (CPU 核心数 * 2) + 磁盘数量
postgres:
  max_pool_size: 100  # 适用于 4 核 + 1 SSD
```

3. **SQL 重写优化**

```yaml
# 如果应用已经使用兼容语法,可禁用重写提升性能
sql_rewrite:
  enabled: false
```

### 应用层优化

1. **使用连接池** - 客户端使用连接池复用连接
2. **使用预编译语句** - 提升性能,减少解析开销
3. **批量操作** - 使用批量插入/更新
4. **避免长事务** - 尽快提交或回滚事务

### 系统层优化

1. **调整文件描述符限制**

```bash
# /etc/security/limits.conf
* soft nofile 65536
* hard nofile 65536
```

2. **网络优化**

```bash
# /etc/sysctl.conf
net.core.somaxconn = 4096
net.ipv4.tcp_max_syn_backlog = 4096
net.ipv4.ip_local_port_range = 10000 65000
```

## 故障恢复

### 场景 1: 代理进程崩溃

**步骤**:

1. 检查崩溃日志
```bash
journalctl -u aproxy -n 100
```

2. 重启服务
```bash
# Docker
docker restart aproxy

# K8s (自动重启)
kubectl rollout restart deployment/aproxy -n aproxy
```

3. 验证恢复
```bash
mysql -h proxy-host -P 3306 -e "SELECT 1"
```

### 场景 2: PostgreSQL 不可达

**步骤**:

1. 确认 PostgreSQL 状态
```bash
pg_isready -h pg-host -p 5432
```

2. 检查网络连通性
```bash
telnet pg-host 5432
```

3. 代理会自动重连,检查健康状态
```bash
curl http://localhost:9090/health
```

### 场景 3: 高负载导致服务降级

**步骤**:

1. 快速扩容 (Kubernetes)
```bash
kubectl scale deployment aproxy --replicas=10 -n aproxy
```

2. 限流保护
```yaml
security:
  rate_limit_per_second: 500  # 临时降低限流
```

3. 优先处理关键查询,拒绝非关键流量

## 维护操作

### 版本升级

```bash
# 1. 下载新版本
wget https://github.com/.../aproxy-v2.0.0

# 2. 滚动升级 (K8s)
kubectl set image deployment/aproxy \
  aproxy=aproxy:v2.0.0 \
  -n aproxy

# 3. 监控升级过程
kubectl rollout status deployment/aproxy -n aproxy
```

### 配置变更

```bash
# 1. 更新 ConfigMap
kubectl edit configmap aproxy-config -n aproxy

# 2. 重启 Pod 使配置生效
kubectl rollout restart deployment/aproxy -n aproxy
```

### 日志轮转

```yaml
# 使用 logrotate
/var/log/aproxy/*.log {
    daily
    rotate 7
    compress
    delaycompress
    missingok
    notifempty
    create 0644 proxy proxy
}
```

### 备份与恢复

**配置备份**:
```bash
kubectl get configmap aproxy-config -n aproxy -o yaml > config-backup.yaml
```

**恢复**:
```bash
kubectl apply -f config-backup.yaml
```

## 安全最佳实践

1. **使用 TLS 加密**
```yaml
security:
  enable_tls: true
  tls_cert: "/path/to/cert.pem"
  tls_key: "/path/to/key.pem"
```

2. **限制访问来源**
```yaml
security:
  max_connections_per_ip: 10
```

3. **定期审计日志**
```yaml
observability:
  enable_query_log: true
  redact_parameters: true
```

4. **使用专用数据库用户**
```sql
CREATE USER proxy_user WITH PASSWORD 'secure-password';
GRANT CONNECT ON DATABASE mydb TO proxy_user;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO proxy_user;
```

## 联系支持

- GitHub Issues: https://github.com/your-org/aproxy/issues
- 文档: https://docs.your-org.com/aproxy
