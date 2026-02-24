# Router Testing on Kind - Step by Step Guide

This guide provides copy-paste commands to test the Meridio-2 router on a Kind cluster.

## Prerequisites

- Docker installed
- Kind installed
- kubectl installed
- Clone this repository and navigate to the project root

## Step 1: Create Kind Cluster

```bash
kind create cluster --name meridio-test
```

Kind automatically configures kubectl to use the new cluster.

## Step 2: Install CNI plugins

```bash
kubectl apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/master/e2e/templates/cni-install.yml.j2
```

## Step 3: Install Multus CNI

```bash
kubectl apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/master/deployments/multus-daemonset.yml
```

Wait for Multus to be ready:

```bash
kubectl wait --for=condition=ready pod -l app=multus -n kube-system --timeout=300s
```

## Step 4: Install Whereabouts IPAM

```bash
kubectl apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/whereabouts/master/doc/crds/daemonset-install.yaml
kubectl apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/whereabouts/master/doc/crds/whereabouts.cni.cncf.io_ippools.yaml
kubectl apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/whereabouts/master/doc/crds/whereabouts.cni.cncf.io_overlappingrangeipreservations.yaml
```

## Step 5: Install Gateway API CRDs

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml
```

## Step 6: Install Meridio-2 CRDs, RBACs and create namespace

Note: it does not matter if other components like meridio-2-controller-manager will be hanging

```bash
NAMESPACE=meridio-2-system make deploy
```

## Step 7: Build and Load Router Image

```bash
# Build router image
make router

# Load into Kind cluster
kind load docker-image localhost:5001/router:latest --name meridio-test
```

## Step 8: Build and Start VPN Gateway (External BGP Peer)

The VPN gateway simulates an external BGP router:

```bash
cd hack/vpn-gateway

# Build the VPN gateway image
docker compose build

# Start the VPN gateway
docker compose up -d

# Return to project root
cd ../..
```

Verify it's running:

```bash
docker exec vpn-gateway ip addr show vlan1
# Should show: 169.254.100.150/24 and fd00:100::150/64
```

## Step 9: Deploy Router Test Pod

```bash
kubectl apply -f test/examples/sample-router-test.yaml
```

Wait for pod to be ready:

```bash
kubectl wait --for=condition=Ready pod/router-test-pod -n meridio-2-system --timeout=120s
```

## Step 10: Patch Gateway to add VIPs to status.addresses

```bash
kubectl patch gateway gatewayrouter-sample -n meridio-2-system --subresource=status --type=merge -p '
{
  "status": {
    "addresses": [
      {"type": "IPAddress", "value": "40.0.0.1"},
      {"type": "IPAddress", "value": "40.0.0.2"},
      {"type": "IPAddress", "value": "40.0.0.3"},
      {"type": "IPAddress", "value": "2001:db8::1"},
      {"type": "IPAddress", "value": "2001:db8::2"}
    ]
  }
}'
```

## Step 11: Verify BGP Connectivity

Check router logs:

```bash
kubectl logs -n meridio-2-system router-test-pod -f
```

Look for:
```
Gateway connectivity established  status="2/2 protocols up"
```

Check BIRD status in router:

```bash
kubectl exec -n meridio-2-system router-test-pod -- birdc -s /run/bird/bird.ctl show protocols
```

Expected output:
```
Name       Proto      Table      State  Since         Info
NBR-gatewayrouter-sample BGP        ---        up     ...  Established
NBR-gatewayrouter-sample-v6 BGP     ---        up     ...  Established
```

## Step 12: Verify Routes

Check IPv4 routes:

```bash
kubectl exec -n meridio-2-system router-test-pod -- ip route show table 4096
```

Expected routes from VPN gateway:
- `100.64.0.0/24`
- `100.64.1.0/24`
- `100.64.2.0/24`
- `0.0.0.0/0` (default route)

Check IPv6 routes:

```bash
kubectl exec -n meridio-2-system router-test-pod -- ip -6 route show table 4096
```

Expected routes:
- `fd00:64::/64`
- `fd00:65::/64`
- `::/0` (default route)

## Step 13: Verify VIP Configuration

Check routing rules:

```bash
kubectl exec -n meridio-2-system router-test-pod -- ip rule show
```

Should see rules like:
```
100: from 40.0.0.1 lookup 4096
100: from 40.0.0.2 lookup 4096
100: from 40.0.0.3 lookup 4096
100: from 2001:db8::1 lookup 4096
100: from 2001:db8::2 lookup 4096
```

## Step 14: Test VIP Traffic (Optional)

VIPs should already be assigned to loopback by the init container. Verify:

```bash
kubectl exec -n meridio-2-system router-test-pod -- ip addr show lo
```

Start ctraffic server on VIP:

```bash
kubectl exec -n meridio-2-system router-test-pod -- /opt/ctraffic -server -address=40.0.0.1:15000
```

In another terminal, test from VPN gateway:

```bash
docker exec vpn-gateway /opt/ctraffic -address=40.0.0.1:15000 -nconn=1 -rate=100
```

## Debug Commands

### Check VPN Gateway BGP Status

```bash
docker exec vpn-gateway birdc show protocols
```

Expected:
```
router1     BGP      ---        up     ...  Established
router1_v6  BGP      ---        up     ...  Established
```

### Check VPN Gateway Routes

```bash
docker exec vpn-gateway birdc show route
```

### Ping Test from Router to VPN Gateway

```bash
# IPv4
kubectl exec -n meridio-2-system router-test-pod -- ping -c 3 169.254.100.150

