build:
	./scripts/build.sh

test:
	./scripts/test.sh

vet:
	./scripts/vet.sh

package-providers:
	./scripts/package-providers.sh

package-encryption-bins:
	./scripts/package-encryption-bins.sh

docker-build:
	docker build -t obot-platform/providers:latest --target providers .
