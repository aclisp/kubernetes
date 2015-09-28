.PHONY:	clean build push

IMAGE = kube-controller-manager
TAG = 1.0.6

build: clean
	docker build -t sigmas/$(IMAGE):$(TAG) .

push: build
	docker push sigmas/$(IMAGE):$(TAG)

all:	push
