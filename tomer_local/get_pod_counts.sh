#!/bin/bash

# Get the current timestamp
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Get all pods
PODS=$(kubectl get pods --no-headers)

# --- Warmpool Pods ---
WARMPOOL_COMPLETED=$(echo "$PODS" | grep "^warmpool-" | grep "Completed" | wc -l)
WARMPOOL_RUNNING=$(echo "$PODS" | grep "^warmpool-" | grep "Running" | wc -l)
WARMPOOL_CRASHLOOPBACKOFF=$(echo "$PODS" | grep "^warmpool-" | grep "CrashLoopBackOff" | wc -l)
# Get all warmpool pods, then exclude the ones already counted
WARMPOOL_TOTAL=$(echo "$PODS" | grep "^warmpool-" | wc -l)
WARMPOOL_OTHER=$(($WARMPOOL_TOTAL - $WARMPOOL_COMPLETED - $WARMPOOL_RUNNING - $WARMPOOL_CRASHLOOPBACKOFF))


# --- Non-Warmpool Pods ---
NONWARMPOOL_COMPLETED=$(echo "$PODS" | grep -v "^warmpool-" | grep -v "^agent" | grep "Completed" | wc -l)
NONWARMPOOL_RUNNING=$(echo "$PODS" | grep -v "^warmpool-" | grep -v "^agent" | grep "Running" | wc -l)
NONWARMPOOL_CRASHLOOPBACKOFF=$(echo "$PODS" | grep -v "^warmpool-" | grep -v "^agent" | grep "CrashLoopBackOff" | wc -l)
# Get all non-warmpool pods, then exclude the ones already counted
NONWARMPOOL_TOTAL=$(echo "$PODS" | grep -v "^warmpool-" | grep -v "^agent" | wc -l)
NONWARMPOOL_OTHER=$(($NONWARMPOOL_TOTAL - $NONWARMPOOL_COMPLETED - $NONWARMPOOL_RUNNING - $NONWARMPOOL_CRASHLOOPBACKOFF))

# --- Agent Pods ---
AGENT_COMPLETED=$(echo "$PODS" | grep "^agent" | grep "Completed" | wc -l)
AGENT_RUNNING=$(echo "$PODS" | grep "^agent" | grep "Running" | wc -l)
AGENT_CRASHLOOPBACKOFF=$(echo "$PODS" | grep "^agent" | grep "CrashLoopBackOff" | wc -l)
# Get all agent pods, then exclude the ones already counted
AGENT_TOTAL=$(echo "$PODS" | grep "^agent" | wc -l)
AGENT_OTHER=$(($AGENT_TOTAL - $AGENT_COMPLETED - $AGENT_RUNNING - $AGENT_CRASHLOOPBACKOFF))


# --- Output ---
echo "Timestamp,Warmpool_Completed,Warmpool_Running,Warmpool_CrashLoopBackOff,Warmpool_Other,NonWarmpool_Completed,NonWarmpool_Running,NonWarmpool_CrashLoopBackOff,NonWarmpool_Other,Agent_Completed,Agent_Running,Agent_CrashLoopBackOff,Agent_Other"
echo "$TIMESTAMP,$WARMPOOL_COMPLETED,$WARMPOOL_RUNNING,$WARMPOOL_CRASHLOOPBACKOFF,$WARMPOOL_OTHER,$NONWARMPOOL_COMPLETED,$NONWARMPOOL_RUNNING,$NONWARMPOOL_CRASHLOOPBACKOFF,$NONWARMPOOL_OTHER,$AGENT_COMPLETED,$AGENT_RUNNING,$AGENT_CRASHLOOPBACKOFF,$AGENT_OTHER"

# --- Append to CSV ---
echo "$TIMESTAMP,$WARMPOOL_COMPLETED,$WARMPOOL_RUNNING,$WARMPOOL_CRASHLOOPBACKOFF,$WARMPOOL_OTHER,$NONWARMPOOL_COMPLETED,$NONWARMPOOL_RUNNING,$NONWARMPOOL_CRASHLOOPBACKOFF,$NONWARMPOOL_OTHER,$AGENT_COMPLETED,$AGENT_RUNNING,$AGENT_CRASHLOOPBACKOFF,$AGENT_OTHER" >> logs/load-test-5k-burst.csv