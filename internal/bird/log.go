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

package bird

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// BirdLog represents a single BIRD log destination.
// Maps to the BIRD config directive:
//
//	log "filename" [size "backup"] | fixed "filename" size | syslog [name name] | stderr | udp address [port port] all|{ list of classes }
type BirdLog struct {
	Type       string   // "file", "fixed", "syslog", "stderr", "udp"
	Path       string   // file path, syslog name, or UDP address
	Size       int      // rotation limit in bytes (file) or ring buffer size (fixed)
	BackupPath string   // backup file (file mode with rotation)
	Port       int      // UDP port
	Classes    []string // "all" or {"debug", "trace", "info", "remote", "auth", "warning", "error", "bug", "fatal"}
}

// ParseOptions configures the separators used when parsing a bird log spec.
type ParseOptions struct {
	ArgumentSeparator string // separates fields (default ":")
	ClassArgSeparator string // separates classes (default ",")
}

// ParseOption is a functional option for ParseBirdLog.
type ParseOption func(*ParseOptions)

// WithArgumentSeparator sets the field separator.
func WithArgumentSeparator(sep string) ParseOption {
	return func(o *ParseOptions) { o.ArgumentSeparator = sep }
}

// WithClassArgSeparator sets the class list separator.
func WithClassArgSeparator(sep string) ParseOption {
	return func(o *ParseOptions) { o.ClassArgSeparator = sep }
}

func defaultParseOptions() ParseOptions {
	return ParseOptions{ArgumentSeparator: ":", ClassArgSeparator: ","}
}

// ParseBirdLog parses a log specification.
// Formats (using default ":" separator):
//
//	stderr:classes
//	file:path:classes
//	file:path:size:backup:classes
//	fixed:path:size:classes
//	syslog:name:classes
//	udp:address:port:classes
//	udp:[ipv6]:port:classes
//
// Classes are separated by the class arg separator (default ","), e.g. "info,warning,error" or "all".
func ParseBirdLog(s string, opts ...ParseOption) (BirdLog, error) {
	o := defaultParseOptions()
	for _, fn := range opts {
		fn(&o)
	}

	parts := strings.Split(s, o.ArgumentSeparator)
	if len(parts) < 2 {
		return BirdLog{}, fmt.Errorf("invalid bird log spec %q: need at least type%sclasses", s, o.ArgumentSeparator)
	}

	typ := parts[0]
	switch typ {
	case "stderr":
		return BirdLog{Type: typ, Classes: parseClasses(parts[1], o.ClassArgSeparator)}, nil

	case "file":
		switch len(parts) {
		case 3:
			return BirdLog{Type: typ, Path: parts[1], Classes: parseClasses(parts[2], o.ClassArgSeparator)}, nil
		case 5:
			size, err := strconv.Atoi(parts[2])
			if err != nil {
				return BirdLog{}, fmt.Errorf("invalid size %q: %w", parts[2], err)
			}
			return BirdLog{Type: typ, Path: parts[1], Size: size, BackupPath: parts[3], Classes: parseClasses(parts[4], o.ClassArgSeparator)}, nil
		default:
			return BirdLog{}, fmt.Errorf("invalid file log spec %q", s)
		}

	case "fixed":
		if len(parts) != 4 {
			return BirdLog{}, fmt.Errorf("invalid fixed log spec %q", s)
		}
		size, err := strconv.Atoi(parts[2])
		if err != nil {
			return BirdLog{}, fmt.Errorf("invalid size %q: %w", parts[2], err)
		}
		return BirdLog{Type: typ, Path: parts[1], Size: size, Classes: parseClasses(parts[3], o.ClassArgSeparator)}, nil

	case "syslog":
		if len(parts) != 3 {
			return BirdLog{}, fmt.Errorf("invalid syslog log spec %q", s)
		}
		return BirdLog{Type: typ, Path: parts[1], Classes: parseClasses(parts[2], o.ClassArgSeparator)}, nil

	case "udp":
		// Support bracketed IPv6: udp:[addr]:port:classes
		rest := s[len("udp"+o.ArgumentSeparator):]
		var addr, remainder string
		if strings.HasPrefix(rest, "[") {
			closing := strings.Index(rest, "]")
			if closing == -1 {
				return BirdLog{}, fmt.Errorf("invalid udp log spec %q: missing closing bracket", s)
			}
			addr = rest[1:closing]
			remainder = rest[closing+1:]
			if !strings.HasPrefix(remainder, o.ArgumentSeparator) {
				return BirdLog{}, fmt.Errorf("invalid udp log spec %q: expected %q after ']'", s, o.ArgumentSeparator)
			}
			remainder = remainder[len(o.ArgumentSeparator):]
		} else {
			idx := strings.Index(rest, o.ArgumentSeparator)
			if idx == -1 {
				return BirdLog{}, fmt.Errorf("invalid udp log spec %q", s)
			}
			addr = rest[:idx]
			remainder = rest[idx+len(o.ArgumentSeparator):]
		}
		remParts := strings.SplitN(remainder, o.ArgumentSeparator, 2)
		if len(remParts) != 2 {
			return BirdLog{}, fmt.Errorf("invalid udp log spec %q", s)
		}
		port, err := strconv.Atoi(remParts[0])
		if err != nil {
			return BirdLog{}, fmt.Errorf("invalid port %q: %w", remParts[0], err)
		}
		return BirdLog{Type: "udp", Path: addr, Port: port, Classes: parseClasses(remParts[1], o.ClassArgSeparator)}, nil

	default:
		return BirdLog{}, fmt.Errorf("unknown log type %q", typ)
	}
}

// parseClasses splits a class list by the given separator.
// Duplicates are removed.
func parseClasses(s string, sep string) []string {
	var classes []string
	for c := range strings.SplitSeq(s, sep) {
		c = strings.TrimSpace(c)
		if c != "" {
			classes = append(classes, c)
		}
	}
	slices.Sort(classes)
	return slices.Compact(classes)
}

// fmtClasses formats classes for BIRD config syntax.
// Returns "all" if the list contains "all", otherwise "{ info, warning, ... }".
func fmtClasses(classes []string) string {
	if slices.Contains(classes, "all") {
		return "all"
	}
	return "{ " + strings.Join(classes, ", ") + " }"
}

// FmtParams returns the BIRD config line for this log destination.
func (l BirdLog) FmtParams() string {
	classes := fmtClasses(l.Classes)

	switch l.Type {
	case "stderr":
		return fmt.Sprintf("log stderr %s;", classes)
	case "file":
		if l.Size > 0 {
			return fmt.Sprintf("log %q %d %q %s;", l.Path, l.Size, l.BackupPath, classes)
		}
		return fmt.Sprintf("log %q %s;", l.Path, classes)
	case "fixed":
		return fmt.Sprintf("log fixed %q %d %s;", l.Path, l.Size, classes)
	case "syslog":
		return fmt.Sprintf("log syslog name %q %s;", l.Path, classes)
	case "udp":
		return fmt.Sprintf("log udp %s port %d %s;", l.Path, l.Port, classes)
	default:
		return ""
	}
}

// BirdLogParams implements pflag.Value for repeatable --bird-log flags.
type BirdLogParams []BirdLog

func (l *BirdLogParams) String() string {
	return fmt.Sprint(*l)
}

func (l *BirdLogParams) Type() string {
	return "birdlog"
}

func (l *BirdLogParams) Set(val string) error {
	log, err := ParseBirdLog(val)
	if err != nil {
		return err
	}
	*l = append(*l, log)
	return nil
}
