package admin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// BackupManager wraps PostgreSQL backup/restore tools as equivalents
// for MySQL xtrabackup operations.
type BackupManager struct {
	// PGBaseBackupBin is the path to pg_basebackup binary
	PGBaseBackupBin string
	// PGDumpBin is the path to pg_dump binary
	PGDumpBin string
	// PGRestoreBin is the path to pg_restore binary
	PGRestoreBin string
	// DefaultConnStr is the default PostgreSQL connection string
	DefaultConnStr string
}

// NewBackupManager creates a new backup manager with default paths
func NewBackupManager(connStr string) *BackupManager {
	return &BackupManager{
		PGBaseBackupBin: "pg_basebackup",
		PGDumpBin:       "pg_dump",
		PGRestoreBin:    "pg_restore",
		DefaultConnStr:  connStr,
	}
}

// BackupConfig holds configuration for a backup operation
type BackupConfig struct {
	// ConnStr overrides the default connection string
	ConnStr string
	// TargetDir is the directory to store backup files
	TargetDir string
	// Label is a human-readable backup label
	Label string
	// Format: "plain" (directory), "tar", "custom" for pg_dump
	Format string
	// Checkpoint: "fast" or "spread"
	Checkpoint string
	// WALMethod: "stream" (default), "fetch", "none"
	WALMethod string
	// Compress: enable compression (for pg_basebackup)
	Compress bool
	// Database: specific database for pg_dump (empty = all)
	Database string
}

// BackupResult holds the result of a backup operation
type BackupResult struct {
	StartTime time.Time
	EndTime   time.Time
	TargetDir string
	Size      int64 // approximate size in bytes
	Label     string
	Err       error
}

// PhysicalBackup performs a physical backup equivalent to xtrabackup --backup.
// Uses pg_basebackup to create a base backup of the PostgreSQL cluster.
func (bm *BackupManager) PhysicalBackup(ctx context.Context, cfg BackupConfig) *BackupResult {
	result := &BackupResult{
		StartTime: time.Now(),
		Label:     cfg.Label,
	}

	if cfg.TargetDir == "" {
		cfg.TargetDir = filepath.Join(os.TempDir(), fmt.Sprintf("myproxy_backup_%s", time.Now().Format("20060102_150405")))
	}
	result.TargetDir = cfg.TargetDir

	if cfg.Label == "" {
		cfg.Label = fmt.Sprintf("myproxy_backup_%s", time.Now().Format("20060102_150405"))
	}

	connStr := cfg.ConnStr
	if connStr == "" {
		connStr = bm.DefaultConnStr
	}

	// Build pg_basebackup command
	// Equivalent to: xtrabackup --backup --target-dir=<dir>
	args := []string{
		"-D", cfg.TargetDir,
		"-l", cfg.Label,
		"-Fp", // plain format (directory)
		"-Xs", // stream WAL during backup (default, safest)
		"-P",  // show progress
	}

	if connStr != "" {
		args = append(args, "-d", connStr)
	}

	if cfg.Checkpoint == "fast" {
		args = append(args, "--checkpoint=fast")
	}

	if cfg.Compress {
		args = append(args, "--gzip")
	}

	cmd := exec.CommandContext(ctx, bm.PGBaseBackupBin, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		result.Err = fmt.Errorf("pg_basebackup failed: %w, output: %s", err, string(output))
		result.EndTime = time.Now()
		return result
	}

	// Get backup size
	result.Size = dirSize(cfg.TargetDir)
	result.EndTime = time.Now()
	return result
}

