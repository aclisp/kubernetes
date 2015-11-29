.PHONY:	clean build push

IMAGE = etcd
TAG = v2.0.12
ETCD_VERSION = 2.0.12
OUTPUT_DIR = $(IMAGE)-v$(ETCD_VERSION)-linux-amd64

clean:
	rm -rf $(OUTPUT_DIR) $(IMAGE)-v$(ETCD_VERSION)-linux-amd64.tar.gz etcd etcdctl

build: clean
	curl -L -O https://github.com/coreos/etcd/releases/download/v$(ETCD_VERSION)/$(IMAGE)-v$(ETCD_VERSION)-linux-amd64.tar.gz
	tar xzvf $(IMAGE)-v$(ETCD_VERSION)-linux-amd64.tar.gz
	cp $(OUTPUT_DIR)/etcd .
	cp $(OUTPUT_DIR)/etcdctl .
	sudo docker build -t sigmas/$(IMAGE):$(TAG) .

push: build
	sudo docker push sigmas/$(IMAGE):$(TAG)

all:	push