# IPv6
kubectl exec -n meridio-2-system router-test-pod -- ping -c 3 fd00:100::150
```

### Check VLAN Interface

```bash
kubectl exec -n meridio-2-system router-test-pod -- ip addr show vlan-100
```

Should show:
```
169.254.100.1/24
fd00:100::1/64
```

### Packet Capture

```bash
kubectl exec -n meridio-2-system router-test-pod -- tcpdump -i vlan-100 -n
```

## Cleanup

Stop VPN gateway:

```bash
cd hack/vpn-gateway
docker compose down
cd ../..
```

Delete router test pod:

```bash
kubectl delete -f test/examples/sample-router-test.yaml
```

Delete Kind cluster:

```bash
kind delete cluster --name meridio-test
```

## Troubleshooting

### BGP Not Establishing

1. Check VLAN interface exists:
   ```bash
   kubectl exec -n meridio-2-system router-test-pod -- ip link show vlan-100
   ```

2. Check connectivity to VPN gateway:
   ```bash
   kubectl exec -n meridio-2-system router-test-pod -- ping -c 3 169.254.100.150
   ```

3. Check BIRD config:
   ```bash
   kubectl exec -n meridio-2-system router-test-pod -- cat /etc/bird/bird.conf
   ```

### No Routes Received

1. Check VPN gateway is advertising routes:
   ```bash
   docker exec vpn-gateway birdc show route
   ```

2. Check BGP session details:
   ```bash
   kubectl exec -n meridio-2-system router-test-pod -- birdc show protocols all NBR-gatewayrouter-sample
   ```

### Pod Not Starting

1. Check pod events:
   ```bash
   kubectl describe pod router-test-pod -n meridio-2-system
   ```

2. Check if image is loaded:
   ```bash
   docker exec meridio-test-control-plane crictl images | grep router
   ```

3. Reload image if needed:
   ```bash
   kind load docker-image localhost:5001/router:latest --name meridio-test
   ```

## Configuration Details

### VPN Gateway BGP Settings
- ASN: 4248829953
- IPv4: 169.254.100.150:10179
- IPv6: fd00:100::150:10179
- Hold time: 24s
- BFD: enabled (300ms intervals, multiplier 3)

### Router BGP Settings
- ASN: 8103
- IPv4: 169.254.100.1:10179
- IPv6: fd00:100::1:10179
- Hold time: 24s
- BFD: enabled (300ms intervals, multiplier 3)

### Routing Tables
- **Table 4096**: BIRD-managed BGP routes
- **Table 4097**: Blackhole fallback routes
- **Rule Priority 100**: VIP → table 4096
- **Rule Priority 101**: VIP → table 4097 (fallback)

### VIPs Configured
- 40.0.0.1/32
- 40.0.0.2/32
- 40.0.0.3/32
- 2001:db8::1/128
- 2001:db8::2/128
