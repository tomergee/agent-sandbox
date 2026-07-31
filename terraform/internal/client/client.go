// Package client wraps the Kubernetes dynamic client used by all provider
// resources. Connectivity errors are deferred to the first CRUD call so that
// `terraform plan` works before the target cluster exists.
package client

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

const FieldManager = "terraform-provider-agentsandbox"

// Config carries provider-block settings used to build a rest.Config.
type Config struct {
	KubeconfigPath string // explicit path; falls back to $KUBECONFIG then ~/.kube/config
	KubeconfigRaw  string // raw kubeconfig YAML; takes precedence over KubeconfigPath
	Context        string // kubeconfig context override
	InCluster      bool   // use in-cluster service account config
	UserAgent      string
}

type Client struct {
	Dynamic   dynamic.Interface
	Typed     kubernetes.Interface
	Discovery discovery.CachedDiscoveryInterface
	Mapper    meta.RESTMapper
}

// New builds the Kubernetes clients from cfg. Called lazily so that plans
// against not-yet-existing clusters do not fail during provider Configure.
func New(cfg Config) (*Client, error) {
	restCfg, err := buildRESTConfig(cfg)
	if err != nil {
		return nil, err
	}
	restCfg.QPS = 50
	restCfg.Burst = 100
	if cfg.UserAgent != "" {
		restCfg.UserAgent = cfg.UserAgent
	}

	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client: %w", err)
	}
	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building typed client: %w", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building discovery client: %w", err)
	}
	cached := memory.NewMemCacheClient(disc)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cached)

	return &Client{
		Dynamic:   dyn,
		Typed:     typed,
		Discovery: cached,
		Mapper:    mapper,
	}, nil
}

func buildRESTConfig(cfg Config) (*rest.Config, error) {
	if cfg.InCluster {
		restCfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in_cluster is set but in-cluster config unavailable: %w", err)
		}
		return restCfg, nil
	}

	if cfg.KubeconfigRaw != "" {
		clientCfg, err := clientcmd.NewClientConfigFromBytes([]byte(cfg.KubeconfigRaw))
		if err != nil {
			return nil, fmt.Errorf("parsing raw kubeconfig: %w", err)
		}
		raw, err := clientCfg.RawConfig()
		if err != nil {
			return nil, err
		}
		overrides := &clientcmd.ConfigOverrides{}
		if cfg.Context != "" {
			overrides.CurrentContext = cfg.Context
		}
		return clientcmd.NewDefaultClientConfig(raw, overrides).ClientConfig()
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if cfg.KubeconfigPath != "" {
		rules.ExplicitPath = expandHome(cfg.KubeconfigPath)
	} else if env := os.Getenv("KUBE_CONFIG_PATH"); env != "" {
		rules.ExplicitPath = expandHome(env)
	}
	overrides := &clientcmd.ConfigOverrides{}
	if cfg.Context != "" {
		overrides.CurrentContext = cfg.Context
	} else if env := os.Getenv("KUBE_CTX"); env != "" {
		overrides.CurrentContext = env
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

func expandHome(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
