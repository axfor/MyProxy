// Package main provides a test client for MySQL binlog replication via aproxy.
// This test uses go-mysql/canal to connect to aproxy and receive change events
// from PostgreSQL database changes streamed as MySQL binlog protocol.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-mysql-org/go-mysql/canal"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
)

var (
	host     = flag.String("host", "127.0.0.1", "MySQL/aproxy host")
	port     = flag.Int("port", 3306, "MySQL/aproxy port")
	user     = flag.String("user", "root", "MySQL user")
	password = flag.String("password", "", "MySQL password")
	serverID = flag.Uint("server-id", 101, "Slave server ID")
)

// EventHandler handles binlog events from canal
type EventHandler struct {
	canal.DummyEventHandler
	eventCount int
}

func (h *EventHandler) OnRow(e *canal.RowsEvent) error {
	h.eventCount++
	log.Printf("[EVENT #%d] %s on %s.%s", h.eventCount, e.Action, e.Table.Schema, e.Table.Name)

	for i, row := range e.Rows {
		log.Printf("  Row %d: %v", i, row)
	}
	return nil
}

func (h *EventHandler) OnDDL(header *replication.EventHeader, nextPos mysql.Position, queryEvent *replication.QueryEvent) error {
	h.eventCount++
	log.Printf("[DDL #%d] %s", h.eventCount, string(queryEvent.Query))
	return nil
}

func (h *EventHandler) OnRotate(header *replication.EventHeader, rotatEvent *replication.RotateEvent) error {
	log.Printf("[ROTATE] New binlog file: %s, position: %d",
		string(rotatEvent.NextLogName), rotatEvent.Position)
	return nil
}

func (h *EventHandler) String() string {
	return "TestEventHandler"
}

func main() {
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("Starting canal test client...")
	log.Printf("Connecting to %s:%d as server-id %d", *host, *port, *serverID)

	cfg := canal.NewDefaultConfig()
	cfg.Addr = fmt.Sprintf("%s:%d", *host, *port)
	cfg.User = *user
	cfg.Password = *password
	cfg.ServerID = uint32(*serverID)
	cfg.Flavor = "mysql"
	cfg.Dump.ExecutionPath = "" // Disable mysqldump

	// Create canal instance
	c, err := canal.NewCanal(cfg)
	if err != nil {
		log.Fatalf("Failed to create canal: %v", err)
	}

	// Set event handler
	handler := &EventHandler{}
	c.SetEventHandler(handler)

	// Handle shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Printf("Received shutdown signal, closing canal...")
		cancel()
		c.Close()
	}()

	// Start from the beginning
	startPos := mysql.Position{Name: "binlog.000001", Pos: 4}
	log.Printf("Starting replication from position: %s:%d", startPos.Name, startPos.Pos)

	// Run canal in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- c.RunFrom(startPos)
	}()

	// Wait for events or timeout
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Context done, total events received: %d", handler.eventCount)
			return
		case err := <-errChan:
			if err != nil {
				log.Printf("Canal error: %v", err)
			}
			log.Printf("Canal stopped, total events received: %d", handler.eventCount)
			return
		case <-ticker.C:
			log.Printf("Still running... events received so far: %d", handler.eventCount)
		case <-timeout:
			log.Printf("Timeout reached, stopping canal...")
			log.Printf("Total events received: %d", handler.eventCount)
			c.Close()
			return
		}
	}
}
