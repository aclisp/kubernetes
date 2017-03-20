#!/bin/bash

IMGNAME=nodeport-allocator
IMGVER=v0.0.3

go fmt ./... && go vet ./... && go build -o $IMGNAME && \
docker build -t 101.226.20.190:5000/$IMGNAME:$IMGVER -f Dockerfile-nodeport .
