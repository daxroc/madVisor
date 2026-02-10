MODULE     := github.com/daxroc/madVisor
IMAGE_REPO ?= daxroc/madvisor
VERSION    := $(strip $(shell cat VERSION))
GIT_COMMIT := $(or $(strip $(shell git rev-parse --short HEAD 2>/dev/null)),unknown)
GIT_BRANCH := $(or $(strip $(shell git symbolic-ref --short HEAD 2>/dev/null)),unknown)
TAG        ?= $(VERSION)

LDFLAGS := -s -w \
	-X 'main.version=$(VERSION)' \
	-X 'main.commit=$(GIT_COMMIT)' \
	-X 'main.branch=$(GIT_BRANCH)'

.DEFAULT_GOAL := help

.PHONY: help build test test-v run-local run-dummy run-viz docker-build deploy undeploy clean version

help: ## Show this help
	@printf "\n\033[1mmadVisor\033[0m — real-time pod metric visualizer\n\n"
	@printf "\033[33mUsage:\033[0m make [target]\n\n"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
	@printf "\n"

version: ## Print version info
	@echo "version: $(VERSION)"
	@echo "commit:  $(GIT_COMMIT)"
	@echo "branch:  $(GIT_BRANCH)"

build: ## Build both binaries to bin/
	go build -ldflags "$(LDFLAGS)" -o bin/madvisor-dummy ./cmd/madvisor-dummy/
	go build -ldflags "$(LDFLAGS)" -o bin/madvisor ./cmd/madvisor/

test: ## Run tests with race detector
	go test -race ./...

test-v: ## Run tests verbose with race detector
	go test -race -v ./...

run-dummy: ## Start the dummy metrics producer on :8080
	go run ./cmd/madvisor-dummy/

run-viz: ## Start madVisor TUI (expects dummy on :8080)
	METRIC_TARGETS=localhost:8080 go run ./cmd/madvisor/

run-local: ## Start dummy + madVisor together
	@echo "Starting madvisor-dummy in background on :8080..."
	@go run ./cmd/madvisor-dummy/ &
	@sleep 1
	@echo "Starting madVisor..."
	@METRIC_TARGETS=localhost:8080 go run ./cmd/madvisor/
	@echo "Cleaning up..."
	@-pkill -f "madvisor-dummy" 2>/dev/null || true

docker-build: ## Build Docker images
	docker build -f docker/Dockerfile.madvisor-dummy \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg GIT_BRANCH=$(GIT_BRANCH) \
		-t $(IMAGE_REPO)-dummy:$(TAG) \
		-t $(IMAGE_REPO)-dummy:latest .
	docker build -f docker/Dockerfile.madvisor \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg GIT_BRANCH=$(GIT_BRANCH) \
		-t $(IMAGE_REPO):$(TAG) \
		-t $(IMAGE_REPO):latest .

deploy: docker-build ## Build images and deploy to Kubernetes
	kubectl apply -f examples/k8s/pod.yaml

undeploy: ## Remove Kubernetes resources
	kubectl delete -f examples/k8s/pod.yaml --ignore-not-found

clean: ## Remove build artifacts and stop background processes
	rm -rf bin/
	@-pkill -f "madvisor-dummy" 2>/dev/null || true
