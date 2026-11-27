VERSION       ?= 0.1.0
FUZZTIME      ?= 1m
ROUNDS        ?= 150
SEED          ?=
TALOS_VERSION ?= v1.15.0-beta.1
ARCH          ?= arm64
GO_IMAGE      ?= golang:1.26
OUT           ?= _out
# the VM of vm-test has the architecture of the machine it runs on
VM_ARCH       ?= $(if $(filter arm64 aarch64,$(shell uname -m)),arm64,amd64)

GO = docker run --rm -v "$(CURDIR)":/src -v routal-gocache:/root/.cache/go-build -v routal-gomod:/go/pkg/mod -w /src $(GO_IMAGE)

.PHONY: test check check-talos vm-test vm-upgrade-test cover schema fuzz integration clab extension installer push-installer image clean

test: ## run unit tests (in Docker)
	$(GO) sh -c 'test -z "$$(gofmt -l .)" && go vet ./... && go test ./...'

check: test ## also check every example with the real tools: nftables ruleset, daemon configs, talos.yaml
	docker build --platform linux/$(ARCH) --target daemons -t routal-daemons:$(ARCH) .
	docker run --rm --platform linux/$(ARCH) --cap-add NET_ADMIN -v "$(CURDIR)":/src:ro routal-daemons:$(ARCH) /src/hack/check-configs.sh
	hack/check-talos.sh $(TALOS_VERSION) $(OUT)/talos-check
	hack/check-personas.sh $(OUT) $(TALOS_VERSION)

check-personas: ## validate every persona of test/vm/personas (router.yaml and talos.yaml) without a VM
	hack/check-personas.sh $(OUT) $(TALOS_VERSION)

check-talos: ## validate every example's talos.yaml with talosctl of TALOS_VERSION (hack/talos-versions.sh lists the ones CI tests)
	hack/check-talos.sh $(TALOS_VERSION) $(OUT)/talos-check

schema: ## regenerate pkg/schema/router.schema.json from the types and comments of pkg/config
	$(GO) go test ./pkg/schema -run TestUpToDate -update

fuzz: ## fuzz the parsers and the config for FUZZTIME each; nft, Unbound, radvd and chrony check what accepted configs render
	docker build --platform linux/$(ARCH) --target daemons -t routal-daemons:$(ARCH) .
	mkdir -p $(OUT)/fuzz
	$(GO) sh -c 'for p in pkg/blocklist pkg/pd pkg/leases test/variants; do \
		CGO_ENABLED=0 GOARCH=$(ARCH) go test -c -fuzz=Fuzz -o $(OUT)/fuzz/$$(basename $$p).test ./$$p || exit 1; done'
	docker run --rm --platform linux/$(ARCH) --cap-add NET_ADMIN -v "$(CURDIR)":/src routal-daemons:$(ARCH) /src/hack/fuzz.sh $(FUZZTIME) /src/$(OUT)/fuzz

mutation: ## mutation testing: bugs are put in the code one by one, and the whole test suite must notice (slow; a report, not a gate)
	$(GO) sh -c 'for p in pkg/config pkg/nft pkg/services pkg/talospatch pkg/pd pkg/leases; do echo "== $$p"; \
		go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.5.0 unleash --integration --timeout-coefficient 10 ./$$p 2>&1 | \
		grep -E "LIVED|Killed:|Timed out:|efficacy"; done'

fuzz-firewall: ## random rules loaded in a real kernel, probed from every zone and compared with a model of router.yaml (ROUNDS=150, SEED= to replay)
	docker build --platform linux/$(ARCH) --target daemons -t routal-daemons:$(ARCH) .
	mkdir -p $(OUT)/fuzz
	$(GO) sh -c 'CGO_ENABLED=0 GOARCH=$(ARCH) go test -c -o $(OUT)/fuzz/firewall.test ./test/firewall'
	docker run --rm --platform linux/$(ARCH) --privileged -e ROUTAL_FIREWALL_NETNS=1 -e ROUNDS=$(ROUNDS) -e SEED=$(SEED) \
		-v "$(CURDIR)":/src:ro routal-daemons:$(ARCH) /src/$(OUT)/fuzz/firewall.test -test.v -test.run TestRulesetMatchesModel

integration: ## data-plane tests in network namespaces (router, LAN, NAS VLAN, WAN, VPN peer)
	docker build --platform linux/$(ARCH) --target integration -t routal-integration:$(ARCH) .
	docker run --rm --platform linux/$(ARCH) --privileged routal-integration:$(ARCH) /test/run.sh

