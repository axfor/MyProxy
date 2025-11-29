#!/bin/bash

# TPC-C Style Benchmark Script for aproxy
# This script tests aproxy performance with TPC-C-like workload

set -e

# Configuration
MYSQL_HOST="127.0.0.1"
MYSQL_PORT="3306"
MYSQL_USER="root"
MYSQL_DB="test"

PG_HOST="localhost"
PG_PORT="5432"
PG_USER="bast"
PG_DB="test"

# Test parameters
WAREHOUSES=10
THREADS=10
DURATION=60  # seconds
RAMP_TIME=10 # seconds

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "======================================"
echo "  TPC-C Style Benchmark for aproxy"
echo "======================================"
echo ""

# Function to check if aproxy is running
check_aproxy() {
    if ! pgrep -x "aproxy" > /dev/null; then
        echo -e "${RED}Error: aproxy is not running${NC}"
        echo "Please start aproxy first: ./bin/aproxy"
        exit 1
    fi
    echo -e "${GREEN}✓ aproxy is running${NC}"
}

# Function to check if PostgreSQL is accessible
check_postgres() {
    if ! psql -h $PG_HOST -p $PG_PORT -U $PG_USER -d $PG_DB -c "SELECT 1" > /dev/null 2>&1; then
        echo -e "${RED}Error: Cannot connect to PostgreSQL${NC}"
        exit 1
    fi
    echo -e "${GREEN}✓ PostgreSQL is accessible${NC}"
}

# Function to check if MySQL client can connect to aproxy
check_mysql_client() {
    if ! mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB -e "SELECT 1" > /dev/null 2>&1; then
        echo -e "${RED}Error: Cannot connect to aproxy via MySQL protocol${NC}"
        exit 1
    fi
    echo -e "${GREEN}✓ MySQL client can connect to aproxy${NC}"
}

# Function to setup TPC-C schema
setup_schema() {
    echo ""
    echo "Setting up TPC-C-style schema..."

    mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB <<EOF
-- Drop existing tables
DROP TABLE IF EXISTS order_line;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS stock;
DROP TABLE IF EXISTS item;
DROP TABLE IF EXISTS customer;
DROP TABLE IF EXISTS warehouse;

-- Warehouse table
CREATE TABLE warehouse (
    w_id INT AUTO_INCREMENT PRIMARY KEY,
    w_name VARCHAR(20),
    w_street_1 VARCHAR(20),
    w_street_2 VARCHAR(20),
    w_city VARCHAR(20),
    w_state CHAR(2),
    w_zip CHAR(9),
    w_tax DECIMAL(4,4),
    w_ytd DECIMAL(12,2)
);

-- Customer table
CREATE TABLE customer (
    c_id INT AUTO_INCREMENT PRIMARY KEY,
    c_w_id INT NOT NULL,
    c_first VARCHAR(16),
    c_middle CHAR(2),
    c_last VARCHAR(16),
    c_street_1 VARCHAR(20),
    c_street_2 VARCHAR(20),
    c_city VARCHAR(20),
    c_state CHAR(2),
    c_zip CHAR(9),
    c_phone CHAR(16),
    c_since DATETIME,
    c_credit CHAR(2),
    c_credit_lim DECIMAL(12,2),
    c_discount DECIMAL(4,4),
    c_balance DECIMAL(12,2),
    c_ytd_payment DECIMAL(12,2),
    c_payment_cnt INT,
    c_delivery_cnt INT,
    c_data VARCHAR(500),
    INDEX idx_c_w_id (c_w_id)
);

-- Item table
CREATE TABLE item (
    i_id INT AUTO_INCREMENT PRIMARY KEY,
    i_name VARCHAR(24),
    i_price DECIMAL(5,2),
    i_data VARCHAR(50),
    i_im_id INT
);

-- Stock table
CREATE TABLE stock (
    s_id INT AUTO_INCREMENT PRIMARY KEY,
    s_i_id INT NOT NULL,
    s_w_id INT NOT NULL,
    s_quantity INT,
    s_dist_01 CHAR(24),
    s_dist_02 CHAR(24),
    s_dist_03 CHAR(24),
    s_ytd INT,
    s_order_cnt INT,
    s_remote_cnt INT,
    s_data VARCHAR(50),
    INDEX idx_s_w_id (s_w_id),
    INDEX idx_s_i_id (s_i_id)
);

-- Orders table
CREATE TABLE orders (
    o_id INT AUTO_INCREMENT PRIMARY KEY,
    o_w_id INT NOT NULL,
    o_c_id INT NOT NULL,
    o_entry_d DATETIME,
    o_carrier_id INT,
    o_ol_cnt INT,
    o_all_local INT,
    INDEX idx_o_w_id (o_w_id),
    INDEX idx_o_c_id (o_c_id)
);

-- Order Line table
CREATE TABLE order_line (
    ol_id INT AUTO_INCREMENT PRIMARY KEY,
    ol_o_id INT NOT NULL,
    ol_w_id INT NOT NULL,
    ol_number INT NOT NULL,
    ol_i_id INT NOT NULL,
    ol_supply_w_id INT,
    ol_delivery_d DATETIME,
    ol_quantity INT,
    ol_amount DECIMAL(6,2),
    ol_dist_info CHAR(24),
    INDEX idx_ol_o_id (ol_o_id),
    INDEX idx_ol_w_id (ol_w_id)
);
EOF

    echo -e "${GREEN}✓ Schema created${NC}"
}

