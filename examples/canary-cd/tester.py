import subprocess
import time
import sys

def run_command(cmd):
    result = subprocess.run(cmd, shell=True, capture_output=True, text=True)
    if result.returncode != 0:
        print(f"Command failed: {cmd}")
        print(f"Error: {result.stderr}")
        return None
    return result.stdout.strip()

def create_claim(name):
    yaml_content = f"""
apiVersion: extensions.agents.x-k8s.io/v1alpha1
kind: SandboxClaim
metadata:
  name: {name}
  namespace: default
  labels:
    app: canary-test
spec:
  sandboxTemplateRef:
    name: sandbox-python-template-v1
  warmpool: python-pool
"""
    process = subprocess.Popen(["kubectl", "apply", "-f", "-"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    stdout, stderr = process.communicate(input=yaml_content)
    if process.returncode != 0:
        print(f"Failed to create claim {name}: {stderr}")
        return False
    print(f"Created claim {name}")
    return True

def get_assigned_sandbox(claim_name):
    cmd = f"kubectl get sandboxclaim {claim_name} -o jsonpath='{{.status.sandbox.name}}'"
    for _ in range(30): # Wait up to 30 seconds
        output = run_command(cmd)
        if output:
            return output
        time.sleep(1)
    return None

def get_sandbox_image(sandbox_name):
    cmd = f"kubectl get sandbox {sandbox_name} -o jsonpath='{{.spec.podTemplate.spec.containers[0].image}}'"
    return run_command(cmd)

def main():
    # Cleanup old claims if any
    print("Cleaning up old test claims...")
    run_command("kubectl delete sandboxclaim -l app=canary-test --ignore-not-found")
    
    num_claims = 5
    claims = []
    print(f"Creating {num_claims} test claims...")
    for i in range(num_claims):
        name = f"canary-test-claim-{i}"
        if create_claim(name):
            claims.append(name)
            
    print("\nWaiting for claims to be bound...")
    time.sleep(5) # Give it some initial time
    
    results = {"v1": 0, "v2": 0, "failed": 0}
    
    for claim in claims:
        sandbox = get_assigned_sandbox(claim)
        if not sandbox:
            print(f"Claim {claim} timed out waiting for sandbox.")
            results["failed"] += 1
            continue
            
        image = get_sandbox_image(sandbox)
        print(f"Claim {claim} got sandbox {sandbox} running image {image}")
        
        if "v2" in image:
            results["v2"] += 1
        else:
            results["v1"] += 1
            
    print("\n--- Test Results ---")
    print(f"Total claims: {num_claims}")
    print(f"V1 Sandboxes: {results['v1']}")
    print(f"V2 Sandboxes: {results['v2']}")
    print(f"Failed: {results['failed']}")
    
    # Cleanup
    print("\nCleaning up test claims...")
    run_command("kubectl delete sandboxclaim -l app=canary-test")

if __name__ == "__main__":
    main()
