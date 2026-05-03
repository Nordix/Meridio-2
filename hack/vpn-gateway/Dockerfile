FROM ubuntu:24.04 AS bird-builder

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    flex \
    bison \
    libreadline-dev \
    wget \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /tmp
RUN wget https://bird.network.cz/download/bird-3.1.2.tar.gz \
    && tar -xzf bird-3.1.2.tar.gz \
    && cd bird-3.1.2 \
    && ./configure \
    && make -j$(nproc) \
    && make install

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        lksctp-tools \
        iproute2 \
        tcpdump \
        iputils-ping \
        ethtool \
        libreadline8t64 \
        xz-utils \
        software-properties-common \
    && add-apt-repository -y ppa:dreibh/ppa \
    && apt-get update \
    && apt-get install -y --no-install-recommends netperfmeter-all \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*

COPY --from=bird-builder /usr/local/sbin/bird /usr/sbin/bird
COPY --from=bird-builder /usr/local/sbin/birdc /usr/sbin/birdc

ADD https://github.com/Nordix/ctraffic/releases/download/v1.7.0/ctraffic.gz /tmp/ctraffic.gz
RUN gunzip /tmp/ctraffic.gz \
  && chmod u+x /tmp/ctraffic \
  && mv /tmp/ctraffic /usr/bin/

ADD https://github.com/Nordix/mconnect/releases/download/v2.2.0/mconnect.xz /tmp/mconnect.xz
RUN unxz /tmp/mconnect.xz \
  && chmod u+x /tmp/mconnect \
  && mv /tmp/mconnect /usr/bin/ \
  && mkdir -p /etc/bird/ /run/bird /usr/local/var/run

ARG BIRD_CONFIG_PATH=.

COPY $BIRD_CONFIG_PATH/bird-common.conf /etc/bird/
COPY $BIRD_CONFIG_PATH/bird-gw.conf /etc/bird/
COPY $BIRD_CONFIG_PATH/bird-gw-no-default.conf /etc/bird/
COPY $BIRD_CONFIG_PATH/bird-filler-net.conf /etc/bird/

COPY $BIRD_CONFIG_PATH/init.sh /init.sh
RUN chmod +x /init.sh

CMD ["/init.sh"]
