# Running the latest MP-SPDZ release with Carbyne Stack Ephemeral

This guide builds an MP-SPDZ base image that can run on the Kubernetes machines
where Carbyne Stack Ephemeral executes MPC programs. It then packages Ephemeral
with that base image, deploys it to every party, and proves the exact upstream
version used for a computation.

It is deliberately **release-pinned**. Do not build from `master` and call it
"latest": it prevents later verification and can silently change the tuple-file
format or CPU requirements. Instead, select the latest upstream release tag,
resolve its immutable commit, and include both values in the image.

The maintained release list is at <https://github.com/data61/MP-SPDZ/releases>.
At the time this guide was validated, the latest release was `v0.4.3` at commit
`26a605368e40fed3a7e9cee78c9a3f4390b85eb5`.

## What this workflow produces

The workflow creates two images:

| Image | Purpose |
| --- | --- |
| `ghcr.io/<owner>/carbynestack-spdz:<release>-portable` | MP-SPDZ runtime, compiler, and a version proof file |
| `ghcr.io/<owner>/<ephemeral-package>/ephemeral:<release>-rN` | Ephemeral application layered on the portable MP-SPDZ image |

Every runtime base image contains `/mp-spdz/MP-SPDZ_VERSION`:

```text
version=v0.4.3
commit=26a605368e40fed3a7e9cee78c9a3f4390b85eb5
```

This is the artifact to inspect in the live pod after a deployment.

## Important portability rule

Never compile MP-SPDZ using `-march=native` for a container that will run on a
different machine. The compiler can emit instructions supported by the build
CPU but absent on a Kind node or production worker, producing:

```text
Illegal instruction (core dumped)
```

The supplied [portable Dockerfile](Dockerfile.mp-spdz-portable) uses an explicit
x86-64 feature baseline:

```text
-msse4.1 -msse4.2 -maes -mpclmul -mavx -mavx2 -mbmi2 -madx
```

It is portable across x86_64 machines that support all of those features. Check
the **runtime node**, not just the build machine:

```bash
lscpu | grep -i '^Flags'
```

The output must contain `sse4_1`, `sse4_2`, `aes`, `pclmulqdq`, `avx`, `avx2`,
`bmi2`, and `adx`. For an older CPU or another architecture, build a separate
image variant with an appropriate, tested `MP_SPDZ_ARCH_FLAGS` baseline. Do not
assume that an x86_64 image is portable to ARM64.

## Prerequisites

- Docker Engine with permission to use the Docker socket
- `kubectl`, `helm`, and `ko` installed on the deployment machine
- Access to every Kubernetes context representing an MPC party
- A public or pull-authorized container registry
- A GitHub classic personal access token with `write:packages` if using GHCR
- A Carbyne Stack deployment with Ephemeral, Amphora, Castor, Discovery, Istio,
  Knative, and TLS already installed

For GHCR, authenticate without adding the token to shell history:

```bash
read -r -s CR_PAT
printf '%s' "$CR_PAT" | docker login ghcr.io -u <github-user> --password-stdin
unset CR_PAT
```

Never paste a registry token into a terminal command, source file, or chat.

## 1. Select and pin the upstream release

Open the upstream releases page and choose the desired release tag. Clone that
tag and record the commit actually checked out:

```bash
git clone --depth 1 --branch v0.4.3 https://github.com/data61/MP-SPDZ.git /tmp/mp-spdz-v0.4.3
git -C /tmp/mp-spdz-v0.4.3 rev-parse HEAD
```

For `v0.4.3`, the command must print:

```text
26a605368e40fed3a7e9cee78c9a3f4390b85eb5
```

Use the output as `MP_SPDZ_REF`. When a newer release exists, replace both the
tag and commit everywhere below. The commit must correspond to that release
tag; this is the supply-chain identity of the engine.

## 2. Build the portable MP-SPDZ base image

From the Ephemeral repository root, build the supplied Dockerfile. Replace the
example repository and tag with your own registry namespace.

```bash
docker build \
  --build-arg MP_SPDZ_VERSION=v0.4.3 \
  --build-arg MP_SPDZ_REF=26a605368e40fed3a7e9cee78c9a3f4390b85eb5 \
  --tag ghcr.io/<owner>/carbynestack-spdz:v0.4.3-portable-r1 \
  --file docs/Dockerfile.mp-spdz-portable \
  .
```

