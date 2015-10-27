FROM debian:wheezy
MAINTAINER Huang Hao <huanghao@yy.com>

RUN apt-get update && apt-get install -y iptables && rm -rf /var/lib/apt/lists/*

ADD kube-proxy /
