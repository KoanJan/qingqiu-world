#!/bin/sh
set -e

# Start the Go server in the background on internal port 8080
export PORT=8080
/app/qingqiu-world-server &
SERVER_PID=$!

# Start nginx in the foreground
nginx -g 'daemon off;' &
NGINX_PID=$!

# Wait for either process to exit and forward the signal
wait -n $SERVER_PID $NGINX_PID