Before publishing, inspect the embedded provenance and make sure the binary can
start on the local machine:

```bash
docker run --rm --entrypoint cat \
  ghcr.io/<owner>/carbynestack-spdz:v0.4.3-portable-r1 \
  /mp-spdz/MP-SPDZ_VERSION

docker run --rm --entrypoint sh \
  ghcr.io/<owner>/carbynestack-spdz:v0.4.3-portable-r1 \
  -c './Player-Online.x --help >/dev/null || test $? -eq 1'
```

The second command must not report `Illegal instruction` or missing shared
libraries. A non-zero help exit code is normal for some MP-SPDZ binaries.

Publish and record the immutable digest:

```bash
docker push ghcr.io/<owner>/carbynestack-spdz:v0.4.3-portable-r1

docker buildx imagetools inspect \
  ghcr.io/<owner>/carbynestack-spdz:v0.4.3-portable-r1 \
  --format '{{.Manifest.Digest}}'
```

Use the digest, not only the mutable tag, when layering Ephemeral.

## 3. Build the Ephemeral image against the base digest

Update `.ko.yaml` to point the Ephemeral package at the immutable base image:

```yaml
baseImageOverrides:
  github.com/carbynestack/ephemeral/cmd/ephemeral: ghcr.io/<owner>/carbynestack-spdz@sha256:<base-image-digest>
```

Do not replace the base image with a tag in the override. A digest guarantees
that all parties build and deploy against exactly the MP-SPDZ version verified
in the previous step.

Build and publish the Ephemeral application image:

```bash
KO_DOCKER_REPO=ghcr.io/<owner>/<ephemeral-package> \
  ko build --platform=linux/amd64 --base-import-paths \
  --tags=v0.4.3-r1 ./cmd/ephemeral
```

Record the final application image digest emitted by `ko`. The tag is convenient
for Helm; the digest is the definitive deployment evidence.

## 4. Deploy the same application image to every party

Upgrade each party with exactly the same application tag or digest. This example
uses two local Kind contexts:

```bash
helm --kube-context kind-apollo upgrade cs-ephemeral ./charts/ephemeral \
  --namespace default --reuse-values \
  --set-string ephemeral.image.registry=ghcr.io \
  --set-string ephemeral.image.repository=<owner>/<ephemeral-package>/ephemeral \
  --set-string ephemeral.image.tag=v0.4.3-r1

helm --kube-context kind-starbuck upgrade cs-ephemeral ./charts/ephemeral \
  --namespace default --reuse-values \
  --set-string ephemeral.image.registry=ghcr.io \
  --set-string ephemeral.image.repository=<owner>/<ephemeral-package>/ephemeral \
  --set-string ephemeral.image.tag=v0.4.3-r1
```

If a public registry is not used, create a registry pull secret and configure
the chart to use it before deployment. Do not place registry credentials in
Helm values committed to Git.

## 5. Verify the live deployment before executing MPC

Wait for the Knative revision to become ready:

```bash
kubectl --context kind-apollo get ksvc ephemeral-generic -n default
kubectl --context kind-starbuck get ksvc ephemeral-generic -n default
```

Both must show a `LATESTREADY` revision and `READY=True`. Then verify the
runtime provenance from both actual Ephemeral containers:

```bash
kubectl --context kind-apollo exec -n default \
  -c ephemeral-ephemeral deploy/ephemeral-generic-00005-deployment \
  -- cat /mp-spdz/MP-SPDZ_VERSION

kubectl --context kind-starbuck exec -n default \
  -c ephemeral-ephemeral deploy/ephemeral-generic-00005-deployment \
  -- cat /mp-spdz/MP-SPDZ_VERSION
```

Replace `00005` with the revision reported by each `ksvc`. Both commands must
return the selected version and commit. Also check the exact deployed image:

```bash
kubectl --context kind-apollo get pod -n default \
  -l serving.knative.dev/revision=ephemeral-generic-00005 \
  -o jsonpath='{.items[0].status.containerStatuses[?(@.name=="ephemeral-ephemeral")].imageID}{"\n"}'
```

