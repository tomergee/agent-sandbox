# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import json
import fileinput
import sys
import collections

# Path: tomer_local/scripts/parse_controller_logs.py

latencies = []
claims_seen = collections.defaultdict(int)

# Use fileinput to handle stdin or file arguments
for line in fileinput.input():
    if "SandbClaimReadyMS" not in line:
        continue
    
    try:
        # Extract the JSON part.
        # Line format: timestamp INFO SandbClaimReadyMS {JSON}
        # Or similar. Let's find the first '{' and parse from there.
        start = line.find('{')
        if start == -1:
            continue
        
        json_str = line[start:]
        data = json.loads(json_str)
        
        name = data.get("name")
        latency_ms = data.get("latency_ms")
        
        if name and latency_ms is not None:
            latencies.append(latency_ms)
            claims_seen[name] += 1
            if claims_seen[name] > 1:
                print(f"Duplicate SandbClaimReadyMS for {name}: {latency_ms} ms", file=sys.stderr)
    except json.JSONDecodeError:
        #print(f"Failed to parse JSON: {line}", file=sys.stderr)
        pass
    except Exception as e:
        #print(f"Error parsing line: {e}", file=sys.stderr)
        pass

if not latencies:
    print("No latencies found.")
    sys.exit(0)

latencies.sort()
count = len(latencies)
average = sum(latencies) / count
p50 = latencies[int(count * 0.5)]
p90 = latencies[int(count * 0.9)]
p99 = latencies[int(count * 0.99)]

print(f"Total Events: {count}")
print(f"Unique Claims: {len(claims_seen)}")
print(f"Average: {average:.2f} ms")
print(f"P50: {p50:.2f} ms")
print(f"P90: {p90:.2f} ms")
print(f"P99: {p99:.2f} ms")

# Print duplicates summary
duplicates = {k: v for k, v in claims_seen.items() if v > 1}
if duplicates:
    print(f"\nFound {len(duplicates)} duplicate claims:", file=sys.stderr)
    # for k, v in duplicates.items():
    #     print(f"  {k}: {v} times", file=sys.stderr)
