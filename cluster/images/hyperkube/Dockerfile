FROM debian:jessie

RUN DEBIAN_FRONTEND=noninteractive apt-get update -y \
    && DEBIAN_FRONTEND=noninteractive apt-get -yy -q \
    install \
    iptables \
    ca-certificates \
    file \
    util-linux \
    net-tools \
    procps \
    iproute2 \
    iputils-tracepath \
    traceroute \
    && DEBIAN_FRONTEND=noninteractive apt-get autoremove -y

RUN cp /usr/bin/nsenter /nsenter

COPY hyperkube /hyperkube
RUN chmod a+rx /hyperkube
