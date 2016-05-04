all: skydns

skydns:
	CGO_ENABLED=0 go build -a -installsuffix cgo --ldflags '-w' github.com/skynetservices/skydns

container: skydns
	sudo docker build -t sigmas/skydns .

push:
	sudo docker push sigmas/skydns

clean:
	rm -f skydns
