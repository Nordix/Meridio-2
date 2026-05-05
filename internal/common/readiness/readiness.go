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

// Package readiness defines the shared contract between the LB and router
// controllers for signaling target availability via the filesystem.
package readiness

// LBReadyFilePrefix is the filename prefix used by the LB controller to signal
// that a DistributionGroup has ready targets. The router controller watches for
// files matching this prefix to gate VIP advertisement.
const LBReadyFilePrefix = "lb-ready-"
