/*
Copyright 2021 The Kubernetes Authors.

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

package v1alpha4

import (
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilpointer "k8s.io/utils/pointer"
	"sigs.k8s.io/cluster-api-provider-aws/pkg/hash"
	clusterv1 "sigs.k8s.io/cluster-api/cmd/clusterctl/api/v1alpha3"
)

// TODO (richardcase): get this working with defaulter-gen

// SetDefaults_Bastion is used by defaulter-gen.
func SetDefaults_Bastion(obj *Bastion) { //nolint:golint,stylecheck
	// Default to allow open access to the bastion host if no CIDR Blocks have been set
	if len(obj.AllowedCIDRBlocks) == 0 && !obj.DisableIngressRules {
		obj.AllowedCIDRBlocks = []string{"0.0.0.0/0"}
	}
}

// SetDefaults_NetworkSpec is used by defaulter-gen.
func SetDefaults_NetworkSpec(obj *NetworkSpec) { //nolint:golint,stylecheck
	// Default to Calico ingress rules if no rules have been set
	if obj.CNI == nil {
		obj.CNI = &CNISpec{
			CNIIngressRules: CNIIngressRules{
				{
					Description: "bgp (calico)",
					Protocol:    SecurityGroupProtocolTCP,
					FromPort:    179,
					ToPort:      179,
				},
				{
					Description: "IP-in-IP (calico)",
					Protocol:    SecurityGroupProtocolIPinIP,
					FromPort:    -1,
					ToPort:      65535,
				},
			},
		}
	}
}

// SetDefaults_Labels is used by defaulter-gen.
func SetDefaults_Labels(obj *metav1.ObjectMeta) { //nolint:golint,stylecheck
	// Defaults to set label if no labels have been set
	if obj.Labels == nil {
		obj.Labels = map[string]string{
			clusterv1.ClusterctlMoveHierarchyLabelName: ""}
	}
}

func SetDefaults_AWSLoadBalancerSpec(log logr.Logger, clusterName string, obj *AWSLoadBalancerSpec) {
	if obj == nil {
		obj = &AWSLoadBalancerSpec{}
	}

	if obj.Name == nil {
		defaultName, err := GenerateELBName(clusterName)
		if err != nil {
			log.Error(err, "Failed to generate ELB name")
		}
		obj.Name = utilpointer.StringPtr(defaultName)
	}
}

// GenerateELBName generates a formatted ELB name via either
// concatenating the cluster name to the "-apiserver" suffix
// or computing a hash for clusters with names above 32 characters.
func GenerateELBName(clusterName string) (string, error) {
	standardELBName := generateStandardELBName(clusterName)
	if len(standardELBName) <= 32 {
		return standardELBName, nil
	}

	elbName, err := generateHashedELBName(clusterName)
	if err != nil {
		return "", err
	}

	return elbName, nil
}

// generateStandardELBName generates a formatted ELB name based on cluster
// and ELB name.
func generateStandardELBName(clusterName string) string {
	elbCompatibleClusterName := strings.ReplaceAll(clusterName, ".", "-")
	return fmt.Sprintf("%s-%s", elbCompatibleClusterName, APIServerRoleTagValue)
}

// generateHashedELBName generates a 32-character hashed name based on cluster
// and ELB name.
func generateHashedELBName(clusterName string) (string, error) {
	// hashSize = 32 - length of "k8s" - length of "-" = 28
	shortName, err := hash.Base36TruncatedHash(clusterName, 28)
	if err != nil {
		return "", errors.Wrap(err, "unable to create ELB name")
	}

	return fmt.Sprintf("%s-%s", shortName, "k8s"), nil
}
