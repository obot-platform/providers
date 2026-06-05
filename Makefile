build:
	./scripts/build.sh

test:
	cd github-auth-provider && go test ./... && cd ..
	cd google-auth-provider && go test ./... && cd ..

package-providers:
	./scripts/package-providers.sh

package-encryption-bins:
	./scripts/package-encryption-bins.sh

docker-build:
	docker build -t obot-platform/providers:latest --target providers .
