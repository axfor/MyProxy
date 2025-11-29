package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	ActiveConnections prometheus.Gauge
	TotalQueries      prometheus.Counter
	QueryDuration     prometheus.Histogram
	ErrorsTotal       *prometheus.CounterVec
	PGPoolSize        prometheus.Gauge
	BytesIn           prometheus.Counter
	BytesOut          prometheus.Counter
	PreparedStmts     prometheus.Gauge
	TransactionsTotal *prometheus.CounterVec

	// CDC Metrics
	CDCEventsTotal     *prometheus.CounterVec // Events by type (insert, update, delete, truncate)
	CDCReplicationLag  prometheus.Gauge       // Current replication lag in milliseconds
	CDCBackpressureTotal prometheus.Counter   // Total backpressure events
	CDCConnectedClients prometheus.Gauge      // Number of connected binlog dump clients
	CDCLastLSN         prometheus.Gauge       // Last processed LSN position
	CDCReconnectsTotal prometheus.Counter     // Total reconnection attempts
	CDCEventsDropped   prometheus.Counter     // Events dropped due to timeout
}

func NewMetrics() *Metrics {
	return &Metrics{
		ActiveConnections: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "mysql_pg_proxy_active_connections",
			Help: "Number of active MySQL client connections",
		}),
		TotalQueries: promauto.NewCounter(prometheus.CounterOpts{
			Name: "mysql_pg_proxy_total_queries",
			Help: "Total number of queries processed",
		}),
		QueryDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "mysql_pg_proxy_query_duration_seconds",
			Help:    "Query execution duration in seconds",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
		}),
		ErrorsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "mysql_pg_proxy_errors_total",
			Help: "Total number of errors by type",
		}, []string{"type"}),
		PGPoolSize: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "mysql_pg_proxy_pg_pool_size",
			Help: "PostgreSQL connection pool size",
		}),
		BytesIn: promauto.NewCounter(prometheus.CounterOpts{
			Name: "mysql_pg_proxy_bytes_in_total",
			Help: "Total bytes received from MySQL clients",
		}),
		BytesOut: promauto.NewCounter(prometheus.CounterOpts{
			Name: "mysql_pg_proxy_bytes_out_total",
			Help: "Total bytes sent to MySQL clients",
		}),
		PreparedStmts: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "mysql_pg_proxy_prepared_statements",
			Help: "Number of active prepared statements",
		}),
		TransactionsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "mysql_pg_proxy_transactions_total",
			Help: "Total number of transactions by result",
		}, []string{"result"}),

		// CDC Metrics
		CDCEventsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "mysql_pg_proxy_cdc_events_total",
			Help: "Total CDC events by type (insert, update, delete, truncate, begin, commit)",
		}, []string{"type"}),
		CDCReplicationLag: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "mysql_pg_proxy_cdc_replication_lag_ms",
			Help: "Current CDC replication lag in milliseconds",
		}),
		CDCBackpressureTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "mysql_pg_proxy_cdc_backpressure_total",
			Help: "Total number of backpressure events (channel full)",
		}),
		CDCConnectedClients: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "mysql_pg_proxy_cdc_connected_clients",
			Help: "Number of connected binlog dump clients",
		}),
		CDCLastLSN: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "mysql_pg_proxy_cdc_last_lsn",
			Help: "Last processed PostgreSQL LSN position",
		}),
		CDCReconnectsTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "mysql_pg_proxy_cdc_reconnects_total",
			Help: "Total number of PostgreSQL reconnection attempts",
		}),
		CDCEventsDropped: promauto.NewCounter(prometheus.CounterOpts{
			Name: "mysql_pg_proxy_cdc_events_dropped_total",
			Help: "Total events dropped due to backpressure timeout",
		}),
	}
}

func (m *Metrics) IncActiveConnections() {
	m.ActiveConnections.Inc()
}

func (m *Metrics) DecActiveConnections() {
	m.ActiveConnections.Dec()
}

func (m *Metrics) IncTotalQueries() {
	m.TotalQueries.Inc()
}

func (m *Metrics) ObserveQueryDuration(seconds float64) {
	m.QueryDuration.Observe(seconds)
}

func (m *Metrics) IncErrors(errorType string) {
	m.ErrorsTotal.WithLabelValues(errorType).Inc()
}

func (m *Metrics) SetPGPoolSize(size float64) {
	m.PGPoolSize.Set(size)
}

func (m *Metrics) AddBytesIn(bytes float64) {
	m.BytesIn.Add(bytes)
}

func (m *Metrics) AddBytesOut(bytes float64) {
	m.BytesOut.Add(bytes)
}

func (m *Metrics) SetPreparedStmts(count float64) {
	m.PreparedStmts.Set(count)
}

func (m *Metrics) IncTransactions(result string) {
	m.TransactionsTotal.WithLabelValues(result).Inc()
}

// CDC Metrics helper methods

func (m *Metrics) IncCDCEvents(eventType string) {
	m.CDCEventsTotal.WithLabelValues(eventType).Inc()
}

func (m *Metrics) SetCDCReplicationLag(lagMs float64) {
	m.CDCReplicationLag.Set(lagMs)
}

func (m *Metrics) IncCDCBackpressure() {
	m.CDCBackpressureTotal.Inc()
}

func (m *Metrics) SetCDCConnectedClients(count float64) {
	m.CDCConnectedClients.Set(count)
}

func (m *Metrics) IncCDCConnectedClients() {
	m.CDCConnectedClients.Inc()
}

func (m *Metrics) DecCDCConnectedClients() {
	m.CDCConnectedClients.Dec()
}

func (m *Metrics) SetCDCLastLSN(lsn float64) {
	m.CDCLastLSN.Set(lsn)
}

func (m *Metrics) IncCDCReconnects() {
	m.CDCReconnectsTotal.Inc()
}

func (m *Metrics) IncCDCEventsDropped() {
	m.CDCEventsDropped.Inc()
}
