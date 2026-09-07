# Use the published MP-SPDZ v0.4.3 image in an existing Carbyne Stack deployment

This guide is for an operator who already has Carbyne Stack deployed and wants
to run its existing Ephemeral service with the published MP-SPDZ upgrade. It
does **not** build MP-SPDZ, publish an image, alter input secrets, or recreate
the rest of the stack.

The published Ephemeral image already includes the portable MP-SPDZ runtime.
Deploying the Ephemeral image is therefore sufficient; the separate MP-SPDZ
base image is only a build dependency.

## Published release identity

| Item | Value |
| --- | --- |
| Ephemeral image | `ghcr.io/lucy-sha512/ephemeral-mpspdz-test/ephemeral:v0.4.3-test-r11` |
| Ephemeral image digest | `sha256:9d1bde2c5d8ac1c793617040027b71612bb6faceb3515f47ba474283cd08c753` |
| Embedded MP-SPDZ release | `v0.4.3` |
| Embedded MP-SPDZ commit | `26a605368e40fed3a7e9cee78c9a3f4390b85eb5` |
| MP-SPDZ base used at build time | `ghcr.io/lucy-sha512/carbynestack-spdz-test:v0.4.3-portable-r3` |

MP-SPDZ `v0.4.3` was the latest upstream release when this image was
validated. Confirm the current upstream release before treating it as “latest”:
<https://github.com/data61/MP-SPDZ/releases>.

## Scope and prerequisites

This procedure assumes:

- The existing Helm release is named `cs-ephemeral` in namespace `default`.
- Each MPC party has its own Kubernetes context. The examples use
  `kind-apollo` and `kind-starbuck`.
- The published GHCR package is public. If your deployment blocks public
  registries, allow `ghcr.io` or mirror the **application image** to your own
  registry first.
- Your cluster is `linux/amd64` and every runtime node supports `SSE4.1`,
  `SSE4.2`, `AES`, `PCLMULQDQ`, `AVX`, `AVX2`, `BMI2`, and `ADX`.

The image is portable across x86_64 CPUs with that explicit feature baseline;
it is not an ARM64 image and it should not be deployed to older x86 CPUs that
lack those instructions.

Before upgrading, verify the package can be pulled:

```bash
docker pull ghcr.io/lucy-sha512/ephemeral-mpspdz-test/ephemeral:v0.4.3-test-r11
```

If this fails with `denied`, your cluster or local Docker installation needs
GHCR pull permission. For a private mirror, configure the chart's
`ephemeral.image.pullSecrets` value with a Kubernetes registry secret.

## 1. Record the current state

Do this for every party before changing anything. It provides a straightforward
rollback point and records the existing SPDZ configuration.

```bash
helm --kube-context kind-apollo get values cs-ephemeral \
  --namespace default --all --output yaml > cs-ephemeral-apollo-before-r11.yaml

helm --kube-context kind-starbuck get values cs-ephemeral \
  --namespace default --all --output yaml > cs-ephemeral-starbuck-before-r11.yaml

helm --kube-context kind-apollo history cs-ephemeral --namespace default
helm --kube-context kind-starbuck history cs-ephemeral --namespace default
```

Also confirm the release and service names if your installation differs from
the defaults:

```bash
helm --kube-context kind-apollo list --namespace default
kubectl --context kind-apollo get ksvc --namespace default
```

The upgrade must preserve the existing per-party values such as `playerId`,
`frontendUrl`, TLS secret, discovery configuration, prime, MAC keys, and GF(2^n)
parameters. Only the Ephemeral application image changes.

## 2. Upgrade every MPC party to r11

Run the command once for every party. The repository includes
[`charts/ephemeral/values-mp-spdz-r11.yaml`](../charts/ephemeral/values-mp-spdz-r11.yaml),
which contains only the published r11 image settings. Replace the chart path
with the location of your local Ephemeral chart, if necessary.

```bash
helm --kube-context kind-apollo upgrade cs-ephemeral ./charts/ephemeral \
  --namespace default \
  --reuse-values \
  --values charts/ephemeral/values-mp-spdz-r11.yaml

helm --kube-context kind-starbuck upgrade cs-ephemeral ./charts/ephemeral \
  --namespace default \
  --reuse-values \
  --values charts/ephemeral/values-mp-spdz-r11.yaml
```

`--reuse-values` is essential: it prevents the command from replacing your
party-specific Carbyne Stack configuration with chart defaults.

Upgrade all parties to the same r11 image before launching an MPC computation.
Do not run a mixed set of old and new MP-SPDZ images.

## 3. Wait for the Knative revisions

Each Helm upgrade creates a new Knative revision. Wait until the Ephemeral
service reports `READY=True`:

```bash
kubectl --context kind-apollo get ksvc ephemeral-generic --namespace default
kubectl --context kind-starbuck get ksvc ephemeral-generic --namespace default
```

Save the current revision names because the commands below refer to them:

```bash
kubectl --context kind-apollo get ksvc ephemeral-generic --namespace default \
  -o jsonpath='{.status.latestReadyRevisionName}{"\n"}'

kubectl --context kind-starbuck get ksvc ephemeral-generic --namespace default \
  -o jsonpath='{.status.latestReadyRevisionName}{"\n"}'
```

