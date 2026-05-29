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

package v1alpha1

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var gatewayRouterLog = logf.Log.WithName("gatewayrouter-resource")

// SetupGatewayRouterWebhookWithManager registers the webhook for GatewayRouter in the manager.
func SetupGatewayRouterWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &meridio2v1alpha1.GatewayRouter{}).
		WithValidator(&GatewayRouterCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-meridio-2-nordix-org-v1alpha1-gatewayrouter,mutating=false,failurePolicy=fail,sideEffects=None,groups=meridio-2.nordix.org,resources=gatewayrouters,verbs=create;update,versions=v1alpha1,name=vgatewayrouter-v1alpha1.kb.io,admissionReviewVersions=v1

// GatewayRouterCustomValidator struct is responsible for validating the GatewayRouter resource
// when it is created, updated, or deleted.
//
// +kubebuilder:object:generate=false
type GatewayRouterCustomValidator struct{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type GatewayRouter.
func (v *GatewayRouterCustomValidator) ValidateCreate(_ context.Context, obj *meridio2v1alpha1.GatewayRouter) (admission.Warnings, error) {
	gatewayRouterLog.Info("Validation for GatewayRouter upon creation", "name", obj.GetName())
	return nil, v.validateGatewayRouter(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type GatewayRouter.
func (v *GatewayRouterCustomValidator) ValidateUpdate(_ context.Context, _, newObj *meridio2v1alpha1.GatewayRouter) (admission.Warnings, error) {
	gatewayRouterLog.Info("Validation for GatewayRouter upon update", "name", newObj.GetName())
	return nil, v.validateGatewayRouter(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type GatewayRouter.
func (v *GatewayRouterCustomValidator) ValidateDelete(_ context.Context, obj *meridio2v1alpha1.GatewayRouter) (admission.Warnings, error) {
	gatewayRouterLog.Info("Validation for GatewayRouter upon deletion", "name", obj.GetName())
	return nil, nil
}

func (v *GatewayRouterCustomValidator) validateGatewayRouter(r *meridio2v1alpha1.GatewayRouter) error {
	var allErrs field.ErrorList

	allErrs = append(allErrs, validateBFDSpec(r)...)
	allErrs = append(allErrs, validateBGPHoldTime(r)...)

	if len(allErrs) == 0 {
		return nil
	}

	return apierrors.NewInvalid(
		r.GroupVersionKind().GroupKind(),
		r.Name,
		allErrs,
	)
}

// validateBFDSpec validates BFD timer fields (minTx, minRx) are valid durations.
func validateBFDSpec(r *meridio2v1alpha1.GatewayRouter) field.ErrorList {
	var errs field.ErrorList

	// Validate BGP BFD timers
	if r.Spec.BGP.BFD != nil {
		basePath := field.NewPath("spec").Child("bgp").Child("bfd")
		errs = append(errs, validateBFDTimers(r.Spec.BGP.BFD, basePath)...)
	}

	// Validate Static BFD timers
	if r.Spec.Static != nil && r.Spec.Static.BFD != nil {
		basePath := field.NewPath("spec").Child("static").Child("bfd")
		errs = append(errs, validateBFDTimers(r.Spec.Static.BFD, basePath)...)
	}

	return errs
}

func validateBFDTimers(bfd *meridio2v1alpha1.BfdSpec, basePath *field.Path) field.ErrorList {
	var errs field.ErrorList

	if bfd.MinTx != "" {
		if _, err := time.ParseDuration(bfd.MinTx); err != nil {
			errs = append(errs, field.Invalid(basePath.Child("minTx"), bfd.MinTx, "must be a valid duration (e.g., 300ms, 1s)"))
		}
	}

	if bfd.MinRx != "" {
		if _, err := time.ParseDuration(bfd.MinRx); err != nil {
			errs = append(errs, field.Invalid(basePath.Child("minRx"), bfd.MinRx, "must be a valid duration (e.g., 300ms, 1s)"))
		}
	}

	return errs
}

// validateBGPHoldTime validates that bgp.holdTime is a parseable duration.
func validateBGPHoldTime(r *meridio2v1alpha1.GatewayRouter) field.ErrorList {
	var errs field.ErrorList

	if r.Spec.BGP.HoldTime != "" {
		if _, err := time.ParseDuration(r.Spec.BGP.HoldTime); err != nil {
			errs = append(errs, field.Invalid(
				field.NewPath("spec").Child("bgp").Child("holdTime"),
				r.Spec.BGP.HoldTime,
				"must be a valid duration (e.g., 90s, 1m30s, 1h)",
			))
		}
	}

	return errs
}
