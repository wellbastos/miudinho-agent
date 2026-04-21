# Image URL to use all building/pushing image targets
IMG ?= miudinho-agent
NAMESPACE ?= o11y
VERSION ?= 0.1.0

LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint
HELM ?= helm

.PHONY: all
all: fmt vet build

##@ Tooling
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

$(CONTROLLER_GEN): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0

$(GOLANGCI_LINT): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.4.0

##@ Development
.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...

.PHONY: test
test:
	go test ./... -coverprofile cover.out

.PHONY: build
build:
	go build -o bin/manager ./cmd/manager

.PHONY: run
run: fmt vet
	go run ./cmd/manager

##@ Code generation
.PHONY: generate
generate: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) object:headerFile="" paths="./..."

.PHONY: manifests
manifests: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) \
	  crd:crdVersions=v1 \
	  rbac:roleName=miudinho-agent \
	  paths="./..." \
	  output:crd:artifacts:config=config/crd/bases
	cp config/crd/bases/*.yaml charts/miudinho-agent/crds/

.PHONY: install
install: manifests
	$(HELM) upgrade --install miudinho-agent ./charts/miudinho-agent --namespace $(NAMESPACE) --create-namespace --set image.repository=$(IMG) --set image.tag=$(VERSION)

.PHONY: uninstall
uninstall:
	$(HELM) uninstall miudinho-agent --namespace $(NAMESPACE)

.PHONY: deploy
deploy: manifests
	$(HELM) upgrade --install miudinho-agent ./charts/miudinho-agent --namespace $(NAMESPACE) --create-namespace --set image.repository=$(IMG) --set image.tag=$(VERSION)

.PHONY: undeploy
undeploy:
	$(HELM) uninstall miudinho-agent --namespace $(NAMESPACE)

##@ Docker
.PHONY: docker-build
docker-build:
	docker build -t ${IMG}:${VERSION} .

.PHONY: docker-push
docker-push:
	docker push ${IMG}:${VERSION}

##@ Samples
.PHONY: apply-samples
apply-samples:
	kubectl apply -f examples/autoremediationpolicy.yaml
	kubectl apply -f examples/slopolicy.yaml
