/*
Copyright (c) 2026 OpenInfra Foundation Europe. All rights reserved.

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

package gateway

const (
	// Kubernetes resource kinds
	kindGateway              = "Gateway"
	kindGatewayConfiguration = "GatewayConfiguration"

	// Attachment types
	attachmentTypeNAD = "NAD"

	// Gateway API condition messages
	// messageWaitingForController is the default message for Unknown status with Pending reason
	// Matches Gateway API default: {status: "Unknown", reason:"Pending", message:"Waiting for controller"}
	messageWaitingForController = "Waiting for controller"
	messageProgrammed           = "LB Deployment reconciled"

	// YAML decoder buffer size (standard page size)
	yamlDecoderBufferSize = 4096

	// LB Deployment naming
	lbDeploymentPrefix = "sllb-"
	// LBDeploymentTemplateFile is the filename for the LB Deployment template
	LBDeploymentTemplateFile = "lb-deployment.yaml"

	// Labels
	labelGatewayName = "gateway.networking.k8s.io/gateway-name"
	labelManagedBy   = "app.kubernetes.io/managed-by"
	managedByValue   = "gateway-controller.meridio-2.nordix.org"
)
