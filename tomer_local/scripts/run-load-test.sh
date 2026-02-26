#!/bin/bash
# run-load-test.sh
# Usage: ./run-load-test.sh [QPS] [REPLICAS]
# Example: ./run-load-test.sh 100 50

QPS=${1:-50}
REPLICAS=${2:-50}
RUN_ID=$(date +%Y%m%d-%H%M%S)

# Base directories
DEV_DIR="/usr/local/google/home/glottman/dev/jetski_main"
AGENTS_DIR="${DEV_DIR}/agent-sandbox"
CL2_DIR="${DEV_DIR}/perf-tests/clusterloader2"
LOGS_DIR="${AGENTS_DIR}/tomer_local/logs"

mkdir -p "$LOGS_DIR"
LOG_FILE="${LOGS_DIR}/sandbox-startup-${RUN_ID}.log"

echo "=== Starting Agent Sandbox Load Test ==="
echo "QPS: $QPS, Replicas: $REPLICAS, Run ID: $RUN_ID"

# 1. Create overrides
cat <<EOF > "${AGENTS_DIR}/tomer_local/scripts/testoverrides.json"
{
  "CL2_QPS": $QPS,
  "CL2_REPLICAS": $REPLICAS
}
EOF

# 2. Port-forward metrics in background (if not already running)
pkill -f "kubectl port-forward -n agent-sandbox-system statefulset/agent-sandbox-controller 8080:8080" 2>/dev/null || true
kubectl port-forward -n agent-sandbox-system statefulset/agent-sandbox-controller 8080:8080 > /dev/null 2>&1 &

# 3. Start the background capture script
(
  echo "Watching for all SandboxClaims to reach Ready state..."
  export KUBECONFIG=$HOME/.kube/config
  while true; do
    # Check the count of Ready claims
    READY_COUNT=$(kubectl get sandboxclaims -n agent-sandbox-1 -l app=agent-sandbox-load-test -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' 2>/dev/null | grep -c "True")
    
    if [ "$READY_COUNT" -ge "$REPLICAS" ]; then
      echo "All $REPLICAS SandboxClaims are ready. Capturing raw data to $LOG_FILE..."
      echo "KIND   NAME   CREATION   READY" > "$LOG_FILE"
      
      # Fetch Claims and Sandboxes
      kubectl get sandboxclaims,sandboxes -n agent-sandbox-1 \
        -o custom-columns="KIND:.kind,NAME:.metadata.name,CREATION:.metadata.creationTimestamp,READY:.status.conditions[?(@.type=='Ready')].lastTransitionTime" \
        --no-headers >> "$LOG_FILE"
      
      echo "Data capture complete!"
      break
    fi
    sleep 2
  done
) &
CAPTURE_PID=$!

# 4. Run ClusterLoader2
cd "$CL2_DIR"
echo "Cleaning up any existing clusterloader2 namespaces..."
kubectl delete namespace -l e2e-framework=clusterloader2 --wait=true 2>/dev/null

echo "Running clusterloader2 (this will take a few minutes)..."
./clusterloader2 \
  --testconfig="${AGENTS_DIR}/dev/load-test/agent-sandbox-warmpool-load-test.yaml" \
  --kubeconfig=$HOME/.kube/config \
  --provider=gke \
  --testoverrides="${AGENTS_DIR}/tomer_local/scripts/testoverrides.json" \
  2>&1 | tee "${LOGS_DIR}/clusterloader2-${QPS}qps-${RUN_ID}.log"

echo "ClusterLoader2 execution complete. Scraping Prometheus metrics..."
curl -s http://localhost:8080/metrics | grep -E "sandbox_.*_latency_seconds|sandbox_.*_created_total" > "${LOGS_DIR}/prom-metrics-${RUN_ID}.txt"

if [ -f "junit.xml" ]; then
  mv junit.xml "${LOGS_DIR}/junit-${RUN_ID}.xml"
fi

# Cleanup monitor just in case it's stuck (e.g. test failed)
kill $CAPTURE_PID 2>/dev/null || true

echo "=== Load Test $RUN_ID Complete ==="
echo "- Raw data captured to: $LOG_FILE"
echo "- Prometheus metrics to: ${LOGS_DIR}/prom-metrics-${RUN_ID}.txt"
echo "- ClusterLoader log to: ${LOGS_DIR}/clusterloader2-${QPS}qps-${RUN_ID}.log"
