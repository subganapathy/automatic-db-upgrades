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

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbupgradev1alpha1 "github.com/subganapathy/automatic-db-upgrades/api/v1alpha1"
)

var _ = Describe("DBUpgrade Controller", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("Secret Validation", func() {
		It("should set SecretNotFound reason when Secret is missing", func() {
			dbUpgrade := &dbupgradev1alpha1.DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-missing-secret",
					Namespace: "default",
				},
				Spec: dbupgradev1alpha1.DBUpgradeSpec{
					Migrations: dbupgradev1alpha1.MigrationsSpec{
						Image: "test:v1",
					},
					Database: dbupgradev1alpha1.DatabaseSpec{
						Type: dbupgradev1alpha1.DatabaseTypeSelfHosted,
						Connection: &dbupgradev1alpha1.ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "nonexistent-secret",
								},
								Key: "url",
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, dbUpgrade)).To(Succeed())

			// Wait for Progressing=False with Reason=SecretNotFound
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(dbUpgrade), dbUpgrade)
				if err != nil {
					return false
				}
				for _, cond := range dbUpgrade.Status.Conditions {
					if cond.Type == string(dbupgradev1alpha1.ConditionProgressing) &&
						cond.Status == metav1.ConditionFalse &&
						cond.Reason == dbupgradev1alpha1.ReasonSecretNotFound {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Expect(k8sClient.Delete(ctx, dbUpgrade)).To(Succeed())
		})

		It("should create Job when Secret exists", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-db-secret",
					Namespace: "default",
				},
				StringData: map[string]string{
					"url": "postgres://user:pass@localhost:5432/testdb",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			dbUpgrade := &dbupgradev1alpha1.DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-with-secret",
					Namespace: "default",
				},
				Spec: dbupgradev1alpha1.DBUpgradeSpec{
					Migrations: dbupgradev1alpha1.MigrationsSpec{
						Image: "test:v1",
					},
					Database: dbupgradev1alpha1.DatabaseSpec{
						Type: dbupgradev1alpha1.DatabaseTypeSelfHosted,
						Connection: &dbupgradev1alpha1.ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "test-db-secret",
								},
								Key: "url",
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, dbUpgrade)).To(Succeed())

			// Wait for Job to be created
			Eventually(func() bool {
				jobList := &batchv1.JobList{}
				err := k8sClient.List(ctx, jobList, client.InNamespace("default"))
				if err != nil {
					return false
				}
				for i := range jobList.Items {
					job := &jobList.Items[i]
					for _, owner := range job.OwnerReferences {
						if owner.UID == dbUpgrade.UID {
							return true
						}
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Expect(k8sClient.Delete(ctx, dbUpgrade)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
		})
	})

	Context("Job Creation", func() {
		It("should create Job with init container + Atlas pattern", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job-spec-secret",
					Namespace: "default",
				},
				StringData: map[string]string{
					"url": "postgres://user:pass@localhost:5432/testdb",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			dbUpgrade := &dbupgradev1alpha1.DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job-spec",
					Namespace: "default",
				},
				Spec: dbupgradev1alpha1.DBUpgradeSpec{
					Migrations: dbupgradev1alpha1.MigrationsSpec{
						Image: "customer/migrations:v1",
						Dir:   "/db/migrations",
					},
					Database: dbupgradev1alpha1.DatabaseSpec{
						Type: dbupgradev1alpha1.DatabaseTypeSelfHosted,
						Connection: &dbupgradev1alpha1.ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "test-job-spec-secret",
								},
								Key: "url",
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, dbUpgrade)).To(Succeed())

			// Wait for Job to be created and verify spec
			var createdJob *batchv1.Job
			Eventually(func() bool {
				jobList := &batchv1.JobList{}
				err := k8sClient.List(ctx, jobList, client.InNamespace("default"))
				if err != nil {
					return false
				}
				for i := range jobList.Items {
					job := &jobList.Items[i]
					for _, owner := range job.OwnerReferences {
						if owner.UID == dbUpgrade.UID {
							createdJob = job
							return true
						}
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Verify Job spec
			Expect(createdJob).NotTo(BeNil())
			Expect(createdJob.Spec.BackoffLimit).NotTo(BeNil())
			Expect(*createdJob.Spec.BackoffLimit).To(Equal(int32(0)))

			// Verify init container (crane for extracting migrations)
			Expect(createdJob.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initContainer := createdJob.Spec.Template.Spec.InitContainers[0]
			Expect(initContainer.Name).To(Equal("fetch-migrations"))
			Expect(initContainer.Image).To(Equal(CraneImage))

			// Verify main container (Atlas CLI)
			Expect(createdJob.Spec.Template.Spec.Containers).To(HaveLen(1))
			mainContainer := createdJob.Spec.Template.Spec.Containers[0]
			Expect(mainContainer.Name).To(Equal("migrate"))
			Expect(mainContainer.Image).To(Equal(AtlasImage))
			Expect(mainContainer.Env).To(HaveLen(1))
			Expect(mainContainer.Env[0].Name).To(Equal("DATABASE_URL"))
			// Should use operator-managed secret, not customer's secret directly
			Expect(mainContainer.Env[0].ValueFrom.SecretKeyRef.Name).To(Equal("dbupgrade-test-job-spec-connection"))

			// Verify shared volume exists
			Expect(createdJob.Spec.Template.Spec.Volumes).To(HaveLen(1))
			Expect(createdJob.Spec.Template.Spec.Volumes[0].Name).To(Equal("migrations"))
			Expect(createdJob.Spec.Template.Spec.Volumes[0].EmptyDir).NotTo(BeNil())

			// Cleanup
			Expect(k8sClient.Delete(ctx, dbUpgrade)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
		})

		It("should create operator-managed Secret", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-operator-secret-customer",
					Namespace: "default",
				},
				StringData: map[string]string{
					"url": "postgres://user:pass@localhost:5432/testdb",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			dbUpgrade := &dbupgradev1alpha1.DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-operator-secret",
					Namespace: "default",
				},
				Spec: dbupgradev1alpha1.DBUpgradeSpec{
					Migrations: dbupgradev1alpha1.MigrationsSpec{
						Image: "test:v1",
					},
					Database: dbupgradev1alpha1.DatabaseSpec{
						Type: dbupgradev1alpha1.DatabaseTypeSelfHosted,
						Connection: &dbupgradev1alpha1.ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "test-operator-secret-customer",
								},
								Key: "url",
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, dbUpgrade)).To(Succeed())

			// Wait for operator-managed Secret to be created
			operatorSecretName := "dbupgrade-test-operator-secret-connection"
			Eventually(func() bool {
				operatorSecret := &corev1.Secret{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      operatorSecretName,
					Namespace: "default",
				}, operatorSecret)
				if err != nil {
					return false
				}
				// Verify the secret contains the URL
				_, hasURL := operatorSecret.Data["url"]
				return hasURL
			}, timeout, interval).Should(BeTrue())

			// Verify operator Secret has owner reference
			operatorSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      operatorSecretName,
				Namespace: "default",
			}, operatorSecret)).To(Succeed())
			Expect(operatorSecret.OwnerReferences).To(HaveLen(1))
			Expect(operatorSecret.OwnerReferences[0].UID).To(Equal(dbUpgrade.UID))

			// Cleanup
			Expect(k8sClient.Delete(ctx, dbUpgrade)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
		})
	})

	Context("Job Status Synchronization", func() {
		It("should update Ready condition when Job succeeds", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job-success-secret",
					Namespace: "default",
				},
				StringData: map[string]string{
					"url": "postgres://user:pass@localhost:5432/testdb",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			dbUpgrade := &dbupgradev1alpha1.DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job-success",
					Namespace: "default",
				},
				Spec: dbupgradev1alpha1.DBUpgradeSpec{
					Migrations: dbupgradev1alpha1.MigrationsSpec{
						Image: "test:v1",
					},
					Database: dbupgradev1alpha1.DatabaseSpec{
						Type: dbupgradev1alpha1.DatabaseTypeSelfHosted,
						Connection: &dbupgradev1alpha1.ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "test-job-success-secret",
								},
								Key: "url",
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, dbUpgrade)).To(Succeed())

			// Wait for Job to be created
			var createdJob *batchv1.Job
			Eventually(func() bool {
				jobList := &batchv1.JobList{}
				err := k8sClient.List(ctx, jobList, client.InNamespace("default"))
				if err != nil {
					return false
				}
				for i := range jobList.Items {
					job := &jobList.Items[i]
					for _, owner := range job.OwnerReferences {
						if owner.UID == dbUpgrade.UID {
							createdJob = job
							return true
						}
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Simulate Job completion
			createdJob.Status.Conditions = []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				},
			}
			Expect(k8sClient.Status().Update(ctx, createdJob)).To(Succeed())

			// Wait for Ready condition to be True
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      dbUpgrade.Name,
					Namespace: dbUpgrade.Namespace,
				}, dbUpgrade)
				if err != nil {
					return false
				}
				for _, cond := range dbUpgrade.Status.Conditions {
					if cond.Type == string(dbupgradev1alpha1.ConditionReady) && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Expect(k8sClient.Delete(ctx, dbUpgrade)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
		})

		It("should set JobFailed reason when Job fails", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job-failed-secret",
					Namespace: "default",
				},
				StringData: map[string]string{
					"url": "postgres://user:pass@localhost:5432/testdb",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			dbUpgrade := &dbupgradev1alpha1.DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job-failed",
					Namespace: "default",
				},
				Spec: dbupgradev1alpha1.DBUpgradeSpec{
					Migrations: dbupgradev1alpha1.MigrationsSpec{
						Image: "test:v1",
					},
					Database: dbupgradev1alpha1.DatabaseSpec{
						Type: dbupgradev1alpha1.DatabaseTypeSelfHosted,
						Connection: &dbupgradev1alpha1.ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "test-job-failed-secret",
								},
								Key: "url",
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, dbUpgrade)).To(Succeed())

			// Wait for Job to be created
			var createdJob *batchv1.Job
			Eventually(func() bool {
				jobList := &batchv1.JobList{}
				err := k8sClient.List(ctx, jobList, client.InNamespace("default"))
				if err != nil {
					return false
				}
				for i := range jobList.Items {
					job := &jobList.Items[i]
					for _, owner := range job.OwnerReferences {
						if owner.UID == dbUpgrade.UID {
							createdJob = job
							return true
						}
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Simulate Job failure
			createdJob.Status.Conditions = []batchv1.JobCondition{
				{
					Type:   batchv1.JobFailed,
					Status: corev1.ConditionTrue,
					Reason: "BackoffLimitExceeded",
				},
			}
			Expect(k8sClient.Status().Update(ctx, createdJob)).To(Succeed())

			// Wait for Progressing=False with Reason=JobFailed
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      dbUpgrade.Name,
					Namespace: dbUpgrade.Namespace,
				}, dbUpgrade)
				if err != nil {
					return false
				}
				for _, cond := range dbUpgrade.Status.Conditions {
					if cond.Type == string(dbupgradev1alpha1.ConditionProgressing) &&
						cond.Status == metav1.ConditionFalse &&
						cond.Reason == dbupgradev1alpha1.ReasonJobFailed {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Expect(k8sClient.Delete(ctx, dbUpgrade)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
		})
	})

	Context("Idempotency", func() {
		It("should not create duplicate Jobs for same spec", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-idempotent-secret",
					Namespace: "default",
				},
				StringData: map[string]string{
					"url": "postgres://user:pass@localhost:5432/testdb",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			dbUpgrade := &dbupgradev1alpha1.DBUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-idempotent",
					Namespace: "default",
				},
				Spec: dbupgradev1alpha1.DBUpgradeSpec{
					Migrations: dbupgradev1alpha1.MigrationsSpec{
						Image: "test:v1",
					},
					Database: dbupgradev1alpha1.DatabaseSpec{
						Type: dbupgradev1alpha1.DatabaseTypeSelfHosted,
						Connection: &dbupgradev1alpha1.ConnectionSpec{
							URLSecretRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "test-idempotent-secret",
								},
								Key: "url",
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, dbUpgrade)).To(Succeed())

			// Wait for Job to be created
			Eventually(func() int {
				jobList := &batchv1.JobList{}
				err := k8sClient.List(ctx, jobList, client.InNamespace("default"))
				if err != nil {
					return 0
				}
				count := 0
				for i := range jobList.Items {
					job := &jobList.Items[i]
					for _, owner := range job.OwnerReferences {
						if owner.UID == dbUpgrade.UID {
							count++
						}
					}
				}
				return count
			}, timeout, interval).Should(Equal(1))

			// Wait a bit more to ensure no duplicate Jobs are created
			Consistently(func() int {
				jobList := &batchv1.JobList{}
				err := k8sClient.List(ctx, jobList, client.InNamespace("default"))
				if err != nil {
					return 0
				}
				count := 0
				for i := range jobList.Items {
					job := &jobList.Items[i]
					for _, owner := range job.OwnerReferences {
						if owner.UID == dbUpgrade.UID {
							count++
						}
					}
				}
				return count
			}, time.Second*2, interval).Should(Equal(1))

			// Cleanup
			Expect(k8sClient.Delete(ctx, dbUpgrade)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
		})
	})
})