If Helm times out while the new revision pods are `Running` and the revision is
`Ready=True`, inspect the Knative route/load-balancer condition before rolling
back. See [Troubleshooting](#troubleshooting).

## 4. Prove the deployed image and MP-SPDZ version

First verify the image digest used by the live pod. Replace `00005` with the
revision returned in the previous step.

```bash
kubectl --context kind-apollo get pod --namespace default \
  -l serving.knative.dev/revision=ephemeral-generic-00005 \
  -o jsonpath='{.items[0].status.containerStatuses[?(@.name=="ephemeral-ephemeral")].imageID}{"\n"}'

kubectl --context kind-starbuck get pod --namespace default \
  -l serving.knative.dev/revision=ephemeral-generic-00005 \
  -o jsonpath='{.items[0].status.containerStatuses[?(@.name=="ephemeral-ephemeral")].imageID}{"\n"}'
```

Both values must end in:

```text
@sha256:9d1bde2c5d8ac1c793617040027b71612bb6faceb3515f47ba474283cd08c753
```

Then read the provenance file inside the exact container that executes MPC:

```bash
kubectl --context kind-apollo exec --namespace default \
  -c ephemeral-ephemeral deploy/ephemeral-generic-00005-deployment \
  -- cat /mp-spdz/MP-SPDZ_VERSION

kubectl --context kind-starbuck exec --namespace default \
  -c ephemeral-ephemeral deploy/ephemeral-generic-00005-deployment \
  -- cat /mp-spdz/MP-SPDZ_VERSION
```

Expected output on every party:

```text
version=v0.4.3
commit=26a605368e40fed3a7e9cee78c9a3f4390b85eb5
```

This two-part check is the proof that (1) the running pod uses the published
r11 application image and (2) that image embeds the intended MP-SPDZ release
commit.

## 5. Run a small end-to-end check

Submit a known two-party program with your existing Amphora input-secret IDs.
For example, the Millionaires program returns `1` when the first input is less
than the second:

```bash
java -jar cs.jar ephemeral execute --timeout 600 \
  --input <first-input-secret-id> \
  --input <second-input-secret-id> \
  ephemeral-generic.default < billionaires.mpc
```

The command returns a game ID. Fetch the result secret:

```bash
java -jar cs.jar amphora get-secret <game-id>
```

Check both party logs for successful execution:

```bash
kubectl --context kind-apollo logs --namespace default \
  deploy/ephemeral-generic-00005-deployment -c ephemeral-ephemeral \
  | rg '<game-id>|Computation finished|Activation finished successful'
```

## Tuple-header verification

Header length is determined by the protocol and field configuration, not just
the MP-SPDZ release number. In the r11 validation with `2-p-128` and `2-2-40`,
the runtime generated:

| Protocol | Header size |
| --- | --- |
| `SPDZ gfp` | 57 bytes |
| `SPDZ gf2n_` | 38 bytes |

Confirm your deployment's generated header sizes from the Ephemeral logs:

```bash
kubectl --context kind-apollo logs --namespace default \
  deploy/ephemeral-generic-00005-deployment -c ephemeral-ephemeral \
  | rg 'Generated tuple file header'
```

If your prime, GF(2^n) parameters, or MP-SPDZ protocol differ, the expected
header size can differ as well. Never assume a fixed value such as 59 bytes
without calculating it from the active configuration.

## Roll back

If the new revision cannot become ready or the end-to-end test fails, roll back
the Helm release on every party to the previously recorded revision:

```bash
helm --kube-context kind-apollo history cs-ephemeral --namespace default
helm --kube-context kind-apollo rollback cs-ephemeral <previous-revision> --namespace default

helm --kube-context kind-starbuck history cs-ephemeral --namespace default
helm --kube-context kind-starbuck rollback cs-ephemeral <previous-revision> --namespace default
```

Use the same rollback revision for every party when those parties were upgraded
together. After rollback, verify `LATESTREADY` again before starting a new MPC
execution.

## Troubleshooting

### `Illegal instruction (core dumped)`

The node CPU is missing an instruction required by the r11 portable x86_64
baseline. Do not retry the computation. Build or obtain an MP-SPDZ image for a
lower, tested CPU baseline and deploy that image to every party.

### `ImagePullBackOff` or `denied: denied`

Check that the full application image name is used exactly as shown above and
that your nodes can reach `ghcr.io`. If policy requires an authenticated
registry, mirror the application image and set `ephemeral.image.registry`,
`ephemeral.image.repository`, `ephemeral.image.tag`, and
`ephemeral.image.pullSecrets` to the mirrored registry values.

### Istio-injected pods fail with a `raw` iptables-table error

On a Linux host running Kind, load the kernel modules required by Istio before
restarting the workloads:

```bash
sudo modprobe ip_tables
sudo modprobe iptable_raw
```

To restore this automatically on reboot:

```bash
printf 'ip_tables\niptable_raw\n' | sudo tee /etc/modules-load.d/carbynestack-iptables.conf
```

### Knative waits for a load balancer or the HTTPS VCP endpoint refuses connections

Inspect the party's Knative ingress Gateway. Its HTTPS server must include a
party TLS secret, for example:

```yaml
tls:
  mode: SIMPLE
  credentialName: apollo-tls-secret-generic
```

```bash
kubectl --context kind-apollo get gateway knative-ingress-gateway \
  --namespace knative-serving -o yaml
```

If your Knative operator removes the TLS configuration during reconciliation,
make that patch part of your cluster bootstrap or post-deployment process. This
is an ingress configuration issue; it is unrelated to the r11 MP-SPDZ image.