cover: ## coverage of the unit tests and of a routald run in make integration, merged: $(OUT)/cover/
	rm -rf $(OUT)/cover && mkdir -p $(OUT)/cover/unit $(OUT)/cover/integration
	$(GO) go test -cover -coverpkg=./... ./... -args -test.gocoverdir=/src/$(OUT)/cover/unit >/dev/null
	docker build --platform linux/$(ARCH) --target integration -t routal-integration:$(ARCH) .
	docker run --rm --platform linux/$(ARCH) --privileged -e COVER=1 -v "$(CURDIR)/$(OUT)/cover/integration":/cover \
		routal-integration:$(ARCH) /test/run.sh | tail -1
	$(GO) sh -c 'go tool covdata textfmt -i=$(OUT)/cover/unit,$(OUT)/cover/integration -o $(OUT)/cover/cover.out && \
		go tool cover -func=$(OUT)/cover/cover.out > $(OUT)/cover/func.txt && \
		go tool cover -html=$(OUT)/cover/cover.out -o $(OUT)/cover/cover.html && \
		echo "report: $(OUT)/cover/cover.html" && tail -1 $(OUT)/cover/func.txt'

clab: ## the integration checks on a containerlab topology (containerlab runs in Docker; KEEP=1 keeps the lab)
	docker build --platform linux/$(ARCH) --target integration -t routal-integration:$(ARCH) .
	ARCH=$(ARCH) test/clab/run.sh

extension: ## build the system extension as a flat tarball: $(OUT)/routald-$(ARCH).tar
	mkdir -p $(OUT)
	sed 's/\$$VERSION/$(VERSION)/' extension/manifest.yaml > $(OUT)/manifest.yaml
	docker build --platform linux/$(ARCH) --target extension --build-arg VERSION=$(VERSION) \
		-t routald-extension:$(VERSION)-$(ARCH) .
	hack/flatten-extension.sh routald-extension:$(VERSION)-$(ARCH) linux/$(ARCH) $(OUT)/routald-$(ARCH).tar $(OUT)/manifest.yaml

installer: extension ## build the Talos installer image with the extension: $(OUT)/installer-$(ARCH).tar (OCI)
	sed -e 's/$$ARCH/$(ARCH)/g' -e 's/$$TALOS_VERSION/$(TALOS_VERSION)/g' hack/imager-installer.yaml > $(OUT)/installer-profile.yaml
	docker run --rm -i --privileged -v "$(CURDIR)/$(OUT)":/out -v /dev:/dev \
		ghcr.io/siderolabs/imager:$(TALOS_VERSION) - < $(OUT)/installer-profile.yaml

push-installer: ## push the installer: make push-installer IMAGE=ghcr.io/<you>/routal-installer:<tag>
	@test -n "$(IMAGE)" || { echo "set IMAGE=registry/name:tag"; exit 1; }
	docker tag "$$(docker load -q -i $(OUT)/installer-$(ARCH).tar | sed -n 's/^Loaded image[^:]*: //p')" $(IMAGE)
	docker push $(IMAGE)

image: extension ## build a Talos metal disk image with the extension: $(OUT)/metal-$(ARCH).raw
	sed -e 's/$$ARCH/$(ARCH)/g' -e 's/$$TALOS_VERSION/$(TALOS_VERSION)/g' hack/imager-profile.yaml > $(OUT)/profile.yaml
	docker run --rm -i --privileged -v "$(CURDIR)/$(OUT)":/out -v /dev:/dev \
		ghcr.io/siderolabs/imager:$(TALOS_VERSION) - < $(OUT)/profile.yaml

vm-test: ## boot Talos TALOS_VERSION with the extension in a VM (tart on an Apple silicon Mac, QEMU with sudo on Linux) and check the daemons there; removes the VM and the image
	$(MAKE) image ARCH=$(VM_ARCH)
	hack/vm-test.sh $(TALOS_VERSION) $(OUT) $(VM_ARCH)

vm-upgrade-test: ## Linux: boot the build of test/vm/upgrade-from, upgrade it to this one with talosctl upgrade, check that the state is kept
	hack/vm-upgrade.sh $(TALOS_VERSION) $(OUT) $(VM_ARCH)

site: ## the project's web page (reference from the schema, examples, personas): $(OUT)/site/index.html
	$(GO) go run ./hack/site $(OUT)/site

clean:
	rm -f $(OUT)/*.tar $(OUT)/*.raw $(OUT)/*.yaml
