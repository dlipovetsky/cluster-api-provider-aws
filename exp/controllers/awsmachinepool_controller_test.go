/*
Copyright 2020 The Kubernetes Authors.

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
	"bytes"
	"context"
	"flag"
	"fmt"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1alpha3"
	"sigs.k8s.io/cluster-api/controllers/noderefutil"
	capierrors "sigs.k8s.io/cluster-api/errors"
	expclusterv1 "sigs.k8s.io/cluster-api/exp/api/v1alpha3"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "sigs.k8s.io/cluster-api-provider-aws/api/v1alpha3"
	expinfrav1 "sigs.k8s.io/cluster-api-provider-aws/exp/api/v1alpha3"
	"sigs.k8s.io/cluster-api-provider-aws/pkg/cloud"
	"sigs.k8s.io/cluster-api-provider-aws/pkg/cloud/scope"
	"sigs.k8s.io/cluster-api-provider-aws/pkg/cloud/services"
	"sigs.k8s.io/cluster-api-provider-aws/pkg/cloud/services/mock_services"
)

func newSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
	}
}

func newKubeadmConfig(name string) *bootstrapv1.KubeadmConfig {
	return &bootstrapv1.KubeadmConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: bootstrapv1.GroupVersion.String(),
			Kind:       "KubeadmConfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       "foo",
		},
	}
}

func newAWSMachinePool(name string) *expinfrav1.AWSMachinePool {
	return &expinfrav1.AWSMachinePool{
		TypeMeta: metav1.TypeMeta{
			APIVersion: expinfrav1.GroupVersion.String(),
			Kind:       "AWSMachinePool",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: expinfrav1.AWSMachinePoolSpec{
			MinSize: int32(1),
			MaxSize: int32(1),
		},
	}
}

func newMachinePool(clusterName, machinePoolName string) *expclusterv1.MachinePool {
	return &expclusterv1.MachinePool{
		TypeMeta: metav1.TypeMeta{
			APIVersion: expclusterv1.GroupVersion.String(),
			Kind:       "MachinePool",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      machinePoolName,
			Namespace: "default",
		},
		Spec: expclusterv1.MachinePoolSpec{
			ClusterName: clusterName,
			Template: clusterv1.MachineTemplateSpec{
				Spec: clusterv1.MachineSpec{
					ClusterName: clusterName,
				},
			},
		},
	}
}

func newMachinePoolWithInfrastructureRef(clusterName, machinePoolName string, amp *expinfrav1.AWSMachinePool) *expclusterv1.MachinePool {
	// The APIVersion and Kind are empty on the object read from the API server.
	apiVersion, kind := expinfrav1.GroupVersion.WithKind("AWSMachinePool").ToAPIVersionAndKind()

	m := newMachinePool(clusterName, machinePoolName)
	m.Spec.Template.Spec.InfrastructureRef = corev1.ObjectReference{
		Kind:       kind,
		APIVersion: apiVersion,
		Name:       amp.Name,
		Namespace:  amp.Namespace,
		UID:        amp.UID,
	}
	return m
}

var _ = Describe("AWSMachinePoolReconciler", func() {
	var (
		reconciler AWSMachinePoolReconciler
	)

	BeforeEach(func() {
		if err := flag.Set("logtostderr", "false"); err != nil {
			_ = fmt.Errorf("Error setting logtostderr flag")
		}
		if err := flag.Set("v", "2"); err != nil {
			_ = fmt.Errorf("Error setting v flag")
		}
		klog.SetOutput(GinkgoWriter)

		reconciler = AWSMachinePoolReconciler{
			Client: k8sClient,
			Log:    ctrl.Log.WithName("controllers").WithName("AWSMachinePool"),
		}
	})
	AfterEach(func() {
	})

	Context("Mapping a bootstrap Secret to an AWSMachinePool", func() {

		var (
			s = &corev1.Secret{}
		)

		BeforeEach(func() {
			ctx := context.TODO()

			// create bootstrap secret
			s = newSecret("testsecret")
			Expect(k8sClient.Create(ctx, s)).To(Succeed())
		})
		AfterEach(func() {
			ctx := context.TODO()

			// delete bootstrap secret
			Expect(k8sClient.Delete(ctx, s)).To(Succeed())
		})

		When("bootstrap Secret is owned by a bootstrap config", func() {

			var (
				c = &bootstrapv1.KubeadmConfig{}
			)

			BeforeEach(func() {
				ctx := context.TODO()

				// create bootstrap config
				c = newKubeadmConfig("testconfig")
				Expect(k8sClient.Create(ctx, c)).To(Succeed())

				// update bootstrap secret with owner ref to bootstrap config
				Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Name}, c)).To(Succeed())
				Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: s.Name}, s)).To(Succeed())
				s.OwnerReferences = []metav1.OwnerReference{
					{
						APIVersion: bootstrapv1.GroupVersion.String(),
						Kind:       "KubeadmConfig",
						Name:       c.Name,
						UID:        c.UID,
					},
				}
				Expect(k8sClient.Update(ctx, s)).To(Succeed())
			})
			AfterEach(func() {
				ctx := context.TODO()

				// delete bootstrap config
				Expect(k8sClient.Delete(ctx, c)).To(Succeed())
			})

			When("bootstrap config is owned by a MachinePool", func() {

				var (
					mp = &expclusterv1.MachinePool{}
				)

				BeforeEach(func() {
					ctx := context.TODO()

					// create machinepool
					mp = newMachinePool("testcluster", "testpool")
					Expect(k8sClient.Create(ctx, mp)).To(Succeed())

					// update bootstrap config with owner ref to machinepool
					Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: mp.Namespace, Name: mp.Name}, mp)).To(Succeed())
					Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Name}, c)).To(Succeed())
					c.OwnerReferences = []metav1.OwnerReference{
						{
							APIVersion: expclusterv1.GroupVersion.String(),
							Kind:       "MachinePool",
							Name:       mp.Name,
							UID:        mp.UID,
						},
					}
					Expect(k8sClient.Update(ctx, c)).To(Succeed())
				})

				AfterEach(func() {
					ctx := context.TODO()

					// remove owner ref to machinepool from bootstrap config
					Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: mp.Namespace, Name: mp.Name}, mp)).To(Succeed())
					Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Name}, c)).To(Succeed())
					c.OwnerReferences = []metav1.OwnerReference{}
					Expect(k8sClient.Update(ctx, c)).To(Succeed())

					// delete machinepool
					Expect(k8sClient.Delete(ctx, mp)).To(Succeed())
				})

				When("MachinePool has an infrastructure ref to an AWSMachinePool", func() {

					var (
						amp = &expinfrav1.AWSMachinePool{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "testawspool",
								Namespace: "default",
							},
						}
					)

					BeforeEach(func() {
						ctx := context.TODO()

						// update machinepool with infrastructure ref to awsmachinepool
						Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: mp.Namespace, Name: mp.Name}, mp)).To(Succeed())
						mp.Spec.Template.Spec.InfrastructureRef = corev1.ObjectReference{
							APIVersion: expinfrav1.GroupVersion.String(),
							Kind:       "AWSMachinePool",
							Name:       amp.Name,
							Namespace:  amp.Namespace,
						}
						Expect(k8sClient.Update(ctx, mp)).To(Succeed())
					})
					AfterEach(func() {
						ctx := context.TODO()

						// remove infrastructure ref to awsmachinepool from machinepool
						Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: mp.Namespace, Name: mp.Name}, mp)).To(Succeed())
						mp.Spec.Template.Spec.InfrastructureRef = corev1.ObjectReference{}
						Expect(k8sClient.Update(ctx, mp)).To(Succeed())
					})

					It("should map to the AWSMachinePool", func() {
						obj := handler.MapObject{Meta: s.GetObjectMeta(), Object: s.DeepCopyObject()}
						got := reconciler.bootstrapSecretToAWSMachinePool(obj)
						want := []reconcile.Request{
							{
								NamespacedName: client.ObjectKey{
									Name:      amp.Name,
									Namespace: amp.Namespace,
								},
							},
						}
						Expect(got).To(Equal(want))
					})
				})

				When("MachinePool does not have an infrastructure ref to an AWSMachinePool", func() {
					It("should map to nothing", func() {
						obj := handler.MapObject{Meta: s.GetObjectMeta(), Object: s.DeepCopyObject()}
						result := reconciler.bootstrapSecretToAWSMachinePool(obj)
						Expect(result).To(HaveLen(0))
					})
				})

			})

			When("bootstrap config is not owned by a MachinePool", func() {
				It("should map to nothing", func() {
					obj := handler.MapObject{Meta: s.GetObjectMeta(), Object: s.DeepCopyObject()}
					result := reconciler.bootstrapSecretToAWSMachinePool(obj)
					Expect(result).To(HaveLen(0))
				})
			})

		})

		When("bootstrap Secret is not owned by a bootstrap config", func() {

			It("should map to nothing", func() {
				obj := handler.MapObject{Meta: s.GetObjectMeta(), Object: s.DeepCopyObject()}
				result := reconciler.bootstrapSecretToAWSMachinePool(obj)
				Expect(result).To(HaveLen(0))
			})
		})

	})

})

var _ = Describe("AWSMachinePoolReconciler", func() {
	var (
		reconciler     AWSMachinePoolReconciler
		cs             *scope.ClusterScope
		ms             *scope.MachinePoolScope
		mockCtrl       *gomock.Controller
		ec2Svc         *mock_services.MockEC2MachineInterface
		asgSvc         *mock_services.MockASGInterface
		recorder       *record.FakeRecorder
		awsMachinePool *expinfrav1.AWSMachinePool
		secret         *corev1.Secret
	)

	BeforeEach(func() {
		var err error

		if err := flag.Set("logtostderr", "false"); err != nil {
			_ = fmt.Errorf("Error setting logtostderr flag")
		}
		if err := flag.Set("v", "2"); err != nil {
			_ = fmt.Errorf("Error setting v flag")
		}
		ctx := context.TODO()
		klog.SetOutput(GinkgoWriter)

		awsMachinePool = &expinfrav1.AWSMachinePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "default",
			},
			Spec: expinfrav1.AWSMachinePoolSpec{
				MinSize: int32(1),
				MaxSize: int32(1),
			},
		}

		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "bootstrap-data",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"value": []byte("shell-script"),
			},
		}

		Expect(testEnv.Create(ctx, awsMachinePool)).To(Succeed())
		Expect(testEnv.Create(ctx, secret)).To(Succeed())

		ms, err = scope.NewMachinePoolScope(
			scope.MachinePoolScopeParams{
				Client: testEnv.Client,
				Cluster: &clusterv1.Cluster{
					Status: clusterv1.ClusterStatus{
						InfrastructureReady: true,
					},
				},
				MachinePool: &expclusterv1.MachinePool{
					Spec: expclusterv1.MachinePoolSpec{
						Template: clusterv1.MachineTemplateSpec{
							Spec: clusterv1.MachineSpec{
								Bootstrap: clusterv1.Bootstrap{
									DataSecretName: pointer.StringPtr("bootstrap-data"),
								},
							},
						},
					},
				},
				InfraCluster:   cs,
				AWSMachinePool: awsMachinePool,
			},
		)
		Expect(err).To(BeNil())

		cs, err = scope.NewClusterScope(
			scope.ClusterScopeParams{
				Cluster:    &clusterv1.Cluster{},
				AWSCluster: &infrav1.AWSCluster{},
			},
		)
		Expect(err).To(BeNil())

		mockCtrl = gomock.NewController(GinkgoT())
		ec2Svc = mock_services.NewMockEC2MachineInterface(mockCtrl)
		asgSvc = mock_services.NewMockASGInterface(mockCtrl)

		// If the test hangs for 9 minutes, increase the value here to the number of events during a reconciliation loop
		recorder = record.NewFakeRecorder(2)

		reconciler = AWSMachinePoolReconciler{
			ec2ServiceFactory: func(scope.EC2Scope) services.EC2MachineInterface {
				return ec2Svc
			},
			asgServiceFactory: func(cloud.ClusterScoper) services.ASGInterface {
				return asgSvc
			},
			Recorder: recorder,
		}
	})
	AfterEach(func() {
		ctx := context.TODO()
		mpPh, err := patch.NewHelper(awsMachinePool, testEnv)
		Expect(err).ShouldNot(HaveOccurred())
		awsMachinePool.SetFinalizers([]string{})
		Expect(mpPh.Patch(ctx, awsMachinePool)).To(Succeed())
		Expect(testEnv.Delete(ctx, awsMachinePool)).To(Succeed())
		Expect(testEnv.Delete(ctx, secret)).To(Succeed())
		mockCtrl.Finish()
	})

	Context("Reconciling an AWSMachinePool", func() {
		When("we can't reach amazon", func() {
			expectedErr := errors.New("no connection available ")

			BeforeEach(func() {
				ec2Svc.EXPECT().GetLaunchTemplate(gomock.Any()).Return(nil, expectedErr).AnyTimes()
				asgSvc.EXPECT().GetASGByName(gomock.Any()).Return(nil, expectedErr).AnyTimes()
			})

			It("should exit immediately on an error state", func() {
				er := capierrors.CreateMachineError
				ms.AWSMachinePool.Status.FailureReason = &er
				ms.AWSMachinePool.Status.FailureMessage = pointer.StringPtr("Couldn't create machine pool")

				buf := new(bytes.Buffer)
				klog.SetOutput(buf)

				_, _ = reconciler.reconcileNormal(context.Background(), ms, cs, cs)
				Expect(buf).To(ContainSubstring("Error state detected, skipping reconciliation"))
			})

			It("should add our finalizer to the machinepool", func() {
				_, _ = reconciler.reconcileNormal(context.Background(), ms, cs, cs)

				Expect(ms.AWSMachinePool.Finalizers).To(ContainElement(expinfrav1.MachinePoolFinalizer))
			})

			It("should exit immediately if cluster infra isn't ready", func() {
				ms.Cluster.Status.InfrastructureReady = false

				buf := new(bytes.Buffer)
				klog.SetOutput(buf)

				_, err := reconciler.reconcileNormal(context.Background(), ms, cs, cs)
				Expect(err).To(BeNil())
				Expect(buf.String()).To(ContainSubstring("Cluster infrastructure is not ready yet"))
				expectConditions(ms.AWSMachinePool, []conditionAssertion{{expinfrav1.ASGReadyCondition, corev1.ConditionFalse, clusterv1.ConditionSeverityInfo, infrav1.WaitingForClusterInfrastructureReason}})
			})

			It("should exit immediately if bootstrap data secret reference isn't available", func() {
				ms.MachinePool.Spec.Template.Spec.Bootstrap.DataSecretName = nil
				buf := new(bytes.Buffer)
				klog.SetOutput(buf)

				_, err := reconciler.reconcileNormal(context.Background(), ms, cs, cs)

				Expect(err).To(BeNil())
				Expect(buf.String()).To(ContainSubstring("Bootstrap data secret reference is not yet available"))
				expectConditions(ms.AWSMachinePool, []conditionAssertion{{expinfrav1.ASGReadyCondition, corev1.ConditionFalse, clusterv1.ConditionSeverityInfo, infrav1.WaitingForBootstrapDataReason}})
			})
		})

		When("there's a provider ID", func() {
			id := "<cloudProvider>://<optional>/<segments>/<providerid>"
			BeforeEach(func() {
				_, err := noderefutil.NewProviderID(id)
				Expect(err).To(BeNil())

				ms.AWSMachinePool.Spec.ProviderID = id
			})

			It("it should look up by provider ID when one exists", func() {
				expectedErr := errors.New("no connection available ")
				var launchtemplate *expinfrav1.AWSLaunchTemplate
				ec2Svc.EXPECT().GetLaunchTemplate(gomock.Any()).Return(launchtemplate, expectedErr)
				_, err := reconciler.reconcileNormal(context.Background(), ms, cs, cs)
				Expect(errors.Cause(err)).To(MatchError(expectedErr))
			})

			It("should try to create a new machinepool if none exists", func() {
				expectedErr := errors.New("Invalid instance")
				asgSvc.EXPECT().ASGIfExists(gomock.Any()).Return(nil, nil).AnyTimes()
				ec2Svc.EXPECT().GetLaunchTemplate(gomock.Any()).Return(nil, nil)
				ec2Svc.EXPECT().DiscoverLaunchTemplateAMI(gomock.Any()).Return(nil, nil)
				ec2Svc.EXPECT().CreateLaunchTemplate(gomock.Any(), gomock.Any(), gomock.Any()).Return("", expectedErr).AnyTimes()

				_, err := reconciler.reconcileNormal(context.Background(), ms, cs, cs)
				Expect(errors.Cause(err)).To(MatchError(expectedErr))
			})
		})

		When("ASG creation succeeds", func() {
			BeforeEach(func() {
				ec2Svc.EXPECT().GetLaunchTemplate(gomock.Any()).Return(nil, nil).AnyTimes()
				ec2Svc.EXPECT().CreateLaunchTemplate(gomock.Any(), gomock.Any(), gomock.Any()).Return("", nil).AnyTimes()
				asgSvc.EXPECT().GetASGByName(gomock.Any()).Return(nil, nil).AnyTimes()
			})
		})
	})

	Context("deleting an AWSMachinePool", func() {
		BeforeEach(func() {
			ms.AWSMachinePool.Finalizers = []string{
				expinfrav1.MachinePoolFinalizer,
				metav1.FinalizerDeleteDependents,
			}
		})

		It("should exit immediately on an error state", func() {
			expectedErr := errors.New("no connection available ")
			asgSvc.EXPECT().GetASGByName(gomock.Any()).Return(nil, expectedErr).AnyTimes()

			_, err := reconciler.reconcileDelete(ms, cs, cs)
			Expect(errors.Cause(err)).To(MatchError(expectedErr))
		})

		It("should log and remove finalizer when no machinepool exists", func() {
			asgSvc.EXPECT().GetASGByName(gomock.Any()).Return(nil, nil)
			ec2Svc.EXPECT().GetLaunchTemplate(gomock.Any()).Return(nil, nil).AnyTimes()

			buf := new(bytes.Buffer)
			klog.SetOutput(buf)

			_, err := reconciler.reconcileDelete(ms, cs, cs)
			Expect(err).To(BeNil())
			Expect(buf.String()).To(ContainSubstring("Unable to locate ASG"))
			Expect(ms.AWSMachinePool.Finalizers).To(ConsistOf(metav1.FinalizerDeleteDependents))
			Eventually(recorder.Events).Should(Receive(ContainSubstring("NoASGFound")))
		})

		It("should cause AWSMachinePool to go into NotReady", func() {
			inProgressASG := expinfrav1.AutoScalingGroup{
				Name:   "an-asg-that-is-currently-being-deleted",
				Status: expinfrav1.ASGStatusDeleteInProgress,
			}
			asgSvc.EXPECT().GetASGByName(gomock.Any()).Return(&inProgressASG, nil)
			ec2Svc.EXPECT().GetLaunchTemplate(gomock.Any()).Return(nil, nil).AnyTimes()

			buf := new(bytes.Buffer)
			klog.SetOutput(buf)
			_, err := reconciler.reconcileDelete(ms, cs, cs)
			Expect(err).To(BeNil())
			Expect(ms.AWSMachinePool.Status.Ready).To(Equal(false))
			Eventually(recorder.Events).Should(Receive(ContainSubstring("DeletionInProgress")))
		})
	})
})

//TODO: This was taken from awsmachine_controller_test, i think it should be moved to elsewhere in both locations like test/helpers

type conditionAssertion struct {
	conditionType clusterv1.ConditionType
	status        corev1.ConditionStatus
	severity      clusterv1.ConditionSeverity
	reason        string
}

func expectConditions(m *expinfrav1.AWSMachinePool, expected []conditionAssertion) {
	Expect(len(m.Status.Conditions)).To(BeNumerically(">=", len(expected)), "number of conditions")
	for _, c := range expected {
		actual := conditions.Get(m, c.conditionType)
		Expect(actual).To(Not(BeNil()))
		Expect(actual.Type).To(Equal(c.conditionType))
		Expect(actual.Status).To(Equal(c.status))
		Expect(actual.Severity).To(Equal(c.severity))
		Expect(actual.Reason).To(Equal(c.reason))
	}
}
