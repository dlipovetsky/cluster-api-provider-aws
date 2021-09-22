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

	"github.com/pkg/errors"
	"sigs.k8s.io/cluster-api-provider-aws/pkg/hash"
)

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