// LogicalBackup performs a logical backup equivalent to mysqldump.
// Uses pg_dump to create a logical dump.
func (bm *BackupManager) LogicalBackup(ctx context.Context, cfg BackupConfig) *BackupResult {
	result := &BackupResult{
		StartTime: time.Now(),
		Label:     cfg.Label,
	}

	if cfg.TargetDir == "" {
		cfg.TargetDir = filepath.Join(os.TempDir(), fmt.Sprintf("myproxy_dump_%s", time.Now().Format("20060102_150405")))
	}
	result.TargetDir = cfg.TargetDir

	if err := os.MkdirAll(cfg.TargetDir, 0755); err != nil {
		result.Err = fmt.Errorf("failed to create target dir: %w", err)
		result.EndTime = time.Now()
		return result
	}

	connStr := cfg.ConnStr
	if connStr == "" {
		connStr = bm.DefaultConnStr
	}

	dumpFile := filepath.Join(cfg.TargetDir, "dump.sql")
	if cfg.Format == "custom" {
		dumpFile = filepath.Join(cfg.TargetDir, "dump.custom")
	}

	args := []string{
		"-f", dumpFile,
	}

	if cfg.Format == "custom" {
		args = append(args, "-Fc")
	}

	if connStr != "" {
		args = append(args, connStr)
	}

	if cfg.Database != "" {
		args = append(args, "-d", cfg.Database)
	}

	cmd := exec.CommandContext(ctx, bm.PGDumpBin, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		result.Err = fmt.Errorf("pg_dump failed: %w, output: %s", err, string(output))
		result.EndTime = time.Now()
		return result
	}

	result.Size = fileSize(dumpFile)
	result.EndTime = time.Now()
	return result
}

// Restore restores a physical backup.
// Equivalent to xtrabackup --copy-back.
// Note: xtrabackup --prepare is not needed for PostgreSQL backups.
//
// IMPORTANT: This operation requires PostgreSQL to be stopped.
// The caller must ensure:
//  1. PostgreSQL is stopped
//  2. The PGDATA directory is empty or backed up
//  3. After restore, PostgreSQL can be started
func (bm *BackupManager) Restore(ctx context.Context, backupDir string, pgDataDir string) error {
	if backupDir == "" {
		return fmt.Errorf("backup directory is required")
	}
	if pgDataDir == "" {
		return fmt.Errorf("PGDATA directory is required")
	}

	// Verify backup directory exists
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		return fmt.Errorf("backup directory does not exist: %s", backupDir)
	}

	// Copy backup to PGDATA (equivalent to xtrabackup --copy-back)
	cmd := exec.CommandContext(ctx, "rsync", "-a", "--delete",
		backupDir+"/",
		pgDataDir+"/",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restore (rsync) failed: %w, output: %s", err, string(output))
	}

	// Create standby.signal if this is for a standby setup
	// (equivalent to configuring MySQL slave after restore)
	signalFile := filepath.Join(pgDataDir, "standby.signal")
	if err := os.WriteFile(signalFile, []byte{}, 0600); err != nil {
		return fmt.Errorf("failed to create standby.signal: %w", err)
	}

	return nil
}

// LogicalRestore restores a logical dump.
// Equivalent to mysql < dump.sql.
func (bm *BackupManager) LogicalRestore(ctx context.Context, dumpFile string, connStr string) error {
	if connStr == "" {
		connStr = bm.DefaultConnStr
	}

	args := []string{
		"-f", dumpFile,
	}
	if connStr != "" {
		args = append(args, "-d", connStr)
	}

	// Detect format by extension
	if filepath.Ext(dumpFile) == ".custom" {
		// Use pg_restore for custom format
		cmd := exec.CommandContext(ctx, bm.PGRestoreBin, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("pg_restore failed: %w, output: %s", err, string(output))
		}
		return nil
	}

	// For plain SQL, use psql
	psqlArgs := []string{"-f", dumpFile}
	if connStr != "" {
		psqlArgs = append(psqlArgs, connStr)
	}
	cmd := exec.CommandContext(ctx, "psql", psqlArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql restore failed: %w, output: %s", err, string(output))
	}

	return nil
}

// dirSize calculates the total size of a directory
func dirSize(path string) int64 {
	var size int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		size += info.Size()
		return nil
	})
	return size
}

// fileSize returns the size of a single file
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
