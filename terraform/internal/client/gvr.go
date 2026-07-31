package client

import "k8s.io/apimachinery/pkg/runtime/schema"

// GroupVersionResources for the agent-sandbox APIs, verified against the
// upstream CRD manifests (k8s/crds). The core Sandbox lives in
// agents.x-k8s.io; the template/warmpool/claim types live in the separate
// extensions.agents.x-k8s.io group. v1beta1 is the storage version for all.
var (
	SandboxGVR = schema.GroupVersionResource{
		Group: "agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxes",
	}
	SandboxTemplateGVR = schema.GroupVersionResource{
		Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxtemplates",
	}
	SandboxWarmPoolGVR = schema.GroupVersionResource{
		Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxwarmpools",
	}
	SandboxClaimGVR = schema.GroupVersionResource{
		Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxclaims",
	}
	CRDGVR = schema.GroupVersionResource{
		Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
	}
)

const (
	SandboxAPIVersion    = "agents.x-k8s.io/v1beta1"
	ExtensionsAPIVersion = "extensions.agents.x-k8s.io/v1beta1"
)
