#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

PID_FILE="$SCRIPT_DIR/.pid"
LOG_DIR="$SCRIPT_DIR/logs"
mkdir -p "$LOG_DIR"

# Check if server is already running via PID file
if [ -f "$PID_FILE" ]; then
    PID=$(cat "$PID_FILE")
    if kill -0 "$PID" 2>/dev/null; then
        echo "Server is already running (PID: $PID)"
        exit 1
    fi
    rm -f "$PID_FILE"
fi

# Check if port is already occupied by another process
PORT="${PORT:-8000}"
PID_BY_PORT=$(lsof -ti :"$PORT" 2>/dev/null || true)
if [ -n "$PID_BY_PORT" ]; then
    echo "Port $PORT is occupied by PID $PID_BY_PORT. Run stop.sh first or kill the process manually."
    exit 1
fi

echo "Building server..."
export GOPROXY=https://goproxy.cn,direct
go build -o qingqiu-world-server ./cmd/

echo "Starting server..."
nohup "./qingqiu-world-server" > "$LOG_DIR/server.log" 2>&1 &
PID=$!
echo "$PID" > "$PID_FILE"

sleep 1
if kill -0 "$PID" 2>/dev/null; then
    echo "Server started (PID: $PID)"
else
    echo "Server failed to start. Check $LOG_DIR/server.log"
    rm -f "$PID_FILE"
    exit 1
fi
