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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// These are integration tests that validate the webhook through the Kubernetes API
// They test the full admission control flow, not just the validation logic
var _ = Describe("DBUpgrade Webhook Integration Tests", func() {
	Context("Create Validation via API", func() {
		It("should accept valid selfHosted DBUpgrade", func() {
			dbUpgrade := &DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "envtest-selfhosted-valid",
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

			Expect(k8sClient.Create(ctx, dbUpgrade)).To(Succeed())

			// Cleanup
			Expect(k8sClient.Delete(ctx, dbUpgrade)).To(Succeed())
		})

		It("should reject selfHosted without connection", func() {
			dbUpgrade := &DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "envtest-selfhosted-invalid",
					Namespace: "default",
				},
				Spec: DBUpgradeSpec{
					Migrations: MigrationsSpec{
						Image: "test:v1",
					},
					Database: DatabaseSpec{
						Type: DatabaseTypeSelfHosted,
						// Missing connection - should be rejected
					},
				},
			}

			err := k8sClient.Create(ctx, dbUpgrade)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("requires connection"))
		})

		It("should accept valid awsRds DBUpgrade", func() {
			dbUpgrade := &DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "envtest-awsrds-valid",
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

			Expect(k8sClient.Create(ctx, dbUpgrade)).To(Succeed())

			// Cleanup
			Expect(k8sClient.Delete(ctx, dbUpgrade)).To(Succeed())
		})

		It("should reject awsRds without AWS config or connection", func() {
			dbUpgrade := &DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "envtest-awsrds-invalid",
					Namespace: "default",
				},
				Spec: DBUpgradeSpec{
					Migrations: MigrationsSpec{
						Image: "test:v1",
					},
					Database: DatabaseSpec{
						Type: DatabaseTypeAWSRDS,
						// Missing both aws and connection - should be rejected
					},
				},
			}

			err := k8sClient.Create(ctx, dbUpgrade)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("requires either aws or connection"))
		})

		It("should reject awsRds with incomplete AWS config", func() {
			dbUpgrade := &DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "envtest-awsrds-incomplete",
					Namespace: "default",
				},
				Spec: DBUpgradeSpec{
					Migrations: MigrationsSpec{
						Image: "test:v1",
					},
					Database: DatabaseSpec{
						Type: DatabaseTypeAWSRDS,
						AWS: &AWSSpec{
							RoleArn: "arn:aws:iam::123456789012:role/test",
							// Missing region, host, dbName, username
						},
					},
				},
			}

			err := k8sClient.Create(ctx, dbUpgrade)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("region is required"))
		})
	})

	Context("Update Validation - Immutability via API", func() {
		var testDBUpgrade *DBUpgrade

		BeforeEach(func() {
			// Create a valid DBUpgrade for update tests
			testDBUpgrade = &DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "envtest-immutability",
					Namespace: "default",
				},
				Spec: DBUpgradeSpec{
					Migrations: MigrationsSpec{
						Image: "myapp/migrations:v1.0.0",
						Dir:   "/migrations",
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
			Expect(k8sClient.Create(ctx, testDBUpgrade)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup
			Expect(k8sClient.Delete(ctx, testDBUpgrade)).To(Succeed())
		})

		It("should reject changing database.type", func() {
			// Fetch the current version
			current := &DBUpgrade{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testDBUpgrade), current)).To(Succeed())

			// Try to change database type
			current.Spec.Database.Type = DatabaseTypeAWSRDS
			current.Spec.Database.AWS = &AWSSpec{
				RoleArn:  "arn:aws:iam::123456789012:role/test",
				Region:   "us-east-1",
				Host:     "db.amazonaws.com",
				Port:     5432,
				DBName:   "testdb",
				Username: "testuser",
			}

			err := k8sClient.Update(ctx, current)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("database.type is immutable"))
		})

		It("should reject changing database.connection.urlSecretRef", func() {
			// Fetch the current version
			current := &DBUpgrade{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testDBUpgrade), current)).To(Succeed())

			// Try to change secret reference
			current.Spec.Database.Connection.URLSecretRef.Name = "different-secret"

			err := k8sClient.Update(ctx, current)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("database.connection.urlSecretRef is immutable"))
		})

		It("should allow changing migrations.image", func() {
			// Fetch the current version
			current := &DBUpgrade{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testDBUpgrade), current)).To(Succeed())

			// Change migrations image - this should succeed
			current.Spec.Migrations.Image = "myapp/migrations:v2.0.0"

			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			// Verify the change was applied
			updated := &DBUpgrade{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testDBUpgrade), updated)).To(Succeed())
			Expect(updated.Spec.Migrations.Image).To(Equal("myapp/migrations:v2.0.0"))
		})

		It("should allow changing migrations.dir", func() {
			// Fetch the current version
			current := &DBUpgrade{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testDBUpgrade), current)).To(Succeed())

			// Change migrations directory - this should succeed
			current.Spec.Migrations.Dir = "/new-migrations"

			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			// Verify the change was applied
			updated := &DBUpgrade{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testDBUpgrade), updated)).To(Succeed())
			Expect(updated.Spec.Migrations.Dir).To(Equal("/new-migrations"))
		})
	})

	Context("Update Validation - AWS Immutability via API", func() {
		var awsDBUpgrade *DBUpgrade

		BeforeEach(func() {
			// Create a valid AWS DBUpgrade for immutability tests
			awsDBUpgrade = &DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "envtest-aws-immutability",
					Namespace: "default",
				},
				Spec: DBUpgradeSpec{
					Migrations: MigrationsSpec{
						Image: "myapp/migrations:v1.0.0",
					},
					Database: DatabaseSpec{
						Type: DatabaseTypeAWSRDS,
						AWS: &AWSSpec{
							RoleArn:  "arn:aws:iam::123456789012:role/test-role",
							Region:   "us-east-1",
							Host:     "mydb.abc123.us-east-1.rds.amazonaws.com",
							Port:     5432,
							DBName:   "myapp",
							Username: "migrator",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, awsDBUpgrade)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup
			Expect(k8sClient.Delete(ctx, awsDBUpgrade)).To(Succeed())
		})

		It("should reject changing database.aws.roleArn", func() {
			// Fetch the current version
			current := &DBUpgrade{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(awsDBUpgrade), current)).To(Succeed())

			// Try to change roleArn
			current.Spec.Database.AWS.RoleArn = "arn:aws:iam::123456789012:role/different-role"

			err := k8sClient.Update(ctx, current)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("database.aws.roleArn is immutable"))
		})

		It("should reject changing database.aws.region", func() {
			// Fetch the current version
			current := &DBUpgrade{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(awsDBUpgrade), current)).To(Succeed())

			// Try to change region
			current.Spec.Database.AWS.Region = "us-west-2"

			err := k8sClient.Update(ctx, current)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("database.aws.region is immutable"))
		})

		It("should reject changing database.aws.host", func() {
			// Fetch the current version
			current := &DBUpgrade{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(awsDBUpgrade), current)).To(Succeed())

			// Try to change host
			current.Spec.Database.AWS.Host = "different-db.abc123.us-east-1.rds.amazonaws.com"

			err := k8sClient.Update(ctx, current)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("database.aws.host is immutable"))
		})

		It("should reject changing database.aws.dbName", func() {
			// Fetch the current version
			current := &DBUpgrade{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(awsDBUpgrade), current)).To(Succeed())

			// Try to change dbName
			current.Spec.Database.AWS.DBName = "different-db"

			err := k8sClient.Update(ctx, current)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("database.aws.dbName is immutable"))
		})
	})
})
