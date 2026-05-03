ARG USER=example-target
ARG UID=10011
ARG HOME=/home/${USER}

FROM ubuntu:24.04

ARG USER
ARG UID
ARG HOME

ENV DEBIAN_FRONTEND=noninteractive

# Install base packages and netperfmeter
RUN apt-get update -y \
  && apt-get install -y --no-install-recommends \
     ca-certificates \
     gnupg \
     software-properties-common \
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
  && add-apt-repository -y ppa:dreibh/ppa \
  && apt-get update \
  && apt-get install -y --no-install-recommends netperfmeter-all \
  && setcap 'cap_sys_ptrace,cap_dac_override+ep' /usr/bin/ss \
  && setcap 'cap_sys_ptrace,cap_dac_override+ep' /usr/bin/netstat \
  && setcap 'cap_net_raw+ep' /usr/bin/ping \
  && setcap 'cap_net_raw+ep' /usr/bin/tcpdump \
  && setcap 'cap_sys_ptrace+ep' /usr/bin/strace \
  && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*

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
