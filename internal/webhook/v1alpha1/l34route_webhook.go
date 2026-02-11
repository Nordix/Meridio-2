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
	"fmt"
	"net"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var l34routelog = logf.Log.WithName("l34route-resource")

// SetupL34RouteWebhookWithManager registers the webhook for L34Route in the manager.
func SetupL34RouteWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &meridio2v1alpha1.L34Route{}).
		WithValidator(&L34RouteCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-meridio-2-nordix-org-v1alpha1-l34route,mutating=false,failurePolicy=fail,sideEffects=None,groups=meridio-2.nordix.org,resources=l34routes,verbs=create;update,versions=v1alpha1,name=vl34route-v1alpha1.kb.io,admissionReviewVersions=v1

// L34RouteCustomValidator struct is responsible for validating the L34Route resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type L34RouteCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type L34Route.
func (v *L34RouteCustomValidator) ValidateCreate(_ context.Context, obj *meridio2v1alpha1.L34Route) (admission.Warnings, error) {
	l34routelog.Info("Validation for L34Route upon creation", "name", obj.GetName())
	return nil, v.validateL34Route(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type L34Route.
func (v *L34RouteCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *meridio2v1alpha1.L34Route) (admission.Warnings, error) {
	l34routelog.Info("Validation for L34Route upon update", "name", newObj.GetName())
	return nil, v.validateL34Route(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type L34Route.
func (v *L34RouteCustomValidator) ValidateDelete(_ context.Context, obj *meridio2v1alpha1.L34Route) (admission.Warnings, error) {
	l34routelog.Info("Validation for L34Route upon deletion", "name", obj.GetName())
	return nil, nil
}

func (v *L34RouteCustomValidator) validateL34Route(r *meridio2v1alpha1.L34Route) error {
	var allErrs field.ErrorList

	// Validate IP family consistency
	if len(r.Spec.SourceCIDRs) > 0 && len(r.Spec.DestinationCIDRs) > 0 {
		dstFamilySet, err := getIPFamilySet(r.Spec.DestinationCIDRs)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("destinationCIDRs"), r.Spec.DestinationCIDRs[0], "invalid CIDR format"))
		}

		srcFamilySet, err := getIPFamilySet(r.Spec.SourceCIDRs)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("sourceCIDRs"), r.Spec.SourceCIDRs[0], "invalid CIDR format"))
		}

		if srcFamilySet != dstFamilySet {
			allErrs = append(allErrs, field.Invalid(field.NewPath("spec"), r.Spec, "source and destination CIDRs must be of the same IP family"))
		}
	}

	// Validate source CIDRs for overlaps
	if cidr, err := validateCIDRs(r.Spec.SourceCIDRs); err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("sourceCIDRs"), cidr,
			fmt.Sprintf("source CIDR%s", err.Error())))
	}

	// Validate destination CIDRs for overlaps
	if cidr, err := validateCIDRs(r.Spec.DestinationCIDRs); err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("destinationCIDRs"), cidr,
			fmt.Sprintf("destination CIDR%s", err.Error())))
	}

	// Validate source ports for overlaps
	if p, err := validatePorts(r.Spec.SourcePorts); err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("sourcePorts"), p,
			fmt.Sprintf("source port%s", err.Error())))
	}

	// Validate destination ports for overlaps
	if p, err := validatePorts(r.Spec.DestinationPorts); err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec").Child("destinationPorts"), p,
			fmt.Sprintf("destination port%s", err.Error())))
	}

	if len(allErrs) == 0 {
		return nil
	}

	return apierrors.NewInvalid(
		r.GroupVersionKind().GroupKind(),
		r.Name,
		allErrs,
	)
}

