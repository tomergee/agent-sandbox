import sys

def generate_yaml():
    total_claims = 6000 # 20 bursts * 300 claims
    burst_size = 300
    num_bursts = 20
    
    yaml_content = f"""{{{{$namespaces := DefaultParam .CL2_NAMESPACES 1}}}}
{{{{$qps := DefaultParam .CL2_QPS 300}}}}
{{{{$warmpool_size := DefaultParam .CL2_WARMPOOL_SIZE 1000}}}}
{{{{$namespacePrefix := DefaultParam .CL2_NAMESPACE_PREFIX "agent-sandbox"}}}}
name: agent-sandbox-6k-rapid-burst
namespace:
  number: {{{{$namespaces}}}}
  prefix: {{{{$namespacePrefix}}}}
tuningSets:
- name: BurstCreate
  qpsLoad:
    qps: {{{{$qps}}}}
steps:
- name: Start Startup Latency Measurement
  measurements:
  - Identifier: SandboxStartupLatency
    Method: PodStartupLatency
    Params:
      action: start
      labelSelector: "app=agent-sandbox-load-test"

- name: Setup Sandbox Template
  phases:
  - namespaceRange: {{min: 1, max: {{{{$namespaces}}}}}}
    replicasPerNamespace: 1
    tuningSet: BurstCreate
    objectBundle:
    - basename: template
      objectTemplatePath: "cluster-loader-sandbox-template.yaml"

- name: Setup Sandbox Warm Pool
  phases:
  - namespaceRange: {{min: 1, max: {{{{$namespaces}}}}}}
    replicasPerNamespace: 1
    tuningSet: BurstCreate
    objectBundle:
    - basename: warmpool
      objectTemplatePath: "cluster-loader-sandbox-warmpool-custom.yaml"

- name: Wait for Warm Pool Pods to be Ready
  measurements:
  - Identifier: WaitForWarmPoolPods
    Method: WaitForRunningPods
    Params:
      action: start
      labelSelector: "agents.x-k8s.io/pool"
      desiredPodCount: {{{{MultiplyInt 1000 $namespaces}}}}
"""

    for i in range(1, num_bursts + 1):
        target_claims = i * burst_size
        yaml_content += f"""
- name: Create Burst {i} Sandbox Claims ({burst_size})
  phases:
  - namespaceRange: {{min: 1, max: {{{{$namespaces}}}}}}
    replicasPerNamespace: {target_claims}
    tuningSet: BurstCreate
    objectBundle:
    - basename: agent-claim
      objectTemplatePath: "cluster-loader-sandbox-claim.yaml"

- name: Wait for Burst {i} Sandbox Claims to be Ready
  measurements:
  - Identifier: WaitForBurst{i}SandboxClaims
    Method: WaitForGenericK8sObjects
    Params:
      action: start
      objectGroup: extensions.agents.x-k8s.io
      objectVersion: v1alpha1
      objectResource: sandboxclaims
      namespaceRange: {{min: 1, max: {{{{$namespaces}}}}}}
      successfulConditions: ["Ready=True"]
      failedConditions: []
      minDesiredObjectCount: {{{{MultiplyInt {target_claims} $namespaces}}}}
      maxFailedObjectCount: 0
      timeout: 60m
      refreshInterval: 20ms

- name: Pause 20 Seconds After Burst {i}
  measurements:
  - Identifier: WaitAfterBurst{i}
    Method: Sleep
    Params:
      duration: 20s
"""

    yaml_content += """
- name: Gather Results
  measurements:
  - Identifier: SandboxStartupLatency
    Method: PodStartupLatency
    Params:
      action: gather
      labelSelector: "app=agent-sandbox-load-test"

- name: Delete Sandbox Claims
  phases:
  - namespaceRange: {min: 1, max: {{$namespaces}}}
    replicasPerNamespace: 0
    tuningSet: BurstCreate
    objectBundle:
    - basename: agent-claim
      objectTemplatePath: "cluster-loader-sandbox-claim.yaml"

- name: Delete Sandbox Warm Pool
  phases:
  - namespaceRange: {min: 1, max: {{$namespaces}}}
    replicasPerNamespace: 0
    tuningSet: BurstCreate
    objectBundle:
    - basename: warmpool
      objectTemplatePath: "cluster-loader-sandbox-warmpool-custom.yaml"

- name: Delete Sandbox Templates
  phases:
  - namespaceRange: {min: 1, max: {{$namespaces}}}
    replicasPerNamespace: 0
    tuningSet: BurstCreate
    objectBundle:
    - basename: template
      objectTemplatePath: "cluster-loader-sandbox-template.yaml"
"""
    print(yaml_content)

if __name__ == "__main__":
    generate_yaml()
