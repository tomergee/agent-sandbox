import re
import sys

def parse_junit(file_path):
    print("Parsing JUnit Latency Data...")
    try:
        with open(file_path, "r") as f:
            content = f.read()
    except FileNotFoundError:
        print(f"File {file_path} not found.")
        return

    # Extract Wait for Warm Pool Pods to be Ready
    warm_pool_match = re.search(r'<testcase name="Wait for Warm Pool Pods to be Ready" classname="ClusterLoaderV2" time="([0-9\.]+)">', content)
    warm_pool_time = warm_pool_match.group(1) if warm_pool_match else "N/A"

    # Extract Burst creation and wait times
    create_times = []
    wait_times = []
    pause_times = []
    
    for match in re.finditer(r'<testcase name="Create Burst \d+ Sandbox Claims \(\d+\)" classname="ClusterLoaderV2" time="([0-9\.]+)">', content):
        create_times.append(float(match.group(1)))
    
    for match in re.finditer(r'<testcase name="Wait for Burst \d+ Sandbox Claims to be Ready" classname="ClusterLoaderV2" time="([0-9\.]+)">', content):
        wait_times.append(float(match.group(1)))
        
    for match in re.finditer(r'<testcase name="Pause \d+ Seconds After Burst \d+" classname="ClusterLoaderV2" time="([0-9\.]+)">', content):
        pause_times.append(float(match.group(1)))

    if create_times:
        avg_create = sum(create_times) / len(create_times)
    else:
        avg_create = 0
        
    if wait_times:
        avg_wait = sum(wait_times) / len(wait_times)
        max_wait = max(wait_times)
    else:
        avg_wait = 0
        max_wait = 0
        
    if pause_times:
        avg_pause = sum(pause_times) / len(pause_times)
    else:
        avg_pause = 0

    print(f"Warm Pool Prep: {warm_pool_time}s")
    print(f"Avg API Creation: {avg_create:.3f}s")
    print(f"Avg Wait Phase: {avg_wait:.3f}s")
    print(f"Max Wait Phase: {max_wait:.3f}s")
    print(f"Avg Pause: {avg_pause:.3f}s")

if __name__ == "__main__":
    if len(sys.argv) > 1:
        parse_junit(sys.argv[1])
    else:
        print("Usage: python extract_metrics.py <path_to_junit.xml>")
