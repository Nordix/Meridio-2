# Webhook Testing Guide

## Unit Tests

Run the webhook unit tests:

```bash
make test
```

Or run webhook tests specifically:

```bash
go test ./internal/webhook/v1alpha1/... -v
```

## Manual Testing in Cluster

### Prerequisites

1. Install cert-manager:
```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml
kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager
kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager-webhook
kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager-cainjector
```

2. Deploy the operator:
```bash
make deploy
```

### Test Cases

**Note**: All examples include the required `parentRefs` and `backendRefs` fields. The Gateway and CustomService referenced don't need to exist for webhook validation to work.

#### 1. Valid L34Route (should succeed)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: valid-route
  namespace: default
spec:
  parentRefs:
    - name: example-gateway
      namespace: default
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  destinationCIDRs:
    - "192.168.1.1/32"
  sourceCIDRs:
    - "10.0.0.0/24"
  sourcePorts:
    - "any"
  destinationPorts:
    - "80"
    - "443"
  protocols:
    - TCP
    - UDP
  priority: 100
EOF
```

#### 1b. Valid L34Route with multiple ports and byteMatches (should succeed)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: vip-20-0-0-1-multi-ports-a
  namespace: default
spec:
  parentRefs:
    - name: sllb-a
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  priority: 10
  destinationCIDRs:
    - "20.0.0.1/32"
  sourceCIDRs:
    - "0.0.0.0/0"
  sourcePorts:
    - "0-65535"
  destinationPorts:
    - "5000"
    - "5001"
  protocols:
    - TCP
  byteMatches:
    - "tcp[0:4] & 0x0000fffe = 5000"
EOF
```

#### 2. Duplicate Protocols (should fail - CEL validation)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: duplicate-protocols
  namespace: default
spec:
  parentRefs:
    - name: example-gateway
      namespace: default
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  destinationCIDRs:
    - "192.168.1.1/32"
  protocols:
    - TCP
    - TCP
  priority: 1
EOF
```

Expected error: `protocols must not contain duplicates`

#### 3. Overlapping Source CIDRs - IPv4 (should fail - webhook validation)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: overlapping-source-cidrs-ipv4
  namespace: default
spec:
  parentRefs:
    - name: example-gateway
      namespace: default
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  destinationCIDRs:
    - "192.168.1.1/32"
  sourceCIDRs:
    - "192.168.1.0/24"
    - "192.168.1.0/25"
  protocols:
    - TCP
  priority: 1
EOF
```

Expected error: `overlapping CIDR`

#### 4. Overlapping Source CIDRs - IPv6 (should fail - webhook validation)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: overlapping-source-cidrs-ipv6
  namespace: default
spec:
  parentRefs:
    - name: example-gateway
      namespace: default
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  destinationCIDRs:
    - "2001:db8::1/128"
  sourceCIDRs:
    - "2001:db8::/32"
    - "2001:db8::/48"
  protocols:
    - TCP
  priority: 1
EOF
```

Expected error: `overlapping CIDR`

#### 5. Overlapping Destination CIDRs - Identical (should fail - webhook validation)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: overlapping-dest-cidrs
  namespace: default
spec:
  parentRefs:
    - name: example-gateway
      namespace: default
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  destinationCIDRs:
    - "192.168.1.1/32"
    - "192.168.1.1/32"
  protocols:
    - TCP
  priority: 1
EOF
```

Expected error: `overlapping CIDR`

#### 6. Overlapping Source Ports (should fail - webhook validation)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: overlapping-source-ports
  namespace: default
spec:
  parentRefs:
    - name: example-gateway
      namespace: default
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  destinationCIDRs:
    - "192.168.1.1/32"
  sourcePorts:
    - "8080-8090"
    - "8085-8095"
  protocols:
    - TCP
  priority: 1
EOF
```

Expected error: `overlapping ports`

#### 7. Overlapping Destination Ports (should fail - webhook validation)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: overlapping-dest-ports
  namespace: default
spec:
  parentRefs:
    - name: example-gateway
      namespace: default
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  destinationCIDRs:
    - "192.168.1.1/32"
  destinationPorts:
    - "80"
    - "80-100"
  protocols:
    - TCP
  priority: 1
EOF
```

