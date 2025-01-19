FROM openjdk:17-jdk-slim

ENV DEBIAN_FRONTEND=noninteractive

ARG POSTGRES_HOST
ARG POSTGRES_PORT
ARG POSTGRES_USER
ARG POSTGRES_PASSWORD
ARG POSTGRES_DB

ENV PGPASSWORD=${POSTGRES_PASSWORD}

RUN echo "deb http://deb.debian.org/debian bullseye main contrib non-free" > /etc/apt/sources.list

RUN apt-get update && apt-get install -y \
    locales \
    && locale-gen "pt_BR.UTF-8" \
    && dpkg-reconfigure --frontend=noninteractive locales \
    && apt-get install -y \
    coreutils wget apt-utils gnupg2 software-properties-common file libfreetype6 ntp ttf-mscorefonts-installer fontconfig

RUN fc-cache -fv

RUN chmod -R 777 /usr/share/fonts/truetype/msttcorefonts

RUN apt-get update && \
    apt-get install --no-install-recommends -y \
    dbus systemd systemd-cron rsyslog iproute2 python-is-python3 python3 python3-apt sudo bash ca-certificates && \
    apt-get clean && \
    rm -rf /usr/share/doc/* /usr/share/man/* /var/lib/apt/lists/* /tmp/* /var/tmp/*

RUN apt-get update && apt-get install -y postgresql-client


RUN mkdir -p /opt/e-SUS/webserver/chaves
RUN mkdir /backups
RUN mkdir -p /var/www/html
WORKDIR /var/www/html

COPY ./pec.jar pec.jar

COPY ./init.sh /init.sh

RUN chmod +x /init.sh

ENTRYPOINT ["/init.sh"]