#!/bin/sh
set -e

ATTEMPTS=0
MAX_ATTEMPTS=30

until mc alias set local http://minio:9000 minioadmin minioadmin; do
  ATTEMPTS=$((ATTEMPTS + 1))
  if [ "$ATTEMPTS" -ge "$MAX_ATTEMPTS" ]; then
    echo "ERROR: Failed to connect to MinIO after $MAX_ATTEMPTS attempts"
    exit 1
  fi
  echo "Waiting for MinIO... attempt $ATTEMPTS/$MAX_ATTEMPTS"
  sleep 2
done

mc mb local/mediaflow --ignore-existing
mc anonymous set download local/mediaflow

echo "MinIO initialized successfully"
exit 0
