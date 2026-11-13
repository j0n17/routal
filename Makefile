VERSION       ?= 0.1.0
TALOS_VERSION ?= v1.15.0-beta.0
ARCH          ?= arm64
GO_IMAGE      ?= golang:1.26
OUT           ?= _out

GO = docker run --rm -v "$(CURDIR)":/src -v routal-gocache:/root/.cache/go-build -v routal-gomod:/go/pkg/mod -w /src $(GO_IMAGE)

.PHONY: test check integration extension installer push-installer image clean

test: ## run unit tests (in Docker)
	$(GO) sh -c 'test -z "$$(gofmt -l .)" && go vet ./... && go test ./...'

check: test ## also check every example's nftables ruleset and daemon configs with the real tools
	docker build --platform linux/$(ARCH) --target daemons -t routal-daemons:$(ARCH) .
	docker run --rm --platform linux/$(ARCH) --cap-add NET_ADMIN -v "$(CURDIR)":/src:ro routal-daemons:$(ARCH) /src/hack/check-configs.sh

integration: ## data-plane tests in network namespaces (router, LAN, NAS VLAN, WAN, VPN peer)
	docker build --platform linux/$(ARCH) --target integration -t routal-integration:$(ARCH) .
	docker run --rm --platform linux/$(ARCH) --privileged routal-integration:$(ARCH)

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

clean:
	rm -f $(OUT)/*.tar $(OUT)/*.raw $(OUT)/*.yaml
