VERSION       ?= 0.1.0
TALOS_VERSION ?= v1.15.0-beta.0
ARCH          ?= arm64
GO_IMAGE      ?= golang:1.26
OUT           ?= _out

GO = docker run --rm -v "$(CURDIR)":/src -v routal-gocache:/root/.cache/go-build -v routal-gomod:/go/pkg/mod -w /src $(GO_IMAGE)

.PHONY: test check extension image clean

test: ## run unit tests (in Docker)
	$(GO) sh -c 'test -z "$$(gofmt -l .)" && go vet ./... && go test ./...'

check: test ## also syntax-check the dnsmasq config of every example with the real dnsmasq
	mkdir -p $(OUT)/dnsmasq-check
	$(GO) sh -c 'for f in examples/*/router.yaml; do go run ./cmd/routald render-dnsmasq $$f > $(OUT)/dnsmasq-check/$$(basename $$(dirname $$f)).conf; done'
	docker run --rm -v "$(CURDIR)/$(OUT)/dnsmasq-check":/c alpine:3.22 sh -c \
		'apk add -q dnsmasq >/dev/null && for f in /c/*.conf; do printf "%s: " $$f; dnsmasq --test --conf-file=$$f || exit 1; done'

extension: ## build the system extension as a flat tarball: $(OUT)/routald-$(ARCH).tar
	mkdir -p $(OUT)
	sed 's/\$$VERSION/$(VERSION)/' extension/manifest.yaml > $(OUT)/manifest.yaml
	docker build --platform linux/$(ARCH) --target extension --build-arg VERSION=$(VERSION) \
		-t routald-extension:$(VERSION)-$(ARCH) .
	hack/flatten-extension.sh routald-extension:$(VERSION)-$(ARCH) linux/$(ARCH) $(OUT)/routald-$(ARCH).tar $(OUT)/manifest.yaml

image: extension ## build a Talos metal disk image with the extension: $(OUT)/metal-$(ARCH).raw
	sed -e 's/$$ARCH/$(ARCH)/g' -e 's/$$TALOS_VERSION/$(TALOS_VERSION)/g' hack/imager-profile.yaml > $(OUT)/profile.yaml
	docker run --rm -i --privileged -v "$(CURDIR)/$(OUT)":/out -v /dev:/dev \
		ghcr.io/siderolabs/imager:$(TALOS_VERSION) - < $(OUT)/profile.yaml

clean:
	rm -f $(OUT)/*.tar $(OUT)/*.raw $(OUT)/*.yaml
