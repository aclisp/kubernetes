#!/bin/bash

IMGNAME=my-java-tomcat
IMGVER=v0.0.1

docker build -t 101.226.20.190:5000/$IMGNAME:$IMGVER -f Dockerfile-tomcat .