# Function to load initial data
load_data() {
    echo ""
    echo "Loading initial data (warehouses=$WAREHOUSES)..."

    # Load warehouses
    for ((i=1; i<=WAREHOUSES; i++)); do
        mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB -e "
            INSERT INTO warehouse (w_name, w_street_1, w_city, w_state, w_zip, w_tax, w_ytd)
            VALUES ('WH-$i', 'Street $i', 'City $i', 'ST', '12345', 0.1, 300000.00);
        " > /dev/null
    done
    echo -e "${GREEN}✓ Loaded $WAREHOUSES warehouses${NC}"

    # Load items (1000 items)
    echo "Loading items..."
    for ((i=1; i<=1000; i++)); do
        mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB -e "
            INSERT INTO item (i_name, i_price, i_data, i_im_id)
            VALUES ('Item $i', ROUND(RAND() * 100, 2), 'Item data $i', $i);
        " > /dev/null 2>&1
    done
    echo -e "${GREEN}✓ Loaded 1000 items${NC}"

    # Load customers (100 per warehouse)
    echo "Loading customers..."
    for ((w=1; w<=WAREHOUSES; w++)); do
        for ((c=1; c<=100; c++)); do
            mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB -e "
                INSERT INTO customer (c_w_id, c_first, c_middle, c_last, c_city, c_state, c_zip,
                                      c_phone, c_since, c_credit, c_credit_lim, c_discount,
                                      c_balance, c_ytd_payment, c_payment_cnt, c_delivery_cnt, c_data)
                VALUES ($w, 'First$c', 'OE', 'Last$c', 'City', 'ST', '12345',
                        '1234567890', NOW(), 'GC', 50000.00, 0.1, -10.00, 10.00, 1, 0, 'Customer data');
            " > /dev/null 2>&1
        done
    done
    echo -e "${GREEN}✓ Loaded $((WAREHOUSES * 100)) customers${NC}"

    # Load stock (1000 items per warehouse)
    echo "Loading stock..."
    for ((w=1; w<=WAREHOUSES; w++)); do
        for ((i=1; i<=1000; i++)); do
            mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB -e "
                INSERT INTO stock (s_i_id, s_w_id, s_quantity, s_ytd, s_order_cnt, s_remote_cnt, s_data)
                VALUES ($i, $w, FLOOR(10 + RAND() * 100), 0, 0, 0, 'Stock data');
            " > /dev/null 2>&1
        done
    done
    echo -e "${GREEN}✓ Loaded $((WAREHOUSES * 1000)) stock items${NC}"
}

# Function to run benchmark via aproxy
run_benchmark_aproxy() {
    echo ""
    echo "======================================"
    echo "  Running Benchmark via aproxy"
    echo "======================================"
    echo "Threads: $THREADS"
    echo "Duration: ${DURATION}s"
    echo "Ramp-up time: ${RAMP_TIME}s"
    echo ""

    sysbench /usr/local/share/sysbench/oltp_read_write.lua \
        --mysql-host=$MYSQL_HOST \
        --mysql-port=$MYSQL_PORT \
        --mysql-user=$MYSQL_USER \
        --mysql-db=$MYSQL_DB \
        --tables=6 \
        --table-size=1000 \
        --threads=$THREADS \
        --time=$DURATION \
        --warmup-time=$RAMP_TIME \
        --report-interval=10 \
        run | tee /tmp/aproxy_benchmark.txt

    echo ""
    echo -e "${GREEN}✓ Benchmark via aproxy completed${NC}"
}

