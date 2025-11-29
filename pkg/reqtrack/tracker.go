// Package reqtrack provides request tracking for monitoring slow queries
package reqtrack

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Request represents an in-flight request being tracked
type Request struct {
	ID        uint64    `json:"id"`
	SessionID string    `json:"session_id"`
	SQL       string    `json:"sql"`
	StartTime time.Time `json:"start_time"`
	Duration  string    `json:"duration"` // Computed field for JSON output
}

// Tracker tracks in-flight requests
type Tracker struct {
	mu       sync.RWMutex
	requests map[uint64]*Request
	nextID   uint64
}

// Global tracker instance
var globalTracker *Tracker
var once sync.Once

// GetTracker returns the global tracker instance
func GetTracker() *Tracker {
	once.Do(func() {
		globalTracker = &Tracker{
			requests: make(map[uint64]*Request),
		}
	})
	return globalTracker
}

// StartRequest registers a new request and returns its ID
func (t *Tracker) StartRequest(sessionID, sql string) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.nextID++
	id := t.nextID

	t.requests[id] = &Request{
		ID:        id,
		SessionID: sessionID,
		SQL:       truncateSQL(sql, 500), // Limit SQL length for display
		StartTime: time.Now(),
	}

	return id
}

// EndRequest removes a request from tracking
func (t *Tracker) EndRequest(id uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.requests, id)
}

// GetSlowRequests returns requests that have been running longer than the threshold
func (t *Tracker) GetSlowRequests(threshold time.Duration) []*Request {
	t.mu.RLock()
	defer t.mu.RUnlock()

	now := time.Now()
	var slow []*Request

	for _, req := range t.requests {
		duration := now.Sub(req.StartTime)
		if duration >= threshold {
			// Create a copy with computed duration
			r := &Request{
				ID:        req.ID,
				SessionID: req.SessionID,
				SQL:       req.SQL,
				StartTime: req.StartTime,
				Duration:  duration.String(),
			}
			slow = append(slow, r)
		}
	}

	// Sort by duration (oldest first)
	sort.Slice(slow, func(i, j int) bool {
		return slow[i].StartTime.Before(slow[j].StartTime)
	})

	return slow
}

// GetAllRequests returns all in-flight requests
func (t *Tracker) GetAllRequests() []*Request {
	t.mu.RLock()
	defer t.mu.RUnlock()

	now := time.Now()
	var all []*Request

	for _, req := range t.requests {
		r := &Request{
			ID:        req.ID,
			SessionID: req.SessionID,
			SQL:       req.SQL,
			StartTime: req.StartTime,
			Duration:  now.Sub(req.StartTime).String(),
		}
		all = append(all, r)
	}

	// Sort by duration (oldest first)
	sort.Slice(all, func(i, j int) bool {
		return all[i].StartTime.Before(all[j].StartTime)
	})

	return all
}

// GetStats returns tracking statistics
func (t *Tracker) GetStats() map[string]interface{} {
	t.mu.RLock()
	defer t.mu.RUnlock()

	now := time.Now()
	count := len(t.requests)

	var oldest time.Duration
	var slowCount1s, slowCount5s, slowCount30s int

	for _, req := range t.requests {
		duration := now.Sub(req.StartTime)
		if duration > oldest {
			oldest = duration
		}
		if duration >= time.Second {
			slowCount1s++
		}
		if duration >= 5*time.Second {
			slowCount5s++
		}
		if duration >= 30*time.Second {
			slowCount30s++
		}
	}

	return map[string]interface{}{
		"total_in_flight":   count,
		"slow_1s":           slowCount1s,
		"slow_5s":           slowCount5s,
		"slow_30s":          slowCount30s,
		"oldest_duration":   oldest.String(),
		"oldest_duration_s": oldest.Seconds(),
	}
}

// truncateSQL truncates SQL to maxLen characters
func truncateSQL(sql string, maxLen int) string {
	if len(sql) <= maxLen {
		return sql
	}
	return sql[:maxLen] + "..."
}

// SlowRequestsResponse represents the HTTP response for slow requests
type SlowRequestsResponse struct {
	Stats    map[string]interface{} `json:"stats"`
	Requests []*Request             `json:"requests"`
}

// HTTPHandler returns an HTTP handler for the /debug/slow-requests endpoint
func HTTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracker := GetTracker()

		// Parse threshold from query parameter (default: 1s)
		thresholdStr := r.URL.Query().Get("threshold")
		threshold := time.Second
		if thresholdStr != "" {
			if d, err := time.ParseDuration(thresholdStr); err == nil {
				threshold = d
			}
		}

		// Get stats and slow requests
		stats := tracker.GetStats()
		requests := tracker.GetSlowRequests(threshold)

		response := SlowRequestsResponse{
			Stats:    stats,
			Requests: requests,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}
}

// AllRequestsHandler returns an HTTP handler for the /debug/requests endpoint
func AllRequestsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracker := GetTracker()

		response := SlowRequestsResponse{
			Stats:    tracker.GetStats(),
			Requests: tracker.GetAllRequests(),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}
}
