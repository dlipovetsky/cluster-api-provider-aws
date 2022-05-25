/*
Copyright 2022 The Kubernetes Authors.

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
	"context"
	"testing"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ekscontrolplanev1 "sigs.k8s.io/cluster-api-provider-aws/controlplane/eks/api/v1beta1"
	"sigs.k8s.io/cluster-api-provider-aws/pkg/cloud/scope"
	"sigs.k8s.io/cluster-api-provider-aws/pkg/cloud/services"
	"sigs.k8s.io/cluster-api-provider-aws/pkg/cloud/services/mock_services"
)

func TestAWSManagedControlPlaneReconciler_Reconcile(t *testing.T) {
	var (
		reconciler AWSManagedControlPlaneReconciler
		mockCtrl   *gomock.Controller
		ec2Svc     *mock_services.MockEC2Interface
		elbSvc     *mock_services.MockELBInterface
		networkSvc *mock_services.MockNetworkInterface
		sgSvc      *mock_services.MockSecurityGroupInterface
		recorder   *record.FakeRecorder
	)

	setup := func(t *testing.T, awsManagedControlPlane *ekscontrolplanev1.AWSManagedControlPlane) client.WithWatch {
		t.Helper()
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "capa-system",
			},
			Data: map[string][]byte{
				"AccessKeyID":     []byte("access-key-id"),
				"SecretAccessKey": []byte("secret-access-key"),
				"SessionToken":    []byte("session-token"),
			},
		}
		csClient := fake.NewClientBuilder().WithObjects(awsManagedControlPlane, secret).Build()

		mockCtrl = gomock.NewController(t)
		ec2Svc = mock_services.NewMockEC2Interface(mockCtrl)
		elbSvc = mock_services.NewMockELBInterface(mockCtrl)
		networkSvc = mock_services.NewMockNetworkInterface(mockCtrl)
		sgSvc = mock_services.NewMockSecurityGroupInterface(mockCtrl)

		recorder = record.NewFakeRecorder(2)

		reconciler = AWSManagedControlPlaneReconciler{
			Client: csClient,
			ec2ServiceFactory: func(scope.EC2Scope) services.EC2Interface {
				return ec2Svc
			},
			elbServiceFactory: func(elbScope scope.ELBScope) services.ELBInterface {
				return elbSvc
			},
			networkServiceFactory: func(managedScope scope.ManagedControlPlaneScope) services.NetworkInterface {
				return networkSvc
			},
			securityGroupFactory: func(managedScope scope.ManagedControlPlaneScope) services.SecurityGroupInterface {
				return sgSvc
			},
			Recorder: recorder,
		}
		return csClient
	}

	teardown := func() {
		mockCtrl.Finish()
	}

	t.Run("Reconciling an AWSManagedControlPlane", func(t *testing.T) {
		t.Run("Reconcile should not modify the default set of security groups", func(t *testing.T) {
			g := NewWithT(t)

			runningCluster := func() {
				sgSvc.EXPECT().ReconcileSecurityGroups().Return(nil)
			}

			cluster := getCluster("test", "test")
			awsManagedControlPlane := getAWSManagedControlPlane("test", "test")
			csClient := setup(t, &awsManagedControlPlane)
			defer teardown()
			runningCluster()
			managedScope, err := scope.NewManagedControlPlaneScope(scope.ManagedControlPlaneScopeParams{
				Client:         csClient,
				Cluster:        &cluster,
				ControlPlane:   &awsManagedControlPlane,
				ControllerName: "awsmanagedcontrolplane",
			})
			g.Expect(err).To(BeNil())
			_, err = reconciler.reconcileNormal(context.Background(), managedScope)
			g.Expect(err).To(BeNil())
		})
	})
}