// getIPFamilySet determines the IP family (IPv4, IPv6 or dual) of the given CIDRs.
func getIPFamilySet(cidrs []string) (string, error) {
	isIPv4 := false
	isIPv6 := false
	for _, c := range cidrs {
		ip, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			return "", fmt.Errorf("invalid CIDR format: %s", c)
		}
		if ip.To4() != nil {
			if len(ipnet.IP) != net.IPv4len && ip.To16() != nil && ip.To16()[10] == 0xff && ip.To16()[11] == 0xff { // IPv4-mapped IPv6 address
				isIPv6 = true
			}
			isIPv4 = true
		} else if ip.To16() != nil {
			isIPv6 = true
		} else {
			return "", fmt.Errorf("invalid IP address in CIDR: %s", c)
		}
	}

	if isIPv4 && isIPv6 {
		return "dual", nil
	} else if isIPv4 {
		return "ipv4", nil
	} else if isIPv6 {
		return "ipv6", nil
	}
	return "", fmt.Errorf("unable to determine IP family from CIDRs")
}

// validateCIDRs checks for overlapping CIDRs
func validateCIDRs(cidrs []string) (string, error) {
	allNonOverlappingCIDRs := make([]*net.IPNet, 0, len(cidrs))
	for i, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return c, fmt.Errorf("[%d]: %s", i, err.Error())
		}

		for j, m := range allNonOverlappingCIDRs {
			if cidrsOverlap(n, m) {
				return c, fmt.Errorf("[%d] and [%d]: overlapping CIDR", i, j)
			}
		}
		allNonOverlappingCIDRs = append(allNonOverlappingCIDRs, n)
	}
	return "", nil
}

func cidrsOverlap(a, b *net.IPNet) bool {
	return cidrContainsCIDR(a, b) || cidrContainsCIDR(b, a)
}

func cidrContainsCIDR(outer, inner *net.IPNet) bool {
	ol, _ := outer.Mask.Size()
	il, _ := inner.Mask.Size()
	if ol == il && outer.IP.Equal(inner.IP) {
		return true
	}
	if ol < il && outer.Contains(inner.IP) {
		return true
	}
	return false
}

type ports struct {
	start uint64
	end   uint64
}

func validatePorts(portList []string) (string, error) {
	var allPorts []ports
	for i, p := range portList {
		candidatePorts, err := parsePort(p)
		if err != nil {
			return p, fmt.Errorf("[%d]: %s", i, err.Error())
		}

		if allPorts, err = checkPortsOverlapping(allPorts, candidatePorts); err != nil {
			return p, fmt.Errorf("[%d]: %s", i, err.Error())
		}
	}
	return "", nil
}

func parsePort(p string) (ports, error) {
	if p == "any" {
		return ports{0, 65535}, nil
	}
	if strings.Contains(p, "-") {
		parts := strings.Split(p, "-")
		if len(parts) != 2 {
			return ports{}, fmt.Errorf("wrong format to define port range, <starting port>-<ending port>")
		}
		start, err := strconv.ParseUint(parts[0], 10, 16)
		if err != nil {
			return ports{}, fmt.Errorf("starting port %s is not a valid port number", parts[0])
		}
		end, err := strconv.ParseUint(parts[1], 10, 16)
		if err != nil {
			return ports{}, fmt.Errorf("ending port %s is not a valid port number", parts[1])
		}
		if start > end {
			return ports{}, fmt.Errorf("starting port cannot be larger than ending port")
		}
		return ports{start, end}, nil
	}
	port, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		return ports{}, fmt.Errorf("port %s is not a valid port number", p)
	}
	return ports{port, port}, nil
}

func checkPortsOverlapping(allPorts []ports, candidatePort ports) ([]ports, error) {
	if len(allPorts) == 0 {
		return append(allPorts, candidatePort), nil
	}
	for j, validp := range allPorts {
		if candidatePort.start > validp.end {
			if j == len(allPorts)-1 {
				return append(allPorts, candidatePort), nil
			}
			continue
		} else if candidatePort.end < validp.start {
			return insertPort(allPorts, j, candidatePort), nil
		} else {
			return allPorts, fmt.Errorf("overlapping ports")
		}
	}
	return allPorts, nil
}

func insertPort(pl []ports, i int, p ports) []ports {
	if i == len(pl) {
		return append(pl, p)
	}
	pl = append(pl[:i+1], pl[i:]...)
	pl[i] = p
	return pl
}
