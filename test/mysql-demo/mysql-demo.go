package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"

	_ "github.com/go-sql-driver/mysql"
)

// DBConfig 数据库连接配置
type DBConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
}

// User 用户表结构
type User struct {
	ID    int
	Name  string
	Email string
	Age   int
}

// loadConfig 从 config.json 读取数据库配置
func loadConfig(path string) (*DBConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	var cfg DBConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	return &cfg, nil
}

// connectDB 根据配置连接 MySQL 数据库
func connectDB(cfg *DBConfig) (*sql.DB, error) {
	// DSN 格式: user:password@tcp(host:port)/database?charset=utf8mb4&parseTime=True&loc=Local
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库连接失败: %w", err)
	}

	// 测试连接是否可用
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	fmt.Println("✅ 成功连接到 MySQL 数据库!")
	return db, nil
}

// createTable 创建 users 表（如果不存在）
func createTable(db *sql.DB) error {
	query := `
	CREATE TABLE IF NOT EXISTS users (
		id    INT AUTO_INCREMENT PRIMARY KEY,
		name  VARCHAR(100) NOT NULL,
		email VARCHAR(100) NOT NULL,
		age   INT NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`

	if _, err := db.Exec(query); err != nil {
		return fmt.Errorf("创建表失败: %w", err)
	}

	if _, err := db.Exec("DELETE FROM users"); err != nil {
		return fmt.Errorf("清空 users 表失败: %w", err)
	}

	fmt.Println("✅ users 表已就绪，旧数据已清空")
	return nil
}

// insertUser 插入一条用户记录（Create）
func insertUser(db *sql.DB, name, email string, age int) (int64, error) {
	result, err := db.Exec("INSERT INTO users (name, email, age) VALUES (?, ?, ?)", name, email, age)
	if err != nil {
		return 0, fmt.Errorf("插入数据失败: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("获取插入 ID 失败: %w", err)
	}
	fmt.Printf("✅ 插入成功, ID=%d\n", id)
	return id, nil
}

// queryUserByID 根据 ID 查询单个用户（Read）
func queryUserByID(db *sql.DB, id int) (*User, error) {
	var u User
	err := db.QueryRow("SELECT id, name, email, age FROM users WHERE id = ?", id).
		Scan(&u.ID, &u.Name, &u.Email, &u.Age)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("未找到 ID=%d 的用户", id)
		}
		return nil, fmt.Errorf("查询失败: %w", err)
	}
	return &u, nil
}

// queryAllUsers 查询所有用户（Read）
func queryAllUsers(db *sql.DB) ([]User, error) {
	rows, err := db.Query("SELECT id, name, email, age FROM users")
	if err != nil {
		return nil, fmt.Errorf("查询所有用户失败: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Age); err != nil {
			return nil, fmt.Errorf("扫描行数据失败: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历结果集出错: %w", err)
	}
	return users, nil
}

// updateUser 根据 ID 更新用户信息（Update）
func updateUser(db *sql.DB, id int, name, email string, age int) error {
	result, err := db.Exec("UPDATE users SET name=?, email=?, age=? WHERE id=?", name, email, age, id)
	if err != nil {
		return fmt.Errorf("更新数据失败: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("未找到 ID=%d 的用户，更新无效", id)
	}
	fmt.Printf("✅ 更新成功, 影响行数=%d\n", affected)
	return nil
}

// deleteUser 根据 ID 删除用户（Delete）
func deleteUser(db *sql.DB, id int) error {
	result, err := db.Exec("DELETE FROM users WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("删除数据失败: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("未找到 ID=%d 的用户，删除无效", id)
	}
	fmt.Printf("✅ 删除成功, 影响行数=%d\n", affected)
	return nil
}

func main() {
	// 1. 加载配置
	cfg, err := loadConfig("config.json")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("📋 数据库配置: %s@%s:%d/%s\n", cfg.User, cfg.Host, cfg.Port, cfg.Database)

	// 2. 连接数据库
	db, err := connectDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// 3. 创建表
	if err := createTable(db); err != nil {
		log.Fatal(err)
	}

	// 4. 增 - 插入数据
	fmt.Println("\n--- 插入数据 ---")
	id1, err := insertUser(db, "张三", "zhangsan@example.com", 25)
	if err != nil {
		log.Fatal(err)
	}
	id2, _ := insertUser(db, "李四", "lisi@example.com", 30)

	// 5. 查 - 查询单个用户
	fmt.Println("\n--- 查询单个用户 ---")
	user, err := queryUserByID(db, int(id1))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("用户: ID=%d, 姓名=%s, 邮箱=%s, 年龄=%d\n", user.ID, user.Name, user.Email, user.Age)

	// 6. 查 - 查询所有用户
	fmt.Println("\n--- 查询所有用户 ---")
	users, err := queryAllUsers(db)
	if err != nil {
		log.Fatal(err)
	}
	for _, u := range users {
		fmt.Printf("  ID=%d, 姓名=%s, 邮箱=%s, 年龄=%d\n", u.ID, u.Name, u.Email, u.Age)
	}

	// 7. 改 - 更新数据
	fmt.Println("\n--- 更新数据 ---")
	if err := updateUser(db, int(id1), "张三丰", "zhangsanfeng@example.com", 26); err != nil {
		log.Fatal(err)
	}
	user, _ = queryUserByID(db, int(id1))
	fmt.Printf("更新后: ID=%d, 姓名=%s, 邮箱=%s, 年龄=%d\n", user.ID, user.Name, user.Email, user.Age)

	// 8. 删 - 删除数据
	fmt.Println("\n--- 删除数据 ---")
	if err := deleteUser(db, int(id2)); err != nil {
		log.Fatal(err)
	}

	// 9. 确认删除后的数据
	fmt.Println("\n--- 删除后查询所有用户 ---")
	users, _ = queryAllUsers(db)
	for _, u := range users {
		fmt.Printf("  ID=%d, 姓名=%s, 邮箱=%s, 年龄=%d\n", u.ID, u.Name, u.Email, u.Age)
	}

	fmt.Println("\n🎉 所有 CRUD 操作执行完毕!")
}