Expected error: `overlapping ports`

#### 8. Invalid Destination CIDR prefix (should fail - CEL validation)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: invalid-dest-cidr-prefix
  namespace: default
spec:
  parentRefs:
    - name: example-gateway
      namespace: default
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  destinationCIDRs:
    - "192.168.1.0/24"
  protocols:
    - TCP
  priority: 1
EOF
```

Expected error: `each destinationCIDR must be an IPv4/32 or IPv6/128 CIDR`

#### 9. Missing required field parentRefs (should fail - CEL validation)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: missing-parent-refs
  namespace: default
spec:
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  destinationCIDRs:
    - "192.168.1.1/32"
  protocols:
    - TCP
  priority: 1
EOF
```

Expected error: `spec.parentRefs: Required value`

#### 10. Invalid port range (should fail - CEL validation)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: invalid-port-range
  namespace: default
spec:
  parentRefs:
    - name: example-gateway
      namespace: default
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  destinationCIDRs:
    - "192.168.1.1/32"
  destinationPorts:
    - "9000-8000"
  protocols:
    - TCP
  priority: 1
EOF
```

Expected error: `each destinationPort must be a single port, a port range, or 'any'`

#### 11. Mismatched IP families - IPv4 source with IPv6 destination (should fail - webhook validation)

```bash
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: mismatched-ip-families
  namespace: default
spec:
  parentRefs:
    - name: example-gateway
      namespace: default
  backendRefs:
    - name: service-a
      group: meridio-2.nordix.org
      kind: CustomService
  sourceCIDRs:
    - "10.0.0.0/24"
  destinationCIDRs:
    - "2001:db8::1/128"
  protocols:
    - TCP
  priority: 1
EOF
```

Expected error: `source and destination CIDRs must be of the same IP family`

### Verify Webhook Logs

Check webhook validation logs:

```bash
kubectl logs -n meridio-2-system deployment/meridio-2-controller-manager -c manager -f | grep "l34route-resource"
```

### Cleanup

```bash
kubectl delete l34route --all
make undeploy
```

## Test Coverage

The webhook validates:

- ✅ IP family consistency (source and destination must be IPv4, IPv6, or dual-stack consistently)
- ✅ Source CIDR overlaps (IPv4 and IPv6)
- ✅ Destination CIDR overlaps (IPv4 /32 and IPv6 /128 only)
- ✅ Source port overlaps
- ✅ Destination port overlaps

CEL validation (in CRD) handles:

- ✅ Required fields (parentRefs, destinationCIDRs, protocols, priority)
- ✅ Protocol uniqueness (no duplicates)
- ✅ CIDR format validation
- ✅ Destination CIDR prefix length (/32 for IPv4, /128 for IPv6)
- ✅ Port format (single port, range, "any")
- ✅ Port range validity (start <= end, 0-65535)
- ✅ ByteMatch format
- ✅ Array size limits

**Note**: 
- Destination CIDRs are restricted to /32 (IPv4) and /128 (IPv6) by CEL validation rules in the CRD.
- Source and destination CIDRs must have consistent IP families:
  - Both IPv4-only
  - Both IPv6-only
  - Both dual-stack (containing both IPv4 and IPv6)

## Troubleshooting

### Webhook not called

If validation doesn't occur, check:

1. Webhook configuration exists:
```bash
kubectl get validatingwebhookconfigurations | grep meridio-2
kubectl get mutatingwebhookconfigurations | grep meridio-2
```

2. Webhook service is running:
```bash
kubectl get svc -n meridio-2-system meridio-2-webhook-service
```

3. Certificate is ready:
```bash
kubectl get certificate -n meridio-2-system
```

### Certificate issues

If you see certificate errors:

```bash
# Check cert-manager is running
kubectl get pods -n cert-manager

# Check certificate status
kubectl describe certificate -n meridio-2-system meridio-2-serving-cert

# Check issuer
kubectl describe issuer -n meridio-2-system
```
