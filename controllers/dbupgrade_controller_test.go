package controllers

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	dbupgradev1alpha1 "github.com/subganapathy/automatic-db-upgrades/api/v1alpha1"
)

// TestComputeSpecHash tests the spec hash computation
func TestComputeSpecHash(t *testing.T) {
	spec1 := dbupgradev1alpha1.DBUpgradeSpec{
		Migrations: dbupgradev1alpha1.MigrationsSpec{
			Image: "test:v1",
			Dir:   "/migrations",
		},
		Database: dbupgradev1alpha1.DatabaseSpec{
			Type: dbupgradev1alpha1.DatabaseTypeSelfHosted,
			Connection: &dbupgradev1alpha1.ConnectionSpec{
				URLSecretRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "db-secret",
					},
					Key: "url",
				},
			},
		},
	}

	spec2 := dbupgradev1alpha1.DBUpgradeSpec{
		Migrations: dbupgradev1alpha1.MigrationsSpec{
			Image: "test:v2", // Different image
			Dir:   "/migrations",
		},
		Database: dbupgradev1alpha1.DatabaseSpec{
			Type: dbupgradev1alpha1.DatabaseTypeSelfHosted,
			Connection: &dbupgradev1alpha1.ConnectionSpec{
				URLSecretRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "db-secret",
					},
					Key: "url",
				},
			},
		},
	}

	hash1 := computeSpecHash(spec1)
	hash2 := computeSpecHash(spec2)
	hash1Again := computeSpecHash(spec1)

	// Hash should be consistent for same spec
	if hash1 != hash1Again {
		t.Errorf("Hash not consistent: %s != %s", hash1, hash1Again)
	}

	// Hash should be different for different specs
	if hash1 == hash2 {
		t.Errorf("Hash should be different for different specs: %s == %s", hash1, hash2)
	}

	// Hash should be 8 characters
	if len(hash1) != 8 {
		t.Errorf("Hash length should be 8, got %d", len(hash1))
	}
}

// TestIsJobRunning tests the isJobRunning helper
func TestIsJobRunning(t *testing.T) {
	tests := []struct {
		name     string
		job      *batchv1.Job
		expected bool
	}{
		{
			name:     "nil job",
			job:      nil,
			expected: false,
		},
		{
			name: "job with active pods",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Active: 1,
				},
			},
			expected: true,
		},
		{
			name: "job with no active pods",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Active: 0,
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isJobRunning(tt.job)
			if result != tt.expected {
				t.Errorf("isJobRunning() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

// TestIsJobSucceeded tests the isJobSucceeded helper
func TestIsJobSucceeded(t *testing.T) {
	tests := []struct {
		name     string
		job      *batchv1.Job
		expected bool
	}{
		{
			name:     "nil job",
			job:      nil,
			expected: false,
		},
		{
			name: "job succeeded",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobComplete,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "job not succeeded",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobComplete,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "job with no conditions",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isJobSucceeded(tt.job)
			if result != tt.expected {
				t.Errorf("isJobSucceeded() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

// TestIsJobFailed tests the isJobFailed helper
func TestIsJobFailed(t *testing.T) {
	tests := []struct {
		name     string
		job      *batchv1.Job
		expected bool
	}{
		{
			name:     "nil job",
			job:      nil,
			expected: false,
		},
		{
			name: "job failed",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobFailed,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "job not failed",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobFailed,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "job with no conditions",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isJobFailed(tt.job)
			if result != tt.expected {
				t.Errorf("isJobFailed() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