# Function to run custom TPC-C workload
run_custom_workload() {
    echo ""
    echo "======================================"
    echo "  Running Custom TPC-C Workload"
    echo "======================================"

    local OUTPUT_FILE="/tmp/tpcc_custom_results.txt"
    local START_TIME=$(date +%s)
    local END_TIME=$((START_TIME + DURATION))
    local TOTAL_TXN=0
    local SUCCESS_TXN=0
    local FAILED_TXN=0

    echo "Starting workload... (Duration: ${DURATION}s)"

    # Run transactions for the specified duration
    while [ $(date +%s) -lt $END_TIME ]; do
        # New Order Transaction (45% of workload)
        if [ $((RANDOM % 100)) -lt 45 ]; then
            W_ID=$((1 + RANDOM % WAREHOUSES))
            C_ID=$((1 + RANDOM % 100))

            if mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB -e "
                INSERT INTO orders (o_w_id, o_c_id, o_entry_d, o_ol_cnt, o_all_local)
                VALUES ($W_ID, $C_ID, NOW(), 5, 1);
            " > /dev/null 2>&1; then
                SUCCESS_TXN=$((SUCCESS_TXN + 1))
            else
                FAILED_TXN=$((FAILED_TXN + 1))
            fi

        # Payment Transaction (43% of workload)
        elif [ $((RANDOM % 100)) -lt 88 ]; then
            C_ID=$((1 + RANDOM % 100))

            if mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB -e "
                UPDATE customer SET c_balance = c_balance - 100.00, c_ytd_payment = c_ytd_payment + 100.00
                WHERE c_id = $C_ID;
            " > /dev/null 2>&1; then
                SUCCESS_TXN=$((SUCCESS_TXN + 1))
            else
                FAILED_TXN=$((FAILED_TXN + 1))
            fi

        # Order Status Transaction (4% of workload)
        elif [ $((RANDOM % 100)) -lt 92 ]; then
            C_ID=$((1 + RANDOM % 100))

            if mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB -e "
                SELECT o.*, c.* FROM orders o JOIN customer c ON o.o_c_id = c.c_id
                WHERE c.c_id = $C_ID ORDER BY o.o_entry_d DESC LIMIT 1;
            " > /dev/null 2>&1; then
                SUCCESS_TXN=$((SUCCESS_TXN + 1))
            else
                FAILED_TXN=$((FAILED_TXN + 1))
            fi

        # Stock Level Transaction (4% of workload)
        else
            W_ID=$((1 + RANDOM % WAREHOUSES))

            if mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB -e "
                SELECT COUNT(*) FROM stock WHERE s_w_id = $W_ID AND s_quantity < 20;
            " > /dev/null 2>&1; then
                SUCCESS_TXN=$((SUCCESS_TXN + 1))
            else
                FAILED_TXN=$((FAILED_TXN + 1))
            fi
        fi

        TOTAL_TXN=$((TOTAL_TXN + 1))

        # Progress report every 1000 transactions
        if [ $((TOTAL_TXN % 1000)) -eq 0 ]; then
            echo "Processed $TOTAL_TXN transactions..."
        fi
    done

    local ACTUAL_DURATION=$(($(date +%s) - START_TIME))
    local TPS=$(echo "scale=2; $SUCCESS_TXN / $ACTUAL_DURATION" | bc)

    echo ""
    echo "======================================"
    echo "  Custom TPC-C Workload Results"
    echo "======================================"
    echo "Total Transactions: $TOTAL_TXN"
    echo "Successful: $SUCCESS_TXN"
    echo "Failed: $FAILED_TXN"
    echo "Duration: ${ACTUAL_DURATION}s"
    echo "TPS (Transactions/sec): $TPS"
    echo ""

    # Save results
    cat > $OUTPUT_FILE <<EOF
Custom TPC-C Workload Results
=============================
Total Transactions: $TOTAL_TXN
Successful: $SUCCESS_TXN
Failed: $FAILED_TXN
Duration: ${ACTUAL_DURATION}s
TPS: $TPS
EOF

    echo -e "${GREEN}✓ Results saved to $OUTPUT_FILE${NC}"
}

# Function to check cache statistics
check_cache_stats() {
    echo ""
    echo "======================================"
    echo "  Schema Cache Statistics"
    echo "======================================"

    # Query cache stats via PostgreSQL (assuming aproxy exposes stats)
    mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB -e "
        SELECT COUNT(*) as table_count FROM warehouse UNION ALL
        SELECT COUNT(*) FROM customer UNION ALL
        SELECT COUNT(*) FROM item UNION ALL
        SELECT COUNT(*) FROM stock UNION ALL
        SELECT COUNT(*) FROM orders UNION ALL
        SELECT COUNT(*) FROM order_line;
    "

    echo ""
    echo "Note: Check aproxy logs for cache hit/miss statistics"
}

# Function to cleanup
cleanup() {
    echo ""
    echo "Cleaning up test data..."

    mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER $MYSQL_DB -e "
        DROP TABLE IF EXISTS order_line;
        DROP TABLE IF EXISTS orders;
        DROP TABLE IF EXISTS stock;
        DROP TABLE IF EXISTS item;
        DROP TABLE IF EXISTS customer;
        DROP TABLE IF EXISTS warehouse;
    " > /dev/null 2>&1

    echo -e "${GREEN}✓ Cleanup completed${NC}"
}

# Main execution
main() {
    echo "Step 1: Checking prerequisites..."
    check_aproxy
    check_postgres
    check_mysql_client

    echo ""
    read -p "Do you want to setup schema and load data? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        setup_schema
        load_data
    fi

    echo ""
    read -p "Run sysbench OLTP benchmark via aproxy? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        run_benchmark_aproxy
    fi

    echo ""
    read -p "Run custom TPC-C workload? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        run_custom_workload
    fi

    check_cache_stats

    echo ""
    read -p "Cleanup test data? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        cleanup
    fi

    echo ""
    echo "======================================"
    echo "  Benchmark Complete!"
    echo "======================================"
    echo ""
    echo "Results saved to:"
    echo "  - /tmp/aproxy_benchmark.txt (sysbench)"
    echo "  - /tmp/tpcc_custom_results.txt (custom workload)"
    echo ""
}

# Run main function
main
