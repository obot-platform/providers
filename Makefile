PROVIDER_DIRS := $(sort $(patsubst %/go.mod,%,$(wildcard *-provider/go.mod)))

IMAGE_REPOSITORY ?= ghcr.io/obot-platform/providers
IMAGE_TAG ?= main
IMAGE_PLATFORMS ?=
IMAGE_PUSH ?= false
IMAGE_BUILD_COMMAND ?=

.PHONY: test all-images provider-dirs

test:
	cd github-auth-provider && go test ./... && cd ..
	cd google-auth-provider && go test ./... && cd ..

provider-dirs:
	@printf '%s\n' $(PROVIDER_DIRS)

all-images:
	@set -e; \
	build_command="$(IMAGE_BUILD_COMMAND)"; \
	if [ -z "$${build_command}" ]; then \
		if [ "$(IMAGE_PUSH)" = "true" ] || [ -n "$(IMAGE_PLATFORMS)" ]; then \
			build_command="docker buildx build"; \
		else \
			build_command="docker build"; \
		fi; \
	fi; \
	platforms_arg=""; \
	if [ -n "$(IMAGE_PLATFORMS)" ]; then \
		platforms_arg="--platform $(IMAGE_PLATFORMS)"; \
	fi; \
	push_arg=""; \
	if [ "$(IMAGE_PUSH)" = "true" ]; then \
		push_arg="--push"; \
	fi; \
	for provider_dir in $(PROVIDER_DIRS); do \
		tag="$(IMAGE_REPOSITORY)/$${provider_dir}:$(IMAGE_TAG)"; \
		echo "Building $${tag}"; \
		$${build_command} \
			--pull \
			$${platforms_arg} \
			$${push_arg} \
			--build-arg PROVIDER_DIR=$${provider_dir} \
			-t "$${tag}" \
			-f Dockerfile .; \
	done
