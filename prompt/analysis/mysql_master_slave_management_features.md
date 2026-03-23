# MySQL 特性使用分析报告

本项目(mysql-manager)是一个MySQL主从复制生命周期管理服务，主要使用以下MySQL特性：

## 1. 主从复制相关

### 1.1 复制控制命令
| SQL命令 | 用途 | 位置 |
|---------|------|------|
| `START SLAVE` | 启动复制 | mysqlservice.go |
| `STOP SLAVE` | 停止复制 | mysqlservice.go |
| `START SLAVE SQL_THREAD` | 启动SQL线程 | semisync.go |
| `STOP SLAVE SQL_THREAD` | 停止SQL线程 | semisync.go |
| `STOP SLAVE IO_THREAD` | 停止IO线程 | semisync.go |
| `CHANGE MASTER TO` | 配置主从关系 | mysqlservice.go:524 |
| `RESET SLAVE` | 重置复制 | semisync.go:575 |

### 1.2 复制状态查询
| SQL命令 | 用途 |
|---------|------|
| `SHOW SLAVE STATUS FOR CHANNEL 'xxx'` | 查看指定通道的复制状态 |
| `SHOW SLAVE HOSTS` | 查看从库列表 |

### 1.3 GTID复制
| 特性 | 说明 |
|------|------|
| `master_auto_position=1` | 启用GTID自动定位 |
| `SET GLOBAL GTID_PURGED` | 设置已执行GTID集合 |
| GTID模式 | 用于主从复制关系建立和数据同步 |

---

## 2. 半同步复制 (Semi-Sync Replication)

### 2.1 配置参数
| 参数 | 用途 |
|------|------|
| `rpl_semi_sync_master_enabled` | 主库半同步启用 |
| `rpl_semi_sync_slave_enabled` | 从库半同步启用 |
| `rpl_semi_sync_master_status` | 主库半同步状态 |
| `rpl_semi_sync_master_clients` | 半同步从库数量 |

### 2.2 相关SQL
```sql
SET GLOBAL rpl_semi_sync_master_enabled = 1;
SET GLOBAL rpl_semi_sync_slave_enabled = 1;
SHOW GLOBAL STATUS WHERE Variable_name='rpl_semi_sync_master_status' OR Variable_name='rpl_semi_sync_master_clients';
SHOW GLOBAL VARIABLES WHERE Variable_name='rpl_semi_sync_master_enabled';
```

---

## 3. 只读/可写控制

### 3.1 系统变量
| 变量 | 用途 |
|------|------|
| `read_only` | 普通用户只读 |
| `super_read_only` | 超级用户也只读 |

### 3.2 相关SQL
```sql
SET GLOBAL read_only = 0/1;
SET GLOBAL super_read_only = 0/1;
SELECT @@global.read_only;
SELECT @@global.super_read_only;
```

---

## 4. 用户和权限管理

### 4.1 SQL命令
| 命令 | 用途 |
|------|------|
| `CREATE USER` | 创建用户 |
| `GRANT` | 授予权限 |
| `REVOKE` | 撤销权限 |
| `FLUSH PRIVILEGES` | 刷新权限 |
| `DROP USER` | 删除用户 |

### 4.2 权限列表
- `REPLICATION CLIENT`
- `REPLICATION SLAVE`
- `PROCESS`
- `FILE`
- `CREATE USER`
- `RELOAD`
- `SHOW DATABASES`

---

## 5. 备份恢复 (Xtrabackup)

### 5.1 工具
- `/sf/mysql/bin/xtrabackup` - 热备份工具
- `/sf/mysql/bin/mysqldump` - 逻辑备份工具

### 5.2 相关文件
| 文件 | 用途 |
|------|------|
| `xtrabackup_binlog_info` | 记录binlog位置 |
| `xtrabackup_checkpoints` | 备份检查点 |

### 5.3 操作
- `--backup` - 备份
- `--prepare` - 准备恢复
- `--copy-back` - 恢复数据
- 全量备份、增量备份、合并备份

---

## 6. 配置管理

### 6.1 配置文件
- 位置: `/sf/cfg/mysql/my.cnf.d/mysql.cnf`

### 6.2 管理的配置项
| 配置项 | 说明 |
|--------|------|
| `server-id` | 服务器ID |
| `max_connections` | 最大连接数 |
| `read_only` / `super_read_only` | 只读控制 |
| `rpl_semi_sync_master_enabled` | 半同步主库 |
| `rpl_semi_sync_slave_enabled` | 半同步从库 |
| `report_host` | 报告主机名 |

---

## 7. 服务器状态查询

### 7.1 常用查询
```sql
SELECT @@version;           -- 版本
SELECT @@global.server_id;  -- 服务器ID
SELECT @@global.read_only;  -- 只读状态
SELECT @@global.super_read_only; -- 超级只读状态
SHOW PROCESSLIST;           -- 进程列表
```

### 7.2 连接管理
- `max_connections` - 最大连接数配置
- `wait_timeout` - 等待超时 (600秒)

---

## 8. Binlog管理

### 8.1 相关SQL
```sql
SET SESSION sql_log_bin = 0/1;  -- 控制当前会话是否记录binlog
RESET MASTER;                   -- 清除binlog并重新初始化
```

### 8.2 相关参数
- `log_bin` - binlog开关
- `binlog_format` - binlog格式

---

## 9. 其它特性

### 9.1 表空间操作
```sql
ALTER TABLE db.table DISCARD TABLESPACE;
ALTER TABLE db.table IMPORT TABLESPACE;
```

### 9.2 外键控制
```sql
SET SESSION foreign_key_checks = 0/1;
```

### 9.3 连接管理
```sql
KILL CONNECTION @conn_id;  -- 杀掉连接
FLUSH TABLES;              -- 刷新表
```

---

## 10. 项目目录结构对应

| 目录 | 对应的MySQL功能 |
|------|-----------------|
| `driver/mysql/mysqlservice/` | MySQL服务控制、半同步、复制 |
| `driver/mysql/mysqlconfig/` | MySQL配置管理 |
| `driver/mysql/xtrabackup/` | 备份恢复 |
| `driver/mysql/database/` | 数据库操作 |
| `driver/mysqlacl/` | ACL权限管理 |

---

## 总结

本项目主要使用了MySQL的以下核心特性：
1. **GTID + 半同步复制** - 高可用数据同步
2. **读写分离控制** - read_only/super_read_only
3. **Xtrabackup** - 热备份和恢复
4. **用户权限管理** - CREATE USER/GRANT
5. **配置文件管理** - my.cnf动态更新
6. **主从状态监控** - SHOW SLAVE STATUS

项目通过这些特性的组合，实现了MySQL主从集群的自动化管理，包括：
- 主从复制关系建立
- 主备切换(Switchover)
- 故障切换(Failover)
- 备库重建(Rebuild)
- 添加/删除备库
 

### 关键业务逻辑改动

| 业务 | MySQL实现 | PostgreSQL实现 | 改动量 |
|------|-----------|----------------|--------|
| 主备切换 | CHANGE MASTER | 重建流复制 | 大 |
| 备库重建 | xtrabackup | pg_basebackup | 中 |
| 读写状态 | read_only | transaction_read_only | 小 |
| 强同步 | 半同步参数 | synchronous_commit | 中 |
| GTID | auto_position | LSN | 大 |


