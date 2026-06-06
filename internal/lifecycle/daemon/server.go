// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extensionsv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
)

type Server struct {
	client client.Client
}

func NewServer(c client.Client) *Server {
	return &Server{client: c}
}

type SandboxRequest struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type SuspendResponse struct {
	Success      bool   `json:"success"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type ResumeResponse struct {
	Success      bool   `json:"success"`
	PodIP        string `json:"pod_ip,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

func (s *Server) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/v1/sandbox/suspend", s.handleSuspend)
	mux.HandleFunc("/v1/sandbox/resume", s.handleResume)
	mux.HandleFunc("/healthz", s.handleHealthz)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func (s *Server) handleSuspend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Namespace == "" {
		http.Error(w, "name and namespace are required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	log.Printf("Suspending sandbox %s/%s", req.Namespace, req.Name)

	// Fetch Sandbox
	sb := &sandboxv1beta1.Sandbox{}
	err := s.client.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, sb)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeJSON(w, http.StatusNotFound, SuspendResponse{
				Success:      false,
				ErrorMessage: fmt.Sprintf("Sandbox %s not found", req.Name),
			})
			return
		}
		s.writeJSON(w, http.StatusInternalServerError, SuspendResponse{
			Success:      false,
			ErrorMessage: err.Error(),
		})
		return
	}

	// Patch OperatingMode to Suspended
	patch := client.MergeFrom(sb.DeepCopy())
	sb.Spec.OperatingMode = sandboxv1beta1.SandboxOperatingModeSuspended

	if err := s.client.Patch(ctx, sb, patch); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, SuspendResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("Failed to patch Sandbox: %v", err),
		})
		return
	}

	s.writeJSON(w, http.StatusOK, SuspendResponse{Success: true})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Namespace == "" {
		http.Error(w, "name and namespace are required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	log.Printf("Resuming sandbox %s/%s", req.Namespace, req.Name)

	// 1. Check if Sandbox already exists
	sb := &sandboxv1beta1.Sandbox{}
	err := s.client.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, sb)
	if err == nil {
		// Sandbox exists. If suspended, resume it
		if sb.Spec.OperatingMode == sandboxv1beta1.SandboxOperatingModeSuspended {
			patch := client.MergeFrom(sb.DeepCopy())
			sb.Spec.OperatingMode = sandboxv1beta1.SandboxOperatingModeRunning
			if err := s.client.Patch(ctx, sb, patch); err != nil {
				s.writeJSON(w, http.StatusInternalServerError, ResumeResponse{
					Success:      false,
					ErrorMessage: fmt.Sprintf("Failed to resume Sandbox: %v", err),
				})
				return
			}
		}

		// Wait for Sandbox to become ready
		ip, err := s.waitForSandboxReady(ctx, req.Namespace, req.Name)
		if err != nil {
			s.writeJSON(w, http.StatusInternalServerError, ResumeResponse{
				Success:      false,
				ErrorMessage: err.Error(),
			})
			return
		}

		s.writeJSON(w, http.StatusOK, ResumeResponse{
			Success: true,
			PodIP:   ip,
		})
		return
	}

	// 2. If Sandbox is not found, check/create SandboxClaim referencing the warm pool
	if apierrors.IsNotFound(err) {
		claimName := req.Name
		claim := &extensionsv1beta1.SandboxClaim{}
		err = s.client.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: claimName}, claim)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Create SandboxClaim referencing 'openclaw-warmpool'
				claim = &extensionsv1beta1.SandboxClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      claimName,
						Namespace: req.Namespace,
					},
					Spec: extensionsv1beta1.SandboxClaimSpec{
						WarmPoolRef: extensionsv1beta1.SandboxWarmPoolRef{
							Name: "openclaw-warmpool",
						},
					},
				}
				if err := s.client.Create(ctx, claim); err != nil {
					s.writeJSON(w, http.StatusInternalServerError, ResumeResponse{
						Success:      false,
						ErrorMessage: fmt.Sprintf("Failed to create SandboxClaim: %v", err),
					})
					return
				}
				log.Printf("Created SandboxClaim %s referencing warm pool", claimName)
			} else {
				s.writeJSON(w, http.StatusInternalServerError, ResumeResponse{
					Success:      false,
					ErrorMessage: err.Error(),
				})
				return
			}
		}

		// Wait for SandboxClaim to be Ready
		ip, err := s.waitForClaimReady(ctx, req.Namespace, claimName)
		if err != nil {
			s.writeJSON(w, http.StatusInternalServerError, ResumeResponse{
				Success:      false,
				ErrorMessage: err.Error(),
			})
			return
		}

		s.writeJSON(w, http.StatusOK, ResumeResponse{
			Success: true,
			PodIP:   ip,
		})
		return
	}

	s.writeJSON(w, http.StatusInternalServerError, ResumeResponse{
		Success:      false,
		ErrorMessage: err.Error(),
	})
}

func (s *Server) waitForSandboxReady(ctx context.Context, namespace, name string) (string, error) {
	timeout := time.After(60 * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout:
			return "", errors.New("timeout waiting for Sandbox to become ready")
		case <-ticker.C:
			sb := &sandboxv1beta1.Sandbox{}
			err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, sb)
			if err != nil {
				continue
			}

			// Check Ready condition
			for _, cond := range sb.Status.Conditions {
				if cond.Type == string(sandboxv1beta1.SandboxConditionReady) && cond.Status == metav1.ConditionTrue {
					if len(sb.Status.PodIPs) > 0 {
						return sb.Status.PodIPs[0], nil
					}
				}
			}
		}
	}
}

func (s *Server) waitForClaimReady(ctx context.Context, namespace, name string) (string, error) {
	timeout := time.After(60 * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout:
			return "", errors.New("timeout waiting for SandboxClaim to become ready")
		case <-ticker.C:
			claim := &extensionsv1beta1.SandboxClaim{}
			err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, claim)
			if err != nil {
				continue
			}

			// Check Ready condition
			for _, cond := range claim.Status.Conditions {
				if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
					if claim.Status.SandboxStatus.Name != "" {
						// Retrieve the corresponding Sandbox pod IP
						sb := &sandboxv1beta1.Sandbox{}
						err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: claim.Status.SandboxStatus.Name}, sb)
						if err == nil && len(sb.Status.PodIPs) > 0 {
							return sb.Status.PodIPs[0], nil
						}
					}
				}
			}
		}
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, val interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(val)
}
