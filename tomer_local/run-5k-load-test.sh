#!/bin/bash
# run-5k-load-test.sh
QPS=${1:-100}
RUN_ID=$(date +%Y%m%d-%H%M%S)

DEV_DIR="/usr/local/google/home/glottman/dev/jetski_main"
AGENTS_DIR="${DEV_DIR}/agent-sandbox"
CL2_DIR="${DEV_DIR}/perf-tests/clusterloader2"
LOGS_DIR="${AGENTS_DIR}/tomer_local/logs"

mkdir -p "$LOGS_DIR"

echo "=== Starting Agent Sandbox 5,000 Burst Load Test ==="
echo "QPS: $QPS, Run ID: $RUN_ID"

cat <<JSON_EOF > "${AGENTS_DIR}/tomer_local/testoverrides.json"
{
  "CL2_QPS": $QPS,
  "CL2_WARMPOOL_SIZE": 200
}
JSON_EOF

pkill -f "kubectl port-forward -n agent-sandbox-system statefulset/agent-sandbox-controller 8080:8080" 2>/dev/null || true
kubectl port-forward -n agent-sandbox-system statefulset/agent-sandbox-controller 8080:8080 > /dev/null 2>&1 &

cd "$CL2_DIR"
echo "Cleaning up any existing clusterloader2 namespaces..."
kubectl delete namespace -l e2e-framework=clusterloader2 --wait=true 2>/dev/null

echo "Running clusterloader2 5,000 Sandbox Burst Test..."
./clusterloader2 \
  --testconfig="${AGENTS_DIR}/dev/load-test/agent-sandbox-5k-rapid-burst.yaml" \
  --kubeconfig=$HOME/.kube/config \
  --provider=gke \
  --testoverrides="${AGENTS_DIR}/tomer_local/testoverrides.json" \
  2>&1 | tee "${LOGS_DIR}/clusterloader2-5k-${QPS}qps-${RUN_ID}.log"

echo "ClusterLoader2 execution complete. Scraping Prometheus metrics..."
curl -s http://localhost:8080/metrics | grep -E "sandbox_.*_latency_seconds|sandbox_.*_created_total" > "${LOGS_DIR}/prom-metrics-5k-${RUN_ID}.txt"

if [ -f "junit.xml" ]; then
  mv junit.xml "${LOGS_DIR}/junit-5k-${RUN_ID}.xml"
fi

echo "=== 5,000 Burst Load Test $RUN_ID Complete ==="
