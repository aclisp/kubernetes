FROM debian:jessie

RUN DEBIAN_FRONTEND=noninteractive apt-get update -y \
    && DEBIAN_FRONTEND=noninteractive apt-get -yy -q \
    install \
    iptables \
    procps \
    net-tools \
    iputils-tracepath \
    traceroute \
    ca-certificates \
    file \
    util-linux \
    && DEBIAN_FRONTEND=noninteractive apt-get autoremove -y

ADD ./etcd /usr/local/bin/etcd
ADD ./etcdctl /usr/local/bin/etcdctl
