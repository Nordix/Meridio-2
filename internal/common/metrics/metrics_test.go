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

package metrics

import "testing"

func TestValidatePrefix(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		wantErr bool
	}{
		{"default prefix", DefaultPrefix, false},
		{"empty", "", true},
		{"valid lowercase", "mrd", false},
		{"valid with digits and underscores", "m2_lb", false},
		{"single letter", "m", false},
		{"exactly max length", "abcdefghij", false}, // 10 chars
		{"exceeds max length", "abcdefghijk", true}, // 11 chars
		{"uppercase rejected", "Meridio", true},
		{"leading digit rejected", "2meridio", true},
		{"leading underscore rejected", "_meridio", true},
		{"hyphen rejected", "meridio-2", true},
		{"space rejected", "meridio 2", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePrefix(tt.prefix)
			if tt.wantErr && err == nil {
				t.Errorf("ValidatePrefix(%q): expected error, got nil", tt.prefix)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidatePrefix(%q): expected no error, got: %v", tt.prefix, err)
			}
		})
	}
}

func TestEnabled(t *testing.T) {
	tests := []struct {
		name        string
		metricsAddr string
		want        bool
	}{
		{"disabled sentinel", "0", false},
		{"empty string is enabled", "", true},
		{"bind address is enabled", ":8443", true},
		{"host:port is enabled", "127.0.0.1:8080", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Enabled(tt.metricsAddr); got != tt.want {
				t.Errorf("Enabled(%q) = %v, want %v", tt.metricsAddr, got, tt.want)
			}
		})
	}
}
