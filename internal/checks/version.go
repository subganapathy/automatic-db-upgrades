package checks

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbupgradev1alpha1 "github.com/subganapathy/automatic-db-upgrades/api/v1alpha1"
)

// VersionCheckResult contains the result of a version check
type VersionCheckResult struct {
	Passed  bool
	Message string
	// FailedPods contains pods that failed the check (if any)
	FailedPods []PodVersionInfo
	// SkippedPods contains pods that were skipped due to non-semver tags (when strictMode=false)
	SkippedPods []PodVersionInfo
}

// PodVersionInfo contains version info for a single pod
type PodVersionInfo struct {
	Name          string
	Namespace     string
	ContainerName string
	ImageTag      string
	Version       string
}

// CheckMinPodVersions validates that all pods matching the selector have at least the minimum version
func CheckMinPodVersions(ctx context.Context, c client.Client, namespace string, checks []dbupgradev1alpha1.MinPodVersionCheck) (*VersionCheckResult, error) {
	for _, check := range checks {
		result, err := checkSinglePodVersion(ctx, c, namespace, check)
		if err != nil {
			return nil, err
		}
		if !result.Passed {
			return result, nil
		}
	}

	return &VersionCheckResult{
		Passed:  true,
		Message: "All pod version checks passed",
	}, nil
}

func checkSinglePodVersion(ctx context.Context, c client.Client, namespace string, check dbupgradev1alpha1.MinPodVersionCheck) (*VersionCheckResult, error) {
	// Convert LabelSelector to labels.Selector
	selector, err := metav1.LabelSelectorAsSelector(&check.Selector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	// List pods matching the selector
	podList := &corev1.PodList{}
	if err := c.List(ctx, podList, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	if len(podList.Items) == 0 {
		return &VersionCheckResult{
			Passed:  false,
			Message: fmt.Sprintf("No pods found matching selector %v", check.Selector.MatchLabels),
		}, nil
	}

	// Parse minimum version
	minVersion, err := semver.NewVersion(strings.TrimPrefix(check.MinVersion, "v"))
	if err != nil {
		return nil, fmt.Errorf("invalid minimum version %q: %w", check.MinVersion, err)
	}

	// Determine strictMode (defaults to true if not specified)
	strictMode := true
	if check.StrictMode != nil {
		strictMode = *check.StrictMode
	}

	var failedPods []PodVersionInfo
	var skippedPods []PodVersionInfo
	checkedCount := 0

	for _, pod := range podList.Items {
		// Find the container to check
		containers := append(pod.Spec.Containers, pod.Spec.InitContainers...)
		for _, container := range containers {
			// Skip if containerName is specified and doesn't match
			if check.ContainerName != "" && container.Name != check.ContainerName {
				continue
			}

			podInfo := PodVersionInfo{
				Name:          pod.Name,
				Namespace:     pod.Namespace,
				ContainerName: container.Name,
				ImageTag:      container.Image,
			}

			// Extract version from image tag
			imageVersion := extractVersionFromImage(container.Image)
			if imageVersion == "" {
				podInfo.Version = "unknown"
				if strictMode {
					// In strict mode, non-semver is a failure
					failedPods = append(failedPods, podInfo)
				} else {
					// In non-strict mode, skip non-semver pods
					skippedPods = append(skippedPods, podInfo)
				}
				if check.ContainerName != "" {
					break
				}
				continue
			}

			podVersion, err := semver.NewVersion(strings.TrimPrefix(imageVersion, "v"))
			if err != nil {
				podInfo.Version = imageVersion
				if strictMode {
					// In strict mode, unparseable version is a failure
					failedPods = append(failedPods, podInfo)
				} else {
					// In non-strict mode, skip unparseable versions
					skippedPods = append(skippedPods, podInfo)
				}
				if check.ContainerName != "" {
					break
				}
				continue
			}

			podInfo.Version = imageVersion
			checkedCount++

			// Check if version meets minimum
			if podVersion.LessThan(minVersion) {
				failedPods = append(failedPods, podInfo)
			}

			// If containerName was specified, we found it - stop checking other containers
			if check.ContainerName != "" {
				break
			}
		}
	}

	if len(failedPods) > 0 {
		msg := fmt.Sprintf("%d pod(s) have version below minimum %s", len(failedPods), check.MinVersion)
		if len(skippedPods) > 0 {
			msg += fmt.Sprintf(" (%d skipped due to non-semver tags)", len(skippedPods))
		}
		return &VersionCheckResult{
			Passed:      false,
			Message:     msg,
			FailedPods:  failedPods,
			SkippedPods: skippedPods,
		}, nil
	}

	// If all pods were skipped and none were checked, that's suspicious
	if checkedCount == 0 && len(skippedPods) > 0 {
		return &VersionCheckResult{
			Passed:      false,
			Message:     fmt.Sprintf("No pods with semver tags found (%d skipped); cannot validate versions", len(skippedPods)),
			SkippedPods: skippedPods,
		}, nil
	}

	msg := fmt.Sprintf("All %d pod(s) meet minimum version %s", checkedCount, check.MinVersion)
	if len(skippedPods) > 0 {
		msg += fmt.Sprintf(" (%d skipped due to non-semver tags)", len(skippedPods))
	}
	return &VersionCheckResult{
		Passed:      true,
		Message:     msg,
		SkippedPods: skippedPods,
	}, nil
}

// extractVersionFromImage extracts the version tag from an image reference
// Handles formats like:
// - nginx:1.21.0
// - gcr.io/project/app:v2.1.0
// - registry.example.com:5000/app:1.0.0-rc1
// - sha256 digests return empty (unversioned)
func extractVersionFromImage(image string) string {
	// Remove digest if present
	if idx := strings.Index(image, "@"); idx != -1 {
		image = image[:idx]
	}

	// Find the tag separator (last colon after the last slash)
	lastSlash := strings.LastIndex(image, "/")
	tagStart := strings.LastIndex(image, ":")

	// If no tag or the colon is before the last slash (port number), return empty
	if tagStart == -1 || tagStart < lastSlash {
		return ""
	}

	tag := image[tagStart+1:]

	// Check if it looks like a version (starts with v or digit)
	if tag == "latest" || tag == "" {
		return ""
	}

	// Try to extract semver-like pattern
	versionPattern := regexp.MustCompile(`^v?(\d+\.\d+\.\d+(?:-[\w.]+)?(?:\+[\w.]+)?)`)
	if matches := versionPattern.FindStringSubmatch(tag); len(matches) > 0 {
		return matches[0]
	}

	// If it starts with a digit, return as-is
	if len(tag) > 0 && tag[0] >= '0' && tag[0] <= '9' {
		return tag
	}

	return ""
}

// CompareVersions compares two version strings and returns:
// -1 if v1 < v2
//
//	0 if v1 == v2
//	1 if v1 > v2
func CompareVersions(v1, v2 string) (int, error) {
	ver1, err := semver.NewVersion(strings.TrimPrefix(v1, "v"))
	if err != nil {
		return 0, fmt.Errorf("invalid version %q: %w", v1, err)
	}

	ver2, err := semver.NewVersion(strings.TrimPrefix(v2, "v"))
	if err != nil {
		return 0, fmt.Errorf("invalid version %q: %w", v2, err)
	}

	return ver1.Compare(ver2), nil
}
