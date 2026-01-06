/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("DBUpgrade Webhook", func() {
	Context("Database Validation", func() {
		It("should accept selfHosted with connection secret", func() {
			dbUpgrade := &DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-selfhosted",
					Namespace: "default",
				},
				Spec: DBUpgradeSpec{
					Migrations: MigrationsSpec{
						Image: "test:v1",
					},
					Database: DatabaseSpec{
						Type: DatabaseTypeSelfHosted,
						Connection: &ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "db-secret",
								},
								Key: "url",
							},
						},
					},
				},
			}

			Expect(dbUpgrade.validateDBUpgrade()).To(Succeed())
		})

		It("should reject selfHosted without connection", func() {
			dbUpgrade := &DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid-selfhosted",
					Namespace: "default",
				},
				Spec: DBUpgradeSpec{
					Migrations: MigrationsSpec{
						Image: "test:v1",
					},
					Database: DatabaseSpec{
						Type: DatabaseTypeSelfHosted,
						// Missing connection - should fail
					},
				},
			}

			err := dbUpgrade.validateDBUpgrade()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("requires connection"))
		})

		It("should accept awsRds with AWS config", func() {
			dbUpgrade := &DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-aws",
					Namespace: "default",
				},
				Spec: DBUpgradeSpec{
					Migrations: MigrationsSpec{
						Image: "test:v1",
					},
					Database: DatabaseSpec{
						Type: DatabaseTypeAWSRDS,
						AWS: &AWSSpec{
							RoleArn:  "arn:aws:iam::123456789012:role/test",
							Region:   "us-east-1",
							Host:     "db.amazonaws.com",
							Port:     5432,
							DBName:   "testdb",
							Username: "testuser",
						},
					},
				},
			}

			Expect(dbUpgrade.validateDBUpgrade()).To(Succeed())
		})

		It("should reject awsRds without AWS config or connection", func() {
			dbUpgrade := &DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid-aws",
					Namespace: "default",
				},
				Spec: DBUpgradeSpec{
					Migrations: MigrationsSpec{
						Image: "test:v1",
					},
					Database: DatabaseSpec{
						Type: DatabaseTypeAWSRDS,
						// Missing both aws and connection - should fail
					},
				},
			}

			err := dbUpgrade.validateDBUpgrade()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("requires either aws or connection"))
		})
	})

	Context("Metric Validation", func() {
		It("should accept Pod metric with pods target", func() {
			metric := MetricCheck{
				Name:       "test-metric",
				MetricName: "cpu_usage",
				Target: MetricTarget{
					Type: MetricTargetTypePods,
					Pods: &PodsTarget{
						Selector: metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "test"},
						},
					},
				},
				Threshold: ThresholdSpec{
					Operator: ThresholdOperatorLT,
					Value:    resource.MustParse("80"),
				},
			}

			Expect(validateMetricCheck(metric)).To(Succeed())
		})

		It("should reject Pod metric without pods target", func() {
			metric := MetricCheck{
				Name:       "test-metric",
				MetricName: "cpu_usage",
				Target: MetricTarget{
					Type: MetricTargetTypePods,
					// Missing Pods field - should fail
				},
				Threshold: ThresholdSpec{
					Operator: ThresholdOperatorLT,
					Value:    resource.MustParse("80"),
				},
			}

			err := validateMetricCheck(metric)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("requires target.pods"))
		})

		It("should reject invalid threshold quantity", func() {
			metric := MetricCheck{
				Name:       "test-metric",
				MetricName: "cpu_usage",
				Target: MetricTarget{
					Type: MetricTargetTypePods,
					Pods: &PodsTarget{
						Selector: metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "test"},
						},
					},
				},
				Threshold: ThresholdSpec{
					Operator: ThresholdOperatorLT,
					Value:    resource.Quantity{}, // Invalid empty quantity
				},
			}

			err := validateMetricCheck(metric)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("Immutability Validation", func() {
		It("should reject changing database.type", func() {
			old := &DBUpgrade{
				Spec: DBUpgradeSpec{
					Database: DatabaseSpec{
						Type: DatabaseTypeSelfHosted,
						Connection: &ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "db-secret"},
								Key:                  "url",
							},
						},
					},
				},
			}

			new := old.DeepCopy()
			new.Spec.Database.Type = DatabaseTypeAWSRDS

			err := new.validateImmutableFields(old)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("database.type is immutable"))
		})

		It("should reject changing database.connection.urlSecretRef", func() {
			old := &DBUpgrade{
				Spec: DBUpgradeSpec{
					Database: DatabaseSpec{
						Type: DatabaseTypeSelfHosted,
						Connection: &ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "db-secret"},
								Key:                  "url",
							},
						},
					},
				},
			}

			new := old.DeepCopy()
			new.Spec.Database.Connection.URLSecretRef.Name = "different-secret"

			err := new.validateImmutableFields(old)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("database.connection.urlSecretRef is immutable"))
		})

		It("should reject changing database.aws.roleArn", func() {
			old := &DBUpgrade{
				Spec: DBUpgradeSpec{
					Database: DatabaseSpec{
						Type: DatabaseTypeAWSRDS,
						AWS: &AWSSpec{
							RoleArn:  "arn:aws:iam::123456789012:role/old-role",
							Region:   "us-east-1",
							Host:     "db.amazonaws.com",
							Port:     5432,
							DBName:   "testdb",
							Username: "testuser",
						},
					},
				},
			}

			new := old.DeepCopy()
			new.Spec.Database.AWS.RoleArn = "arn:aws:iam::123456789012:role/new-role"

			err := new.validateImmutableFields(old)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("database.aws.roleArn is immutable"))
		})

		It("should allow changing migrations.image", func() {
			old := &DBUpgrade{
				Spec: DBUpgradeSpec{
					Migrations: MigrationsSpec{
						Image: "myapp/migrations:v1.0.0",
					},
					Database: DatabaseSpec{
						Type: DatabaseTypeSelfHosted,
						Connection: &ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "db-secret"},
								Key:                  "url",
							},
						},
					},
				},
			}

			new := old.DeepCopy()
			new.Spec.Migrations.Image = "myapp/migrations:v2.0.0"

			err := new.validateImmutableFields(old)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should allow changing migrations.dir", func() {
			old := &DBUpgrade{
				Spec: DBUpgradeSpec{
					Migrations: MigrationsSpec{
						Image: "myapp/migrations:v1.0.0",
						Dir:   "/old-migrations",
					},
					Database: DatabaseSpec{
						Type: DatabaseTypeSelfHosted,
						Connection: &ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "db-secret"},
								Key:                  "url",
							},
						},
					},
				},
			}

			new := old.DeepCopy()
			new.Spec.Migrations.Dir = "/new-migrations"

			err := new.validateImmutableFields(old)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