The `imageID` must match the digest emitted by `ko`.

## 6. Validate an MPC execution

Use an existing pair of Amphora input-secret IDs and submit an MPC program:

```bash
java -jar cs.jar ephemeral execute --timeout 600 \
  --input <party-0-input-secret-id> \
  --input <party-1-input-secret-id> \
  ephemeral-generic.default < billionaires.mpc
```

The command prints a game ID. Retrieve the result secret:

```bash
java -jar cs.jar amphora get-secret <game-id>
```

For the Millionaires comparison program, `[1]` means the first input is less
than the second; `[0]` means it is not.

Search the live Ephemeral logs by game ID to prove compilation and completion:

```bash
kubectl --context kind-apollo logs -n default \
  deploy/ephemeral-generic-00005-deployment -c ephemeral-ephemeral \
  | rg '<game-id>|Computation finished|Activation finished successful'
```

## Tuple-file header compatibility

Header length is not a universal MP-SPDZ version number. It depends on the
protocol and field configuration. For the validated configuration below:

| Runtime configuration | Tuple protocol | Header length |
| --- | --- | --- |
| `2-p-128` | `SPDZ gfp` | 57 bytes |
| `2-2-40` | `SPDZ gf2n_` | 38 bytes |

Do not hard-code a claim such as “all v0.4.x headers are 59 bytes.” Instead,
generate headers from the live prime, GF(2^n) bit length, storage size, and
protocol descriptor. The implementation does this in
`pkg/ephemeral/io/tuple_streamer.go` through `generateGfpHeader` and
`generateGf2nHeader`.

During a validation run, inspect the logs:

```bash
kubectl --context kind-apollo logs -n default \
  deploy/ephemeral-generic-00005-deployment -c ephemeral-ephemeral \
  | rg 'Generated tuple file header'
```

The logs should show the expected size for every streamed tuple type. A
successful MP-SPDZ execution is the final compatibility check.

## Troubleshooting

### `Illegal instruction (core dumped)`

The binary was built with instructions unsupported by the runtime node. Rebuild
the base image without `-march=native`, select a feature baseline supported by
all nodes, republish the base image, rebuild Ephemeral, and roll out a new
revision. Do not reuse the old application image because it still contains the
old base-image layers.

### Istio-injected pods fail with `iptables-restore: unable to initialize table 'raw'`

On Linux hosts running Kind, load the host kernel modules:

```bash
sudo modprobe ip_tables
sudo modprobe iptable_raw
```

To reload them after reboot:

```bash
printf 'ip_tables\niptable_raw\n' | sudo tee /etc/modules-load.d/carbynestack-iptables.conf
```

Then restart the affected deployments. Confirm the table is available inside a
Kind node:

```bash
docker exec <kind-control-plane-container> iptables-legacy -t raw -L -n
```

### Knative reports `Waiting for load balancer to be ready` or the VCP HTTPS endpoint refuses connections

First check the Istio ingress service and TLS configuration. The Knative ingress
Gateway must have a TLS server with the party-specific certificate secret:

```bash
kubectl --context <context> get gateway knative-ingress-gateway \
  -n knative-serving -o yaml
```

The server should contain:

```yaml
tls:
  mode: SIMPLE
  credentialName: <party>-tls-secret-generic
```

If an operator reconciliation removes this block, add a post-deployment patch
to the cluster bootstrap process. Reapply the TLS configuration after Knative
has reconciled; otherwise Istio omits the HTTPS listener and the VCP cannot
refresh tokens or accept Ephemeral requests.

### GHCR returns `denied: denied`

Use a GitHub **classic** personal access token with `write:packages`. A GitHub
password and fine-grained token will not authenticate the Docker CLI for this
workflow. Revoke any token that was pasted into a terminal command or chat.

## Upgrade and rollback discipline

1. Record the selected upstream tag, commit, base image digest, Ephemeral image
   digest, and the deployed Knative revision in the release notes.
2. Test the new image with a small two-party computation before production data.
3. Keep the previous image digest and Helm values to permit a rollback.
4. Upgrade all parties as one release. Mixed MP-SPDZ binaries or tuple formats
   are not a supported state.
5. Only call a newer engine “latest” after verifying the upstream release page
   and recording the immutable commit used to build it.
