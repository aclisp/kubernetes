# build the hyperkube image.

VERSION=v1.1.8.3

all:
	cp /root/Gopath/bin/hyperkube .
	docker build -t sigmas/hyperkube:${VERSION} .
	docker push sigmas/hyperkube:${VERSION}

.PHONY: all
