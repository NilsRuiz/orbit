package config

import (
	"fmt"
	"regexp"
	"strings"
)

// LabelPrefix is the only label prefix the operator manages on EdgeNode objects (ADR-0007).
// Other labels belong to humans, so the agent refuses them rather than having them ignored.
const LabelPrefix = "orbit.io/"

// Kubernetes naming rules, reimplemented to keep k8s.io/apimachinery out of the agent:
// label names and values (k8s.io/apimachinery/pkg/util/validation, IsQualifiedName and
// IsValidLabelValue) and DNS-1123 labels (object names, IsDNS1123Label).
var (
	labelSegment = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

const maxSegmentLen = 63

// ParseNodeLabels parses ORBIT_NODE_LABELS ("orbit.io/a=x,orbit.io/b=y"). Every key must
// use LabelPrefix; names and values follow Kubernetes label syntax.
func ParseNodeLabels(s string) (map[string]string, error) {
	labels := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("%q: want key=value", pair)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		name, ok := strings.CutPrefix(key, LabelPrefix)
		if !ok {
			return nil, fmt.Errorf("label %q: key must start with %s", key, LabelPrefix)
		}
		if len(name) > maxSegmentLen || !labelSegment.MatchString(name) {
			return nil, fmt.Errorf("label %q: invalid label name %q", key, name)
		}
		if value != "" && (len(value) > maxSegmentLen || !labelSegment.MatchString(value)) {
			return nil, fmt.Errorf("label %q: invalid label value %q", key, value)
		}
		if _, dup := labels[key]; dup {
			return nil, fmt.Errorf("duplicate label %q", key)
		}
		labels[key] = value
	}
	return labels, nil
}

// ValidateNodeID checks that the node ID can name an EdgeNode object (DNS-1123 label) and
// is a single NATS subject token (no dot, no wildcard).
func ValidateNodeID(id string) error {
	if len(id) > maxSegmentLen || !dns1123Label.MatchString(id) {
		return fmt.Errorf("%q must be a DNS-1123 label: lowercase alphanumerics and '-', at most %d characters, starting and ending with an alphanumeric", id, maxSegmentLen)
	}
	return nil
}
