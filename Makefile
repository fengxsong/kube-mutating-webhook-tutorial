REGISTRY = hub.ppmoney.io/getting-started
BINARY = mutating-webhook-demo

dep:
	dep ensure

build:
	CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o ${BINARY} .

docker: build
	docker build --no-cache -t ${REGISTRY}/${BINARY}:latest .
	docker push ${REGISTRY}/${BINARY}:latest

all: docker
	rm -f ${BINARY}

manifest:
	bash deployment/webhook-create-signed-cert.sh
	sed -e "s|\$${IMAGE}|${REGISTRY}/${BINARY}:latest|g" deployment/deployment.tmpl > deployment/deployment.yaml
	cat deployment/mutatingwebhook.yaml | bash deployment/webhook-patch-ca-bundle.sh | tee deployment/mutatingwebhook-ca-budle.yaml

deploy:
	kubectl create -f deployment/deployment.yaml -f deployment/service.yaml
	kubectl create -f deployment/mutatingwebhook-ca-budle.yaml

echo:
	@echo ">> current namespace is **default**"

test-label:
	kubectl label ns default adjust-tz=enabled

test:
	kubectl create -f deployment/test.yaml

clean:
	kubectl delete -f deployment/test.yaml
