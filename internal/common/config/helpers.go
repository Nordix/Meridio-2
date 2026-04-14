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

package config

import (
	"os"
	"strconv"

	"github.com/spf13/pflag"
)

// bindString binds an environment variable to a string configuration field
// Only applies if the corresponding flag was not explicitly set
func bindString(fs *pflag.FlagSet, flagName, envName string, target *string) {
	if !fs.Changed(flagName) {
		if val := os.Getenv(envName); val != "" {
			*target = val
		}
	}
}

// bindBool binds an environment variable to a boolean configuration field
// Only applies if the corresponding flag was not explicitly set
func bindBool(fs *pflag.FlagSet, flagName, envName string, target *bool) {
	if !fs.Changed(flagName) {
		if val := os.Getenv(envName); val != "" {
			if parsed, err := strconv.ParseBool(val); err == nil {
				*target = parsed
			}
		}
	}
}

// bindInt binds an environment variable to an integer configuration field
// Only applies if the corresponding flag was not explicitly set
func bindInt(fs *pflag.FlagSet, flagName, envName string, target *int) {
	if !fs.Changed(flagName) {
		if val := os.Getenv(envName); val != "" {
			if parsed, err := strconv.Atoi(val); err == nil {
				*target = parsed
			}
		}
	}
}
