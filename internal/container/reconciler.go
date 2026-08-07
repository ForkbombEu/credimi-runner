package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

type Reconciler struct {
	Engine   Engine
	RunnerID string
}

func (r Reconciler) Reconcile(ctx context.Context, desired []Spec) Result {
	result := Result{Failures: map[string]error{}}
	networks := map[string]struct{}{}
	for _, spec := range desired {
		if spec.Network != "" {
			networks[spec.Network] = struct{}{}
		}
	}
	for network := range networks {
		if err := r.Engine.EnsureNetwork(ctx, network); err != nil {
			result.Failures["network:"+network] = err
			return result
		}
	}
	existing, err := r.Engine.List(ctx, map[string]string{ManagedLabel: "true", RunnerLabel: r.RunnerID})
	if err != nil {
		result.Failures["list"] = err
		return result
	}
	byName := map[string]Resource{}
	for _, resource := range existing {
		byName[resource.Name] = resource
	}
	names := make([]string, 0, len(desired))
	for _, spec := range desired {
		names = append(names, spec.Name)
	}
	sort.Strings(names)
	desiredByName := map[string]Spec{}
	for _, spec := range desired {
		spec = withFingerprint(r.RunnerID, spec)
		desiredByName[spec.Name] = spec
	}
	for _, name := range names {
		spec := desiredByName[name]
		current, ok := byName[name]
		if ok && current.Labels[FingerprintLabel] != spec.Labels[FingerprintLabel] {
			if err := r.Engine.Stop(ctx, name); err != nil {
				result.Failures[name] = err
				continue
			}
			if err := r.Engine.Remove(ctx, name); err != nil {
				result.Failures[name] = err
				continue
			}
			result.Recreated = append(result.Recreated, name)
			ok = false
		}
		if !ok {
			if spec.PullPolicy == "always" || spec.PullPolicy == "if-not-present" {
				if err := r.Engine.Pull(ctx, spec.Image); err != nil {
					result.Failures[name] = err
					continue
				}
			}
			if err := r.Engine.Create(ctx, spec); err != nil {
				result.Failures[name] = err
				continue
			}
			result.Created = append(result.Created, name)
			if err := r.Engine.Start(ctx, name); err != nil {
				result.Failures[name] = err
				continue
			}
			result.Started = append(result.Started, name)
			continue
		}
		result.Adopted = append(result.Adopted, name)
		if !current.Running {
			if err := r.Engine.Start(ctx, name); err != nil {
				result.Failures[name] = err
				continue
			}
			result.Started = append(result.Started, name)
		}
	}
	for name := range byName {
		if _, ok := desiredByName[name]; ok {
			continue
		}
		if err := r.Engine.Stop(ctx, name); err != nil {
			result.Failures[name] = err
			continue
		}
		if err := r.Engine.Remove(ctx, name); err != nil {
			result.Failures[name] = err
			continue
		}
		result.Removed = append(result.Removed, name)
	}
	return result
}
func withFingerprint(runnerID string, spec Spec) Spec {
	if spec.Labels == nil {
		spec.Labels = map[string]string{}
	}
	spec.Labels[ManagedLabel] = "true"
	spec.Labels[RunnerLabel] = runnerID
	spec.Labels[FingerprintLabel] = fingerprint(spec)
	return spec
}
func fingerprint(spec Spec) string {
	copy := spec
	copy.Labels = map[string]string{}
	for k, v := range spec.Labels {
		if k != FingerprintLabel {
			copy.Labels[k] = v
		}
	}
	encoded := fmt.Sprintf("%#v", copy)
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:])
}
