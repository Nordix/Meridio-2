ARG USER=example-target
ARG UID=10011
ARG HOME=/home/${USER}

FROM ubuntu:24.04

ARG USER
ARG UID
ARG HOME

ENV DEBIAN_FRONTEND=noninteractive

# Install base packages
RUN apt-get update -y \
  && apt-get install -y --no-install-recommends \
     ca-certificates \
     iproute2 \
     tcpdump \
     net-tools \
     iputils-ping \
     netcat-traditional \
     wget \
     screen \
     xz-utils \
     strace \
     nftables \
     iperf3 \
     libcap2-bin \
     lksctp-tools \
     build-essential \
     cmake \
     libsctp-dev \
     libbz2-dev \
  && setcap 'cap_sys_ptrace,cap_dac_override+ep' /usr/bin/ss \
  && setcap 'cap_sys_ptrace,cap_dac_override+ep' /usr/bin/netstat \
  && setcap 'cap_net_raw+ep' /usr/bin/ping \
  && setcap 'cap_net_raw+ep' /usr/bin/tcpdump \
  && setcap 'cap_sys_ptrace+ep' /usr/bin/strace \
  && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*

# Build and install netperfmeter from source
RUN wget -q https://www.nntb.no/~dreibh/netperfmeter/download/netperfmeter-2.0.2.tar.xz \
  && tar xf netperfmeter-2.0.2.tar.xz \
  && cd netperfmeter-2.0.2 \
  && cmake . -DWITH_ICONS=OFF \
  && make -j$(nproc) \
  && make install \
  && cd .. \
  && rm -rf netperfmeter-2.0.2 netperfmeter-2.0.2.tar.xz

# Create non-root user
RUN groupadd --gid $UID $USER \
  && useradd $USER --create-home --home-dir $HOME --no-log-init --uid $UID --gid $UID \
  && chown -R :root "${HOME}" && chmod -R g+s=u "${HOME}"

WORKDIR $HOME

# Install ctraffic
ADD https://github.com/Nordix/ctraffic/releases/download/v1.7.0/ctraffic.gz ctraffic.gz
RUN gunzip ctraffic.gz && chmod a+x ctraffic

# Install mconnect
ADD https://github.com/Nordix/mconnect/releases/download/v2.2.0/mconnect.xz mconnect.xz
RUN unxz mconnect.xz && chmod a+x mconnect

USER ${UID}:${UID}
CMD ["tail", "-f", "/dev/null"]
