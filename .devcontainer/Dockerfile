FROM docker.io/library/golang:1.27.1-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
        git \
        make \
        postgresql-client \
        iputils-ping \
        netcat-openbsd \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -ms /bin/bash vscode
USER vscode
WORKDIR /workspace
