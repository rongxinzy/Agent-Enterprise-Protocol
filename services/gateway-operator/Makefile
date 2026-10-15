# Kubebuilder project integrated with the repository's root Go module.
.PHONY: generate manifests test build run install deploy
generate manifests:
	bash generate.sh
test:
	cd ../.. && go test ./services/gateway-operator/... -count=1
build:
	cd ../.. && go build ./services/gateway-operator/cmd
run:
	cd ../.. && go run ./services/gateway-operator/cmd
install: manifests
	kubectl apply -k config/crd
deploy: manifests
	cd ../.. && ko apply -k services/gateway-operator/config/default
